package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// 报告生成任务队列参数。
const (
	// reportTaskMaxAttempts 单个任务最多尝试次数（含首次）。耗尽后转 failed，
	// 等待管理端人工补跑，避免无限重试持续烧 LLM。
	reportTaskMaxAttempts = 6
	// reportTaskLeaseTTL 任务租约时长。进程崩溃后超过该时长即可被其他 worker 重新领取；
	// 必须大于单次生成的最长耗时（LLM 120s + 质量重写一次）。
	reportTaskLeaseTTL = 8 * time.Minute
	// reportTaskConcurrency 生成任务并发数（LLM 调用是 IO 等待，4 路足够且不会打爆上游）。
	reportTaskConcurrency = 4
	// reportTaskWorkerBudget 定时 worker 每轮最多占用时长；到点就停，剩余任务留在表里
	// 等下一分钟继续，因此不会再有「30 分钟预算耗尽导致尾部用户消失」。
	reportTaskWorkerBudget = 8 * time.Minute
	// reportTaskManualBudget 管理端手动「为所有活跃用户生成」的单次预算。
	reportTaskManualBudget = 10 * time.Minute
	// reportTaskRetryDelay 预算取消/正常退出时的立即重排间隔（避免占用租约又等 30 分钟）。
	reportTaskRetryDelay = time.Minute
)

// ReportTaskMaterializeResult 是「上账」结果。
type ReportTaskMaterializeResult struct {
	PeriodStart time.Time
	Candidates  int
	Created     int
	Existing    int
}

// ReportTaskDrainResult 是「领取并执行」的统计。
type ReportTaskDrainResult struct {
	Generated    int
	Skipped      int
	Failed       int
	ManualRepair int
	Rescheduled  int
	Processed    int
}

type reportTaskOutcome struct {
	kind string
	err  error
}

// MaterializeReportTasks 为某周期所有活跃用户「上账」：先写 pending 任务，再交给
// worker 执行。上账是纯插入、毫秒级，即使下一秒进程崩溃，名单也已经落库。
//
// skipGenerated=true（定时批次）：已有成功报告的用户只补一条 done 台账，不再重生成。
// skipGenerated=false（管理端手动「为所有活跃用户生成」）：保持原来的整体重生成语义。
func (s *ReportService) MaterializeReportTasks(ctx context.Context, reportType string, ref time.Time, trigger ReportTrigger, suppressPush bool, skipGenerated bool) (ReportTaskMaterializeResult, error) {
	result := ReportTaskMaterializeResult{}
	if s == nil || s.reportRepo == nil {
		return result, errors.New("report repository not initialized")
	}
	if s.retryRepo == nil {
		return result, errors.New("report retry repository not initialized")
	}
	cfg, err := s.GetReportConfig(ctx)
	if err != nil {
		// 与生成路径保持一致：配置读取失败时用默认配置推导窗口，不阻塞上账。
		logger.LegacyPrintf("service.report", "[ReportTask] load config for materialize %s: %v", reportType, err)
		cfg = defaultReportLLMConfig()
	}
	windows, err := resolveReportWindows(reportType, ref, cfg)
	if err != nil {
		return result, err
	}
	userIDs, err := s.reportRepo.ListActiveUserIDs(ctx, windows.Start, windows.End)
	if err != nil {
		return result, err
	}
	generated, err := s.reportRepo.ListGeneratedUserIDs(ctx, reportType, windows.ReportStart)
	if err != nil {
		return result, err
	}

	now := timezone.Now()
	autoPush := trigger == ReportTriggerScheduled
	result.PeriodStart = windows.ReportStart
	result.Candidates = len(userIDs)
	for _, userID := range userIDs {
		if _, ok := generated[userID]; ok && skipGenerated {
			// 已有成功报告：补一条 done 台账（不重复生成，也不记失败）。
			result.Existing++
			if _, err := s.retryRepo.InsertCompleted(ctx, &ReportRetryTask{
				UserID:       userID,
				ReportType:   reportType,
				PeriodStart:  windows.ReportStart,
				AutoPush:     autoPush,
				SuppressPush: suppressPush,
			}); err != nil {
				logger.LegacyPrintf("service.report", "[ReportTask] record completed %s user=%d: %v", reportType, userID, err)
			}
			continue
		}
		created, err := s.retryRepo.InsertPending(ctx, &ReportRetryTask{
			UserID:       userID,
			ReportType:   reportType,
			PeriodStart:  windows.ReportStart,
			AutoPush:     autoPush,
			SuppressPush: suppressPush,
			Status:       domain.ReportRetryStatusPending,
			NextRetryAt:  &now,
		})
		if err != nil {
			logger.LegacyPrintf("service.report", "[ReportTask] materialize %s user=%d: %v", reportType, userID, err)
			continue
		}
		if created {
			result.Created++
		} else {
			result.Existing++
		}
	}
	logger.LegacyPrintf("service.report",
		"[ReportTask] materialized %s period=%s candidates=%d created=%d existing=%d auto_push=%v suppress_push=%v",
		reportType, windows.ReportStart.Format(time.RFC3339), result.Candidates, result.Created, result.Existing, autoPush, suppressPush)
	return result, nil
}

