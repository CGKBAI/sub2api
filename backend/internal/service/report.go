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
	// ListGeneratedUserIDs 列出该周期已有成功报告（status=done）的用户 ID，
	// 用于批量生成「上账」时跳过已完成的人，避免重复烧 LLM。
	ListGeneratedUserIDs(ctx context.Context, reportType string, periodStart time.Time) (map[int64]struct{}, error)
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

type ReportRetryTask = domain.ReportRetryTask

// ReportRetryRepository 是报告生成/推送任务队列的持久化接口。
//
// generation 任务既是「首次生成」也是「失败重试」：Materialize 写入 pending，
// worker 通过 ClaimGeneration 带租约领取，FinishGeneration 写结局。
type ReportRetryRepository interface {
	// UpsertGeneration 写入/更新一个生成重试任务（同一用户周期唯一，attempts 自增）。
	UpsertGeneration(ctx context.Context, t *ReportRetryTask) (*ReportRetryTask, error)
	// UpsertPush 写入/更新一个推送重试任务（同一报告×群唯一，attempts 自增）。
	UpsertPush(ctx context.Context, t *ReportRetryTask) (*ReportRetryTask, error)
	// InsertPending 为「首次生成」上账：插入 pending，attempts=0。
	// 同一用户周期已有未结束任务时不再插入（created=false）。
	InsertPending(ctx context.Context, t *ReportRetryTask) (created bool, err error)
	// InsertCompleted 为「已有成功报告」的用户补一条 done 台账（outcome=already_done），
	// 让「应生成 N 人」把已完成的用户也算进来；同周期已有任何任务时不重复插入。
	InsertCompleted(ctx context.Context, t *ReportRetryTask) (created bool, err error)
	// ClaimGeneration 领取到期的 pending 生成任务，写入租约并返回。
	// 过期租约（进程崩溃）会被重新领取；并发实例靠 FOR UPDATE SKIP LOCKED 互斥。
	ClaimGeneration(ctx context.Context, workerID string, now time.Time, limit int, leaseTTL time.Duration) ([]ReportRetryTask, error)
	// FinishGeneration 写入任务结局并释放租约。nextRetryAt 为空表示不再重试。
	// consumeAttempt=true 时 attempts+1（真实失败计一次），预算取消等场景传 false。
	FinishGeneration(ctx context.Context, id int64, status, outcome, lastError string, nextRetryAt *time.Time, reportID int64, consumeAttempt bool) error
	// SummarizeByPeriod 汇总某周期内「每个用户最新一条任务」的状态分布。
	SummarizeByPeriod(ctx context.Context, reportType string, periodStart time.Time, now time.Time) (ReportTaskSummary, error)
	// ListByPeriod 列出某周期内最新任务处于指定状态的用户（管理端展开未完成名单）。
	ListByPeriod(ctx context.Context, reportType string, periodStart time.Time, statuses []string) ([]ReportTaskItem, error)
	// ResetByPeriod 把该周期内最新任务处于指定状态的用户重新置为 pending（人工补跑）。
	ResetByPeriod(ctx context.Context, reportType string, periodStart time.Time, statuses []string) (int64, error)
	// MarkDone 结束任务；MarkDoneByGeneration/MarkDoneByPush 按业务键结束，
	// 用于成功路径（不依赖是否读到任务 ID）。
	MarkDone(ctx context.Context, id int64) error
	MarkDoneByGeneration(ctx context.Context, userID int64, reportType string, periodStart time.Time) error
	MarkDoneByPush(ctx context.Context, reportID, groupID int64) error
	// ListDue 返回到期且 status=pending 的任务（推送重试使用）。
	ListDue(ctx context.Context, kind string, now time.Time, limit int) ([]ReportRetryTask, error)
	// ListActiveByReportIDs 批量查询报告当前未结束的任务（列表展示用）。
	ListActiveByReportIDs(ctx context.Context, reportIDs []int64) (map[int64][]ReportRetryTask, error)
	// GetByID 读取单个任务（重试调度用）。
	GetByID(ctx context.Context, id int64) (*ReportRetryTask, error)
}

// ReportTaskSummary 某个周期内「每个用户最新一条生成任务」的状态分布。
type ReportTaskSummary struct {
	Type         string
	PeriodStart  time.Time
	Total        int
	Done         int
	Pending      int
	Running      int
	Skipped      int
	Failed       int
	ManualRepair int
	Items        []ReportTaskItem
}

// ReportTaskItem 管理端「未完成名单」中的一行。
type ReportTaskItem struct {
	UserID      int64
	Username    string
	Status      string
	Outcome     string
	Attempts    int
	LastError   string
	NextRetryAt *time.Time
}

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
