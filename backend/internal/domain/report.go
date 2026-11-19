package domain

import (
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// 日报/周报/月报类型与状态常量。
const (
	ReportTypeDaily   = "daily"
	ReportTypeWeekly  = "weekly"
	ReportTypeMonthly = "monthly"

	ReportStatusPending = "pending"
	ReportStatusDone    = "done"
	ReportStatusFailed  = "failed"
)

var (
	ErrReportNotFound         = infraerrors.NotFound("REPORT_NOT_FOUND", "report not found")
	ErrReportInvalidType      = infraerrors.BadRequest("REPORT_TYPE_INVALID", "report type must be daily, weekly or monthly")
	ErrReportInvalidPeriod    = infraerrors.BadRequest("REPORT_PERIOD_INVALID", "report period is invalid")
	ErrReportLLMNotConfigured = infraerrors.BadRequest("REPORT_LLM_NOT_CONFIGURED", "report LLM is not configured")
	ErrReportLLMCallFailed    = infraerrors.BadRequest("REPORT_LLM_CALL_FAILED", "report LLM call failed")

	ErrReportFeishuNotConfigured = infraerrors.BadRequest("REPORT_FEISHU_NOT_CONFIGURED", "feishu push is not configured")
	ErrReportFeishuPushFailed    = infraerrors.BadRequest("REPORT_FEISHU_PUSH_FAILED", "feishu push failed")
	ErrReportFeishuNoGroups      = infraerrors.BadRequest("REPORT_FEISHU_NO_GROUPS", "user is not in any enabled report feishu group")
	ErrReportFeishuGroupNotFound = infraerrors.NotFound("REPORT_FEISHU_GROUP_NOT_FOUND", "report feishu group not found")
	ErrReportFeishuGroupNameDup  = infraerrors.BadRequest("REPORT_FEISHU_GROUP_NAME_DUPLICATED", "report feishu group name already exists")
	ErrReportFeishuGroupInvalid  = infraerrors.BadRequest("REPORT_FEISHU_GROUP_INVALID", "report feishu group name and webhook are required")
)

// 报告飞书投递状态。
const (
	ReportPushStatusSuccess = "success"
	ReportPushStatusFailed  = "failed"
)

// ReportFeishuGroup 是一个报告推送群组（对应一个飞书群自定义机器人 webhook）。
type ReportFeishuGroup struct {
	ID          int64
	Name        string
	WebhookURL  string
	Secret      string
	Enabled     bool
	MemberCount int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ReportPushDelivery 是报告×群组最近一次投递结果。
type ReportPushDelivery struct {
	ReportID     int64
	GroupID      int64
	GroupName    string
	Status       string
	Error        string
	AttemptedAt  time.Time
	AttemptCount int
}

// ReportStats 是 reports.stats 列的 JSON 结构：一个周期内的聚合统计。
type ReportStats struct {
	// 请求次数
	Requests int64 `json:"requests"`
	// token 用量
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	// 费用（按 total_cost 汇总）
	TotalCost float64 `json:"total_cost"`
	// 模型分布：模型名 -> 请求次数
	Models map[string]int64 `json:"models"`
	// 模型 token 分布：模型名 -> input+output tokens
	ModelTokens map[string]int64 `json:"model_tokens"`
	// 活跃时段：小时(0-23, 服务器时区) -> 请求次数
	Hourly map[int]int64 `json:"hourly"`
	// 参与总结的 prompt 片段数量
	PromptCount int64 `json:"prompt_count"`
}

// Report 是单人单周期的报告实体。
type Report struct {
	ID          int64
	UserID      int64
	Username    string
	Type        string
	PeriodStart time.Time
	PeriodEnd   time.Time
	Stats       ReportStats
	AISummary   string
	Status      string
	Error       string
	// PushedAt 最近一次飞书推送成功时间（nil = 从未推成功）；LastPushError 最近一次推送失败原因
	PushedAt      *time.Time
	LastPushError string
	// PushDeliveries 报告×群组最近一次投递结果（列表/详情展示用，由服务层批量补齐）
	PushDeliveries []ReportPushDelivery
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// IsWeekly 报告是否为周报。
func (r *Report) IsWeekly() bool {
	return r != nil && r.Type == ReportTypeWeekly
}

// IsMonthly 报告是否为月报。
func (r *Report) IsMonthly() bool {
	return r != nil && r.Type == ReportTypeMonthly
}
