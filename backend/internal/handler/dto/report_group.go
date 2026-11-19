package dto

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// RedactedSecret 是接口返回敏感字段时的占位符（与 api_key 脱敏语义一致：
// 前端原样回传表示保留旧值）。
const RedactedSecret = "********"

// ReportFeishuGroup 是报告飞书群组的对外 JSON 结构。
type ReportFeishuGroup struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	WebhookURL  string    `json:"webhook_url"`
	Secret      string    `json:"secret,omitempty"`
	Enabled     bool      `json:"enabled"`
	MemberCount int       `json:"member_count"`
	UserIDs     []int64   `json:"user_ids,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ReportFeishuGroupFromService 从服务层实体转换（original=false 时对
// webhook/secret 脱敏，用于列表/详情回显；留空表示不修改）。
func ReportFeishuGroupFromService(g *service.ReportFeishuGroup, redact bool, userIDs []int64) *ReportFeishuGroup {
	if g == nil {
		return nil
	}
	webhook := g.WebhookURL
	secret := g.Secret
	if redact {
		if webhook != "" {
			webhook = RedactedSecret
		}
		if secret != "" {
			secret = RedactedSecret
		}
	}
	return &ReportFeishuGroup{
		ID:          g.ID,
		Name:        g.Name,
		WebhookURL:  webhook,
		Secret:      secret,
		Enabled:     g.Enabled,
		MemberCount: g.MemberCount,
		UserIDs:     userIDs,
		CreatedAt:   g.CreatedAt,
		UpdatedAt:   g.UpdatedAt,
	}
}
