package domain

import "time"

// 报告重试任务类型与状态。
//
// generation 任务现在同时承担「首次生成」和「失败重试」：定时/手动批量生成
// 先为每个用户每周期写一条 pending，再由 worker 领取执行。这样进程重启、
// 预算耗尽、某个用户卡住都不会让「还没轮到的人」凭空消失。
const (
	ReportRetryKindGeneration = "generation"
	ReportRetryKindPush       = "push"

	// pending 待生成（含首次尝试与 30 分钟重试）。
	ReportRetryStatusPending = "pending"
	// manual_repair 永久错误，停止自动重试，等待人工修复。
	ReportRetryStatusManualRepair = "manual_repair"
	// done 已生成。
	ReportRetryStatusDone = "done"
	// skipped 设计性跳过（无用量/低于阈值/已有成功报告），不计失败。
	ReportRetryStatusSkipped = "skipped"
	// failed 重试次数耗尽，等待人工介入。
	ReportRetryStatusFailed = "failed"
)

// 生成任务结局原因（report_retry_tasks.outcome）。
const (
	ReportTaskOutcomeGenerated   = "generated"
	ReportTaskOutcomeAlreadyDone = "already_done"
	ReportTaskOutcomeLowUsage    = "low_usage"
	ReportTaskOutcomeNoUsage     = "no_usage"
	ReportTaskOutcomeSensitive   = "sensitive_content"
	ReportTaskOutcomePermanent   = "permanent_error"
	ReportTaskOutcomeAttempts    = "attempts_exhausted"
)

// ReportRetryTask 是报告生成/推送任务。
//
// generation 任务按 (user_id, report_type, period_start) 唯一；
// push 任务按 (report_id, group_id) 唯一。
//
// 状态 running 不落库：status=pending 且 LeaseUntil > now() 即视为正在生成，
// 这样既能看到「谁在跑」，又不必修改唯一索引谓词（旧镜像兼容）。
type ReportRetryTask struct {
	ID            int64
	Kind          string
	ReportID      int64
	UserID        int64
	ReportType    string
	PeriodStart   time.Time
	GroupID       int64
	AutoPush      bool
	SuppressPush  bool
	Status        string
	Outcome       string
	Attempts      int
	LastError     string
	NextRetryAt   *time.Time
	LeaseOwner    string
	LeaseUntil    *time.Time
	LastAttemptAt *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Running 判断任务是否正被某个未过期的租约持有。
func (t ReportRetryTask) Running(now time.Time) bool {
	return t.Status == ReportRetryStatusPending && t.LeaseUntil != nil && t.LeaseUntil.After(now)
}
