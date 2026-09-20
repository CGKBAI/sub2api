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
	"github.com/robfig/cron/v3"
)

const (
	reportSchedulerJobName = "user_reports"

	reportSchedulerLeaderLockKey = "reports:scheduler:leader"
	reportSchedulerLeaderLockTTL = 5 * time.Minute

	// 锁续期周期：TTL 的 1/3，长任务（批量生成最长 30min）期间持续续期，
	// 防止锁中途过期被第二实例抢占造成重复生成/重复推送
	reportSchedulerLockRenewInterval = 90 * time.Second

	reportSchedulerLastRunKeyPrefix = "reports:scheduler:last_run:"

	reportSchedulerTickInterval = 1 * time.Minute

	// 单种报告类型的生成预算（批量串行 LLM 调用）
	reportSchedulerJobTimeout = 30 * time.Minute
)

var reportSchedulerCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

var reportSchedulerReleaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

var reportSchedulerRenewScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`)

// ReportSchedulerService 后台定时生成日报/周报/月报。
//
// 每分钟 tick 一次，按配置里的 cron 表达式（默认每日 20:00 日报、周五 20:10 周报、
// 每月 1 日 20:20 上月月报）触发；Redis leader lock 保证多实例（灰度并行）只有一个实例执行；
// REPORT_SCHEDULER_ENABLED=false 可整体禁用（canary 用）。
type ReportSchedulerService struct {
	reportService *ReportService
	redisClient   *redis.Client
	cfg           *config.Config

	instanceID string
	loc        *time.Location

	startOnce sync.Once
	stopOnce  sync.Once
	stopCtx   context.Context
	stop      context.CancelFunc
	wg        sync.WaitGroup
}

// NewReportSchedulerService 构造定时报告服务。
func NewReportSchedulerService(
	reportService *ReportService,
	redisClient *redis.Client,
	cfg *config.Config,
) *ReportSchedulerService {
	loc := time.Local
	if cfg != nil && strings.TrimSpace(cfg.Timezone) != "" {
		if parsed, err := time.LoadLocation(strings.TrimSpace(cfg.Timezone)); err == nil && parsed != nil {
			loc = parsed
		}
	}
	return &ReportSchedulerService{
		reportService: reportService,
		redisClient:   redisClient,
		cfg:           cfg,
		instanceID:    uuid.NewString(),
		loc:           loc,
	}
}

// Start 启动后台循环。
func (s *ReportSchedulerService) Start() {
	if s == nil {
		return
	}
	if s.cfg != nil && !s.cfg.Report.SchedulerEnabled {
		return
	}
	s.startOnce.Do(func() {
		s.stopCtx, s.stop = context.WithCancel(context.Background())
		s.wg.Add(1)
		go s.run()
	})
}

// Stop 停止后台循环。
func (s *ReportSchedulerService) Stop() {
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

func (s *ReportSchedulerService) run() {
	defer s.wg.Done()

	ticker := time.NewTicker(reportSchedulerTickInterval)
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

func (s *ReportSchedulerService) runOnce() {
	if s == nil || s.reportService == nil {
		return
	}

	// 基础 ctx 只承载 stop 信号与配置/锁/last_run 读写；
	// 每种报告类型在循环内拿到独立的生成预算，互不挤占
	ctx, cancel := context.WithCancel(s.stopCtx)
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
	// 长任务期间持续续期锁，结束后停止续期 goroutine
	stopRenew := s.startLockRenewer()
	defer stopRenew()

	now := timezone.Now()
	if s.loc != nil {
		now = now.In(s.loc)
	}

	type scheduleDef struct {
		kind       string
		spec       string
		reportType string
	}
	defs := []scheduleDef{
		{kind: "daily", spec: cfg.DailySchedule, reportType: domain.ReportTypeDaily},
		{kind: "weekly", spec: cfg.WeeklySchedule, reportType: domain.ReportTypeWeekly},
		{kind: "monthly", spec: cfg.MonthlySchedule, reportType: domain.ReportTypeMonthly},
	}

	for _, d := range defs {
		spec := strings.TrimSpace(d.spec)
		if spec == "" {
			continue
		}
		sched, err := reportSchedulerCronParser.Parse(spec)
		if err != nil {
			logger.LegacyPrintf("service.report", "[ReportScheduler] parse %s schedule %q: %v", d.kind, spec, err)
			continue
		}

		lastRun := s.getLastRunAt(ctx, d.kind)
		base := lastRun
		if base.IsZero() {
			base = now.Add(-1 * time.Minute)
		}
		next := sched.Next(base)
		if next.IsZero() || next.After(now) {
			continue
		}

		// 先标记已跑，避免失败后每分钟重试轰炸 LLM
		s.setLastRunAt(ctx, d.kind, now)

		// 月报语义为「ref 所在自然月」，定时生成上月：ref 传上月 1 日
		ref := now
		if d.reportType == domain.ReportTypeMonthly {
			ref = timezone.StartOfMonth(now).AddDate(0, -1, 0)
		}

		genCtx, genCancel := context.WithTimeout(ctx, reportSchedulerJobTimeout)
		generated, err := s.reportService.GenerateForAllUsers(genCtx, d.reportType, ref, ReportTriggerScheduled)
		genCancel()
		if err != nil {
			logger.LegacyPrintf("service.report",
				"[ReportScheduler] generate %s reports: generated=%d err=%v", d.kind, generated, err)
		}
	}
}

// =========================
// Redis leader lock + last_run（仿 ops_scheduled_report_service）
// =========================

func (s *ReportSchedulerService) tryAcquireLeaderLock(ctx context.Context) (func(), bool) {
	if s == nil {
		return nil, false
	}
	if s.redisClient == nil {
		// 无 Redis（simple 模式）：单实例直接执行
		return nil, true
	}

	ok, err := s.redisClient.SetNX(ctx, reportSchedulerLeaderLockKey, s.instanceID, reportSchedulerLeaderLockTTL).Result()
	if err != nil || !ok {
		return nil, false
	}

	release := func() {
		rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rcancel()
		_, _ = reportSchedulerReleaseScript.Run(rctx, s.redisClient, []string{reportSchedulerLeaderLockKey}, s.instanceID).Result()
	}
	return release, true
}

// startLockRenewer 持有 Redis leader 锁期间周期性续期（compare-and-expire，
// 仅本实例的锁会被续）。返回停止函数；无 Redis（simple 模式）为 no-op。
func (s *ReportSchedulerService) startLockRenewer() (stop func()) {
	if s == nil || s.redisClient == nil {
		return func() {}
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(reportSchedulerLockRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, _ = reportSchedulerRenewScript.Run(rctx, s.redisClient,
					[]string{reportSchedulerLeaderLockKey}, s.instanceID,
					reportSchedulerLeaderLockTTL.Milliseconds()).Result()
				rcancel()
			}
		}
	}()
	return func() {
		close(done)
		wg.Wait()
	}
}

func (s *ReportSchedulerService) getLastRunAt(ctx context.Context, kind string) time.Time {
	if s == nil || s.redisClient == nil {
		return time.Time{}
	}
	raw, err := s.redisClient.Get(ctx, reportSchedulerLastRunKeyPrefix+kind).Result()
	if err != nil || raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (s *ReportSchedulerService) setLastRunAt(ctx context.Context, kind string, now time.Time) {
	if s == nil || s.redisClient == nil {
		return
	}
	// TTL 覆盖月报周期：服务/Redis 在触发时刻宕机恢复后 key 仍在，
	// catch-up 逻辑才能补上错过的那次生成（24h 会导致月报永久丢失）
	_ = s.redisClient.Set(ctx, reportSchedulerLastRunKeyPrefix+kind, now.Format(time.RFC3339), 35*24*time.Hour).Err()
}
