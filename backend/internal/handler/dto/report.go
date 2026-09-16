package dto

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

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
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

// ReportFromService 从服务层实体转换。
func ReportFromService(r *service.Report) *Report {
	if r == nil {
		return nil
	}
	return &Report{
		ID:          r.ID,
		UserID:      r.UserID,
		Username:    r.Username,
		Type:        r.Type,
		PeriodStart: r.PeriodStart,
		PeriodEnd:   r.PeriodEnd,
		Stats:       r.Stats,
		AISummary:   r.AISummary,
		Status:      r.Status,
		Error:       r.Error,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}
