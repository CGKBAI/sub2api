package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/robfig/cron/v3"
)

const (
	reportSchedulerJobName = "user_reports"

	reportSchedulerLeaderLockKey = "reports:scheduler:leader"
	reportSchedulerLeaderLockTTL = 5 * time.Minute

	reportSchedulerLastRunKeyPrefix = "reports:scheduler:last_run:"

	reportSchedulerTickInterval = 1 * time.Minute
)

var reportSchedulerCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

var reportSchedulerReleaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
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

	ctx, cancel := context.WithTimeout(s.stopCtx, 30*time.Minute)
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

	now := timezone.Now()
	if s.loc != nil {
		now = now.In(s.loc)
	}

	type scheduleDef struct {
		kind     string
		spec     string
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

		generated, err := s.reportService.GenerateForAllUsers(ctx, d.reportType, now)
		if err != nil {
			fmt.Printf("[ReportScheduler] generate %s reports: generated=%d err=%v\n", d.kind, generated, err)
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
	// last_run 只需在锁 TTL 窗口内有效，24h 足够
	_ = s.redisClient.Set(ctx, reportSchedulerLastRunKeyPrefix+kind, now.Format(time.RFC3339), 24*time.Hour).Err()
}
