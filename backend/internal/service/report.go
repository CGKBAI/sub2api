package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

const (
	ReportTypeDaily   = domain.ReportTypeDaily
	ReportTypeWeekly  = domain.ReportTypeWeekly
	ReportTypeMonthly = domain.ReportTypeMonthly

	ReportStatusPending = domain.ReportStatusPending
	ReportStatusDone    = domain.ReportStatusDone
	ReportStatusFailed  = domain.ReportStatusFailed
)

var (
	ErrReportNotFound         = domain.ErrReportNotFound
	ErrReportInvalidType      = domain.ErrReportInvalidType
	ErrReportInvalidPeriod    = domain.ErrReportInvalidPeriod
	ErrReportLLMNotConfigured = domain.ErrReportLLMNotConfigured
	ErrReportLLMCallFailed    = domain.ErrReportLLMCallFailed

	ErrReportFeishuNotConfigured = domain.ErrReportFeishuNotConfigured
	ErrReportFeishuPushFailed    = domain.ErrReportFeishuPushFailed
)

type Report = domain.Report

type ReportStats = domain.ReportStats

// ReportListFilters 报告列表过滤条件。
type ReportListFilters struct {
	Type      string
	UserID    int64 // 0 = 全部
	StartDate time.Time
	EndDate   time.Time
}

// ReportRepository 是报告实体的持久化接口。
type ReportRepository interface {
	Create(ctx context.Context, r *Report) error
	Update(ctx context.Context, r *Report) error
	GetByID(ctx context.Context, id int64) (*Report, error)
	GetByUserPeriod(ctx context.Context, userID int64, reportType string, periodStart time.Time) (*Report, error)
	List(ctx context.Context, params pagination.PaginationParams, filters ReportListFilters) ([]Report, *pagination.PaginationResult, error)

	// AggregateUserUsage 聚合单个用户在时间窗内的用量统计（usage_logs）。
	AggregateUserUsage(ctx context.Context, userID int64, start, end time.Time) (ReportStats, error)
	// FetchUserTurns 拉取用户在时间窗内的逐次请求快照（最新 limit 条），按时间正序。
	// Content 优先为写入时抽好的 user_text（CleanText=true，已剥掉注入块）；
	// 历史行回退为完整拍平 prompt 的头部截断（CleanText=false，需正则提取）。
	FetchUserTurns(ctx context.Context, userID int64, start, end time.Time, limit int) ([]UserPromptSnippet, error)
	// FetchPromptSnapshots 拉取时间窗内用于对话区窗口采样的会话快照，按时间正序。
	// session 感知：session_id 非空的行每 session 取两条（最新请求 + 会话中点前最近
	// 一条——后者覆盖最新快照被 64k 截断丢掉的中段），session 超过 count 个取最近
	// count 个；空 session_id 行保留均匀分布兜底。
	FetchPromptSnapshots(ctx context.Context, userID int64, start, end time.Time, count int) ([]UserPromptSnippet, error)
	// ListActiveUserIDs 列出时间窗内有用量的用户 ID。
	ListActiveUserIDs(ctx context.Context, start, end time.Time) ([]int64, error)
	// GetUsername 查询用户显示名（users.username，用于报告标题）。
	GetUsername(ctx context.Context, userID int64) (string, error)
	// GetReportGoal 查询用户自行填写的日报近期目标（users.report_goal）。
	// 空串表示未设置，日报按无目标生成；周报/月报不读取（目标经日报摘要继承）。
	GetReportGoal(ctx context.Context, userID int64) (string, error)
	// IsReportPushEnabled 查询用户是否参与报告飞书自动推送（users.report_push_enabled）。
	IsReportPushEnabled(ctx context.Context, userID int64) (bool, error)
	// MarkPushResult 记录报告最近一次飞书推送结果：
	// pushErr 为空表示成功（写 pushed_at 并清空 last_push_error），否则仅记录失败原因。
	MarkPushResult(ctx context.Context, id int64, pushedAt time.Time, pushErr string) error
}

// ReportGroupRepository 是报告飞书群组/成员/投递结果的持久化接口。
type ReportGroupRepository interface {
	ListGroups(ctx context.Context) ([]ReportFeishuGroup, error)
	GetGroup(ctx context.Context, id int64) (*ReportFeishuGroup, error)
	CreateGroup(ctx context.Context, g *ReportFeishuGroup) error
	UpdateGroup(ctx context.Context, g *ReportFeishuGroup) error
	DeleteGroup(ctx context.Context, id int64) error
	// ListGroupMemberIDs 返回群组成员用户 ID（UI 编辑成员用）。
	ListGroupMemberIDs(ctx context.Context, groupID int64) ([]int64, error)
	// ReplaceGroupMembers 全量替换群组成员。
	ReplaceGroupMembers(ctx context.Context, groupID int64, userIDs []int64) error
	// ListEnabledGroupsForUser 返回用户所属且启用的群组（推送路由用）。
	ListEnabledGroupsForUser(ctx context.Context, userID int64) ([]ReportFeishuGroup, error)
	// UpsertDelivery 写入/更新报告×群组的最近一次投递结果。
	UpsertDelivery(ctx context.Context, d *ReportPushDelivery) error
	// ListDeliveriesByReportIDs 批量查询多份报告的投递结果（列表页避免 N+1）。
	ListDeliveriesByReportIDs(ctx context.Context, reportIDs []int64) (map[int64][]ReportPushDelivery, error)
}

type ReportFeishuGroup = domain.ReportFeishuGroup

type ReportPushDelivery = domain.ReportPushDelivery

// UserPromptSnippet 是用于 LLM 总结的单条 prompt 片段。
type UserPromptSnippet struct {
	Model     string
	CreatedAt time.Time
	Content   string
	// CleanText 为 true 表示 Content 已是写入时抽好的用户输入（user_text 列），
	// 直接使用即可；否则 Content 是 full_prompt 的头部截断，需要正则提取。
	CleanText bool
}

// ReportGenerateResult 手动/定时生成的结果。
type ReportGenerateResult struct {
	Report *Report
	Err    error
}

// ReportTrigger 标记报告生成来源：定时生成可按开关自动推飞书；
// 手动生成永不自动推，只在 web 展示，由卡片按钮手动推送。
type ReportTrigger string

const (
	ReportTriggerManual    ReportTrigger = "manual"    // 管理端/用户手动生成
	ReportTriggerScheduled ReportTrigger = "scheduled" // 定时任务生成
)

var (
	ErrReportGenerateUserNotFound = infraerrors.BadRequest("REPORT_USER_NOT_FOUND", "no usage found for this user in the period")
	// 节假日感知关闭时的日报请求阈值过滤（GenerateForAllUsers 循环静默跳过）
	ErrReportSkippedLowUsage = infraerrors.BadRequest("REPORT_BELOW_MIN_REQUESTS", "daily report skipped: requests below threshold")
)
