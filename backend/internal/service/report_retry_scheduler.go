package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	reportRetrySchedulerLeaderLockKey = "reports:retry:leader"
	reportRetrySchedulerLockTTL       = 5 * time.Minute
	reportRetrySchedulerLockRenew     = 90 * time.Second
	reportRetrySchedulerTick          = 1 * time.Minute
	reportRetrySchedulerBatch         = 50
	reportRetrySchedulerJobTimeout    = 5 * time.Minute
	// reportRetrySchedulerRunBudget 单轮总预算：要覆盖生成任务（8 分钟）与推送重试。
	reportRetrySchedulerRunBudget = 12 * time.Minute
)

var reportRetrySchedulerReleaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

var reportRetrySchedulerRenewScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`)

// ReportRetrySchedulerService 每 30 分钟重试生成/推送失败的报告任务。
//
// 与报告调度器一样受 REPORT_SCHEDULER_ENABLED 控制（dev 不跑），并用 Redis
// leader lock 保证多实例只有一个执行。任务失败后由报告服务把 next_retry_at
// 推到 30 分钟后；本服务每分钟扫一次到期任务。
type ReportRetrySchedulerService struct {
	reportService *ReportService
	retryRepo     ReportRetryRepository
	redisClient   *redis.Client
	cfg           *config.Config

	instanceID string
	startOnce  sync.Once
	stopOnce   sync.Once
	stopCtx    context.Context
	stop       context.CancelFunc
	wg         sync.WaitGroup
}

// NewReportRetrySchedulerService 构造重试调度器。
func NewReportRetrySchedulerService(
	reportService *ReportService,
	retryRepo ReportRetryRepository,
	redisClient *redis.Client,
	cfg *config.Config,
) *ReportRetrySchedulerService {
	return &ReportRetrySchedulerService{
		reportService: reportService,
		retryRepo:     retryRepo,
		redisClient:   redisClient,
		cfg:           cfg,
		instanceID:    uuid.NewString(),
	}
}

// Start 启动后台重试循环。
func (s *ReportRetrySchedulerService) Start() {
	if s == nil || s.reportService == nil || s.retryRepo == nil {
		return
	}
	if s.cfg != nil && !s.cfg.Report.SchedulerEnabled {
		return
	}
	s.startOnce.Do(func() {
		s.stopCtx, s.stop = context.WithCancel(context.Background())
		s.wg.Add(1)
		logger.LegacyPrintf("service.report", "[ReportRetry] scheduler started (tick=1m, retry_interval=30m)")
		go s.run()
	})
}

// Stop 停止后台重试循环。
func (s *ReportRetrySchedulerService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.stop != nil {
			s.stop()
		}
	})
	s.wg.Wait()
}

func (s *ReportRetrySchedulerService) run() {
	defer s.wg.Done()

	ticker := time.NewTicker(reportRetrySchedulerTick)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.runOnce()
		case <-s.stopCtx.Done():
			return
		}
	}
}

func (s *ReportRetrySchedulerService) runOnce() {
	if s == nil || s.reportService == nil || s.retryRepo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.stopCtx, reportRetrySchedulerRunBudget)
	defer cancel()

	cfg, err := s.reportService.GetReportConfig(ctx)
	if err != nil || cfg == nil || !cfg.Enabled {
		return
	}

	release, ok := s.tryAcquireLeaderLock(ctx)
	if !ok {
		return
	}
	if release != nil {
		defer release()
	}
	stopRenew := s.startLockRenewer()
	defer stopRenew()

	now := timezone.Now()
	// 生成任务（首次尝试 + 失败重试统一走任务表）：带租约并发领取，
	// 单个任务失败/卡住不影响其他人；本轮预算用尽后剩余任务保持 pending。
	if result, genErr := s.reportService.RunGenerationTasks(ctx, reportTaskWorkerBudget, reportTaskConcurrency); genErr != nil {
		logger.LegacyPrintf("service.report", "[ReportRetry] run generation tasks: %v", genErr)
	} else if result.Processed > 0 {
		logger.LegacyPrintf("service.report",
			"[ReportRetry] generation tasks processed=%d generated=%d skipped=%d failed=%d manual=%d rescheduled=%d",
			result.Processed, result.Generated, result.Skipped, result.Failed, result.ManualRepair, result.Rescheduled)
	}
	if tasks, err := s.retryRepo.ListDue(ctx, domain.ReportRetryKindPush, now, reportRetrySchedulerBatch); err != nil {
		logger.LegacyPrintf("service.report", "[ReportRetry] list due push tasks: %v", err)
	} else {
		for _, task := range tasks {
			s.processPushTask(ctx, task, cfg)
		}
	}
}

func (s *ReportRetrySchedulerService) processPushTask(ctx context.Context, task ReportRetryTask, cfg *ReportLLMConfig) {
	if task.ReportID <= 0 || task.GroupID <= 0 {
		_ = s.retryRepo.MarkDone(context.Background(), task.ID)
		return
	}
	report, err := s.reportService.GetReport(ctx, task.ReportID)
	if err != nil || report == nil || report.Status != domain.ReportStatusDone {
		_ = s.retryRepo.MarkDone(context.Background(), task.ID)
		return
	}
	if report.PushedAt != nil {
		_ = s.retryRepo.MarkDone(context.Background(), task.ID)
		return
	}
	if cfg == nil || !cfg.FeishuEnabled {
		// 总开关关闭：暂停，等下次扫描；不标记 done，避免重新开启后丢失补发。
		next := timezone.Now().Add(reportRetryInterval)
		updated := task
		updated.NextRetryAt = &next
		_, _ = s.retryRepo.UpsertPush(ctx, &updated)
		return
	}
	if !reportTypePushEnabled(cfg, report.Type) {
		_ = s.retryRepo.MarkDone(context.Background(), task.ID)
		return
	}
	optIn, err := s.reportService.reportRepo.IsReportPushEnabled(ctx, report.UserID)
	if err != nil || !optIn {
		_ = s.retryRepo.MarkDone(context.Background(), task.ID)
		return
	}
	group, err := s.reportService.groupRepo.GetGroup(ctx, task.GroupID)
	if err != nil || group == nil || !group.Enabled {
		_ = s.retryRepo.MarkDone(context.Background(), task.ID)
		return
	}
	groups, err := s.reportService.groupRepo.ListEnabledGroupsForUser(ctx, report.UserID)
	if err != nil {
		s.reschedulePush(ctx, task, err)
		return
	}
	found := false
	for _, g := range groups {
		if g.ID == group.ID {
			found = true
			break
		}
	}
	if !found {
		_ = s.retryRepo.MarkDone(context.Background(), task.ID)
		return
	}
	// 同一 webhook 已经被成功投递过：只把该群标记成功，不重复发卡片。
	if s.webhookAlreadySucceeded(ctx, report.ID, *group) {
		_ = s.reportService.groupRepo.UpsertDelivery(context.Background(), &ReportPushDelivery{
			ReportID:    report.ID,
			GroupID:     group.ID,
			GroupName:   group.Name,
			Status:      domain.ReportPushStatusSuccess,
			AttemptedAt: timezone.Now(),
		})
		_ = s.retryRepo.MarkDone(context.Background(), task.ID)
		s.reportService.refreshReportPushState(ctx, report)
		return
	}

	jobCtx, cancel := context.WithTimeout(ctx, reportRetrySchedulerJobTimeout)
	defer cancel()
	if err := s.reportService.pushReportToGroup(jobCtx, cfg, report, *group, true); err != nil {
		logger.LegacyPrintf("service.report",
			"[ReportRetry] push retry failed report=%d group=%d err=%v", report.ID, group.ID, err)
		return
	}
	logger.LegacyPrintf("service.report",
		"[ReportRetry] push retry done report=%d group=%d", report.ID, group.ID)
}

func (s *ReportRetrySchedulerService) reschedulePush(ctx context.Context, task ReportRetryTask, retryErr error) {
	next := timezone.Now().Add(reportRetryInterval)
	updated := task
	updated.NextRetryAt = &next
	updated.LastError = truncateReportError(retryErr.Error())
	_, _ = s.retryRepo.UpsertPush(ctx, &updated)
}

// webhookAlreadySucceeded 判断该群的 webhook 是否已被同一份报告成功投递过。
func (s *ReportRetrySchedulerService) webhookAlreadySucceeded(ctx context.Context, reportID int64, target ReportFeishuGroup) bool {
	url := strings.TrimSpace(target.WebhookURL)
	if url == "" {
		return false
	}
	deliveries, err := s.reportService.groupRepo.ListDeliveriesByReportIDs(ctx, []int64{reportID})
	if err != nil {
		return false
	}
	groups, err := s.reportService.groupRepo.ListGroups(ctx)
	if err != nil {
		return false
	}
	byID := make(map[int64]ReportFeishuGroup, len(groups))
	for _, g := range groups {
		byID[g.ID] = g
	}
	for _, d := range deliveries[reportID] {
		if d.Status != domain.ReportPushStatusSuccess {
			continue
		}
		if g, ok := byID[d.GroupID]; ok && strings.TrimSpace(g.WebhookURL) == url {
			return true
		}
	}
	return false
}

func reportTypePushEnabled(cfg *ReportLLMConfig, reportType string) bool {
	if cfg == nil {
		return false
	}
	switch reportType {
	case domain.ReportTypeDaily:
		return cfg.FeishuPushDaily
	case domain.ReportTypeWeekly:
		return cfg.FeishuPushWeekly
	case domain.ReportTypeMonthly:
		return cfg.FeishuPushMonthly
	default:
		return false
	}
}

// =========================
// Redis leader lock（与报告调度器同构，key 独立）
// =========================

func (s *ReportRetrySchedulerService) tryAcquireLeaderLock(ctx context.Context) (func(), bool) {
	if s == nil {
		return nil, false
	}
	if s.redisClient == nil {
		return nil, true
	}
	ok, err := s.redisClient.SetNX(ctx, reportRetrySchedulerLeaderLockKey, s.instanceID, reportRetrySchedulerLockTTL).Result()
	if err != nil || !ok {
		return nil, false
	}
	release := func() {
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = reportRetrySchedulerReleaseScript.Run(rctx, s.redisClient, []string{reportRetrySchedulerLeaderLockKey}, s.instanceID).Result()
	}
	return release, true
}

// startLockRenewer 持有锁期间续期，避免批量任务超过 5 分钟被第二实例抢占。
func (s *ReportRetrySchedulerService) startLockRenewer() func() {
	if s == nil || s.redisClient == nil {
		return func() {}
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(reportRetrySchedulerLockRenew)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, _ = reportRetrySchedulerRenewScript.Run(rctx, s.redisClient,
					[]string{reportRetrySchedulerLeaderLockKey}, s.instanceID,
					reportRetrySchedulerLockTTL.Milliseconds()).Result()
				cancel()
			}
		}
	}()
	return func() {
		close(done)
		wg.Wait()
	}
}
