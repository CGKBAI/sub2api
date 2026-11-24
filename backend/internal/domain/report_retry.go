package domain

import "time"

// 报告重试任务类型与状态。
const (
	ReportRetryKindGeneration = "generation"
	ReportRetryKindPush       = "push"

	ReportRetryStatusPending      = "pending"
	ReportRetryStatusManualRepair = "manual_repair"
	ReportRetryStatusDone         = "done"
)

// ReportRetryTask 是报告生成/推送失败后的重试任务。
//
// generation 任务按 (user_id, report_type, period_start) 唯一；
// push 任务按 (report_id, group_id) 唯一。
type ReportRetryTask struct {
	ID          int64
	Kind        string
	ReportID    int64
	UserID      int64
	ReportType  string
	PeriodStart time.Time
	GroupID     int64
	AutoPush    bool
	Status      string
	Attempts    int
	LastError   string
	NextRetryAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