// RunGenerationTasks 领取并执行到期任务：每轮领取 concurrency 条、并发执行、
// 全部完成后领取下一轮。到达 budget 或 ctx 取消就停，剩余任务保持 pending。
func (s *ReportService) RunGenerationTasks(ctx context.Context, budget time.Duration, concurrency int) (ReportTaskDrainResult, error) {
	result := ReportTaskDrainResult{}
	if s == nil || s.retryRepo == nil {
		return result, nil
	}
	if concurrency < 1 {
		concurrency = reportTaskConcurrency
	}
	if budget <= 0 {
		budget = reportTaskWorkerBudget
	}
	deadline := time.Now().Add(budget)
	for {
		if ctx.Err() != nil || time.Now().After(deadline) {
			break
		}
		tasks, err := s.retryRepo.ClaimGeneration(ctx, s.taskWorkerID, timezone.Now(), concurrency, reportTaskLeaseTTL)
		if err != nil {
			return result, err
		}
		if len(tasks) == 0 {
			break
		}
		outcomes := make([]reportTaskOutcome, len(tasks))
		var wg sync.WaitGroup
		for i := range tasks {
			wg.Add(1)
			go func(index int, task ReportRetryTask) {
				defer wg.Done()
				outcomes[index] = s.ProcessReportTask(ctx, task)
			}(i, tasks[i])
		}
		wg.Wait()
		for _, outcome := range outcomes {
			result.Processed++
			switch outcome.kind {
			case domain.ReportRetryStatusDone:
				result.Generated++
			case domain.ReportRetryStatusSkipped:
				result.Skipped++
			case domain.ReportRetryStatusFailed:
				result.Failed++
			case domain.ReportRetryStatusManualRepair:
				result.ManualRepair++
			default:
				result.Rescheduled++
			}
		}
	}
	return result, nil
}

// ProcessReportTask 执行单个生成任务并把结局写回任务表。
func (s *ReportService) ProcessReportTask(ctx context.Context, task ReportRetryTask) reportTaskOutcome {
	if s == nil || s.retryRepo == nil {
		return reportTaskOutcome{kind: domain.ReportRetryStatusPending}
	}
	trigger := ReportTriggerManual
	if task.AutoPush {
		trigger = ReportTriggerScheduled
	}
	report, err := s.generateReport(ctx, task.UserID, task.ReportType, task.PeriodStart, trigger, task.SuppressPush, false)
	now := timezone.Now()

	finish := func(status, outcome, lastErr string, next *time.Time, reportID int64, consume bool) reportTaskOutcome {
		if ferr := s.retryRepo.FinishGeneration(ctx, task.ID, status, outcome, truncateReportError(lastErr), next, reportID, consume); ferr != nil {
			logger.LegacyPrintf("service.report", "[ReportTask] finish task=%d status=%s: %v", task.ID, status, ferr)
			return reportTaskOutcome{kind: status, err: ferr}
		}
		if status == domain.ReportRetryStatusPending || status == domain.ReportRetryStatusFailed || status == domain.ReportRetryStatusManualRepair {
			logger.LegacyPrintf("service.report",
				"[ReportTask] task=%d user=%d %s outcome=%s attempts=%d err=%s",
				task.ID, task.UserID, status, outcome, task.Attempts+1, lastErr)
		}
		return reportTaskOutcome{kind: status, err: err}
	}

	switch {
	case err == nil && report == nil:
		return finish(domain.ReportRetryStatusDone, domain.ReportTaskOutcomeGenerated, "", nil, 0, false)
	case err == nil:
		return finish(domain.ReportRetryStatusDone, domain.ReportTaskOutcomeGenerated, "", nil, report.ID, false)
	case errors.Is(err, ErrReportGenerateUserNotFound):
		return finish(domain.ReportRetryStatusSkipped, domain.ReportTaskOutcomeNoUsage, "", nil, 0, false)
	case errors.Is(err, ErrReportSkippedLowUsage):
		return finish(domain.ReportRetryStatusSkipped, domain.ReportTaskOutcomeLowUsage, "", nil, 0, false)
	case errors.Is(err, domain.ErrReportSensitiveContent):
		return finish(domain.ReportRetryStatusManualRepair, domain.ReportTaskOutcomeSensitive, err.Error(), nil, 0, false)
	case errors.Is(err, domain.ErrReportInvalidType), errors.Is(err, domain.ErrReportInvalidPeriod):
		return finish(domain.ReportRetryStatusManualRepair, domain.ReportTaskOutcomePermanent, err.Error(), nil, 0, false)
	}

	// 预算到点 / 进程优雅退出：不计失败，立刻重排，避免占用租约又等 30 分钟。
	if ctx.Err() != nil {
		next := now.Add(reportTaskRetryDelay)
		return finish(domain.ReportRetryStatusPending, "", "task canceled", &next, task.ReportID, false)
	}
	if reportLLMErrorPermanent(err) {
		return finish(domain.ReportRetryStatusManualRepair, domain.ReportTaskOutcomePermanent, err.Error(), nil, task.ReportID, true)
	}
	if task.Attempts+1 >= reportTaskMaxAttempts {
		return finish(domain.ReportRetryStatusFailed, domain.ReportTaskOutcomeAttempts, err.Error(), nil, task.ReportID, true)
	}
	next := now.Add(reportRetryInterval)
	return finish(domain.ReportRetryStatusPending, "", err.Error(), &next, task.ReportID, true)
}

