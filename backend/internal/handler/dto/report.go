package dto

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ReportTaskItem 生成任务名单中的一行（管理端「未完成」展开用）。
type ReportTaskItem struct {
	UserID      int64      `json:"user_id"`
	Username    string     `json:"username"`
	Status      string     `json:"status"`
	Outcome     string     `json:"outcome,omitempty"`
	Attempts    int        `json:"attempts"`
	LastError   string     `json:"last_error,omitempty"`
	NextRetryAt *time.Time `json:"next_retry_at,omitempty"`
}

// ReportTaskSummary 某周期报告生成进度（每个用户取最新一条任务）。
type ReportTaskSummary struct {
	Type         string           `json:"type"`
	PeriodStart  time.Time        `json:"period_start"`
	Total        int              `json:"total"`
	Done         int              `json:"done"`
	Pending      int              `json:"pending"`
	Running      int              `json:"running"`
	Skipped      int              `json:"skipped"`
	Failed       int              `json:"failed"`
	ManualRepair int              `json:"manual_repair"`
	Items        []ReportTaskItem `json:"items"`
}

// ReportTaskSummaryFromService 从服务层实体转换。
func ReportTaskSummaryFromService(s service.ReportTaskSummary) *ReportTaskSummary {
	out := &ReportTaskSummary{
		Type: s.Type, PeriodStart: s.PeriodStart, Total: s.Total, Done: s.Done,
		Pending: s.Pending, Running: s.Running, Skipped: s.Skipped,
		Failed: s.Failed, ManualRepair: s.ManualRepair,
		Items: make([]ReportTaskItem, 0, len(s.Items)),
	}
	for _, item := range s.Items {
		out.Items = append(out.Items, ReportTaskItem{
			UserID: item.UserID, Username: item.Username, Status: item.Status,
			Outcome: item.Outcome, Attempts: item.Attempts, LastError: item.LastError,
			NextRetryAt: item.NextRetryAt,
		})
	}
	return out
}

// Report 是日报/周报的对外 JSON 结构。
type Report struct {
	ID          int64               `json:"id"`
	UserID      int64               `json:"user_id"`
	Username    string              `json:"username"`
	Type        string              `json:"type"`
	PeriodStart time.Time           `json:"period_start"`
	PeriodEnd   time.Time           `json:"period_end"`
	Stats       service.ReportStats `json:"stats"`
	AISummary   string              `json:"ai_summary"`
	Status      string              `json:"status"`
	Error       string              `json:"error,omitempty"`
	// PushedAt 最近一次飞书推送成功时间；LastPushError 最近一次推送失败原因
	PushedAt      *time.Time `json:"pushed_at,omitempty"`
	LastPushError string     `json:"last_push_error,omitempty"`
	// QualityWarnings 生成后质量校验遗留问题（空串=通过；前端显示 ⚠️）
	QualityWarnings string `json:"quality_warnings,omitempty"`
	// PushDeliveries 报告×群组最近一次投递结果（多群推送）
	PushDeliveries []ReportPushDelivery `json:"push_deliveries,omitempty"`
	// 生成失败重试状态：pending=30 分钟重试中；manual_repair=需人工修复
	GenerationRetryStatus string     `json:"generation_retry_status,omitempty"`
	GenerationRetryAt     *time.Time `json:"generation_retry_at,omitempty"`
	GenerationRetryError  string     `json:"generation_retry_error,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

// ReportPushDelivery 是报告在某一个飞书群的投递结果。
type ReportPushDelivery struct {
	GroupID      int64      `json:"group_id"`
	GroupName    string     `json:"group_name"`
	Status       string     `json:"status"`
	Error        string     `json:"error,omitempty"`
	AttemptedAt  time.Time  `json:"attempted_at"`
	AttemptCount int        `json:"attempt_count"`
	RetryStatus  string     `json:"retry_status,omitempty"`
	NextRetryAt  *time.Time `json:"next_retry_at,omitempty"`
}

// ReportFromService 从服务层实体转换。
func ReportFromService(r *service.Report) *Report {
	if r == nil {
		return nil
	}
	return &Report{
		ID:                    r.ID,
		UserID:                r.UserID,
		Username:              r.Username,
		Type:                  r.Type,
		PeriodStart:           r.PeriodStart,
		PeriodEnd:             r.PeriodEnd,
		Stats:                 r.Stats,
		AISummary:             r.AISummary,
		Status:                r.Status,
		Error:                 r.Error,
		PushedAt:              r.PushedAt,
		LastPushError:         r.LastPushError,
		QualityWarnings:       r.QualityWarnings,
		PushDeliveries:        reportDeliveriesFromService(r.PushDeliveries),
		GenerationRetryStatus: r.GenerationRetryStatus,
		GenerationRetryAt:     r.GenerationRetryAt,
		GenerationRetryError:  r.GenerationRetryError,
		CreatedAt:             r.CreatedAt,
		UpdatedAt:             r.UpdatedAt,
	}
}

func reportDeliveriesFromService(items []service.ReportPushDelivery) []ReportPushDelivery {
	if len(items) == 0 {
		return nil
	}
	out := make([]ReportPushDelivery, 0, len(items))
	for _, d := range items {
		out = append(out, ReportPushDelivery{
			GroupID:      d.GroupID,
			GroupName:    d.GroupName,
			Status:       d.Status,
			Error:        d.Error,
			AttemptedAt:  d.AttemptedAt,
			AttemptCount: d.AttemptCount,
			RetryStatus:  d.RetryStatus,
			NextRetryAt:  d.NextRetryAt,
		})
	}
	return out
}