// ReportTaskSummaryFor 返回某周期（按 ref 推导）的生成进度。
func (s *ReportService) ReportTaskSummaryFor(ctx context.Context, reportType string, ref time.Time) (ReportTaskSummary, error) {
	summary := ReportTaskSummary{Type: reportType}
	if s == nil || s.reportRepo == nil || s.retryRepo == nil {
		return summary, errors.New("report task queue not initialized")
	}
	cfg, err := s.GetReportConfig(ctx)
	if err != nil {
		cfg = defaultReportLLMConfig()
	}
	windows, err := resolveReportWindows(reportType, ref, cfg)
	if err != nil {
		return summary, err
	}
	summary.Type = reportType
	summary.PeriodStart = windows.ReportStart
	result, err := s.retryRepo.SummarizeByPeriod(ctx, reportType, windows.ReportStart, timezone.Now())
	if err != nil {
		return summary, err
	}
	result.Type = reportType
	result.PeriodStart = windows.ReportStart
	items, err := s.retryRepo.ListByPeriod(ctx, reportType, windows.ReportStart, []string{
		domain.ReportRetryStatusPending,
		domain.ReportRetryStatusFailed,
		domain.ReportRetryStatusManualRepair,
	})
	if err != nil {
		return result, err
	}
	result.Items = items
	return result, nil
}

// ResetReportTasks 把该周期未完成的任务重新置为 pending（管理端「补跑未完成」）。
func (s *ReportService) ResetReportTasks(ctx context.Context, reportType string, ref time.Time, statuses []string) (int64, error) {
	if s == nil || s.reportRepo == nil || s.retryRepo == nil {
		return 0, errors.New("report task queue not initialized")
	}
	if len(statuses) == 0 {
		statuses = []string{domain.ReportRetryStatusFailed, domain.ReportRetryStatusManualRepair}
	}
	for _, status := range statuses {
		switch status {
		case domain.ReportRetryStatusFailed, domain.ReportRetryStatusManualRepair,
			domain.ReportRetryStatusPending, domain.ReportRetryStatusSkipped:
		default:
			return 0, errors.New("invalid report task status: " + status)
		}
	}
	cfg, err := s.GetReportConfig(ctx)
	if err != nil {
		cfg = defaultReportLLMConfig()
	}
	windows, err := resolveReportWindows(reportType, ref, cfg)
	if err != nil {
		return 0, err
	}
	reset, err := s.retryRepo.ResetByPeriod(ctx, reportType, windows.ReportStart, statuses)
	if err != nil {
		return 0, err
	}
	logger.LegacyPrintf("service.report",
		"[ReportTask] reset %s period=%s statuses=%s reset=%d",
		reportType, windows.ReportStart.Format(time.RFC3339), strings.Join(statuses, ","), reset)
	return reset, nil
}
