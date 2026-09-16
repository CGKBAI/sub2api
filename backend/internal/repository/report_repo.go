package repository

import (
	"context"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/report"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type reportRepository struct {
	client *dbent.Client
}

// NewReportRepository 构造报告仓储。
func NewReportRepository(client *dbent.Client) service.ReportRepository {
	return &reportRepository{client: client}
}

func (r *reportRepository) Create(ctx context.Context, rp *service.Report) error {
	client := clientFromContext(ctx, r.client)
	created, err := client.Report.Create().
		SetUserID(rp.UserID).
		SetType(rp.Type).
		SetPeriodStart(rp.PeriodStart).
		SetPeriodEnd(rp.PeriodEnd).
		SetStats(rp.Stats).
		SetAiSummary(rp.AISummary).
		SetStatus(rp.Status).
		SetError(rp.Error).
		Save(ctx)
	if err != nil {
		return err
	}
	applyReportEntityToService(rp, created)
	return nil
}

func (r *reportRepository) Update(ctx context.Context, rp *service.Report) error {
	client := clientFromContext(ctx, r.client)
	updated, err := client.Report.UpdateOneID(rp.ID).
		SetUserID(rp.UserID).
		SetType(rp.Type).
		SetPeriodStart(rp.PeriodStart).
		SetPeriodEnd(rp.PeriodEnd).
		SetStats(rp.Stats).
		SetAiSummary(rp.AISummary).
		SetStatus(rp.Status).
		SetError(rp.Error).
		Save(ctx)
	if err != nil {
		return translatePersistenceError(err, domain.ErrReportNotFound, nil)
	}
	applyReportEntityToService(rp, updated)
	return nil
}

func (r *reportRepository) GetByID(ctx context.Context, id int64) (*service.Report, error) {
	m, err := r.client.Report.Query().
		Where(report.IDEQ(id)).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, domain.ErrReportNotFound, nil)
	}
	out := reportEntityToService(m)
	r.attachUsernames(ctx, []*service.Report{out})
	return out, nil
}

func (r *reportRepository) GetByUserPeriod(ctx context.Context, userID int64, reportType string, periodStart time.Time) (*service.Report, error) {
	m, err := r.client.Report.Query().
		Where(
			report.UserIDEQ(userID),
			report.TypeEQ(reportType),
			report.PeriodStartEQ(periodStart),
		).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return reportEntityToService(m), nil
}

func (r *reportRepository) List(
	ctx context.Context,
	params pagination.PaginationParams,
	filters service.ReportListFilters,
) ([]service.Report, *pagination.PaginationResult, error) {
	q := r.client.Report.Query()

	if filters.Type != "" {
		q = q.Where(report.TypeEQ(filters.Type))
	}
	if filters.UserID > 0 {
		q = q.Where(report.UserIDEQ(filters.UserID))
	}
	// 日期筛选用「周期覆盖」语义：period 与 [StartDate, EndDate) 有交集即命中。
	// 周报（period_start=周一）/月报（period_start=上月 1 日）在周中/月末查看时，
	// 按 period_start 落入所选日过滤会永远查不到；覆盖语义对日/周/月三种类型统一正确。
	if !filters.StartDate.IsZero() {
		q = q.Where(report.PeriodEndGT(filters.StartDate))
	}
	if !filters.EndDate.IsZero() {
		q = q.Where(report.PeriodStartLT(filters.EndDate))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	items, err := q.
		Order(dbent.Desc(report.FieldPeriodStart), dbent.Desc(report.FieldID)).
		Offset(params.Offset()).
		Limit(params.Limit()).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	out := make([]service.Report, 0, len(items))
	ptrs := make([]*service.Report, 0, len(items))
	for _, m := range items {
		s := reportEntityToService(m)
		if s != nil {
			out = append(out, *s)
			ptrs = append(ptrs, &out[len(out)-1])
		}
	}
	r.attachUsernames(ctx, ptrs)
	return out, paginationResultFromTotal(int64(total), params), nil
}

// AggregateUserUsage 聚合 usage_logs：请求/token/费用/模型分布/活跃时段。
// 时区：小时分布按服务器配置时区（AT TIME ZONE $tz）计算。
func (r *reportRepository) AggregateUserUsage(ctx context.Context, userID int64, start, end time.Time) (domain.ReportStats, error) {
	stats := domain.ReportStats{
		Models:      map[string]int64{},
		ModelTokens: map[string]int64{},
		Hourly:      map[int]int64{},
	}
	client := clientFromContext(ctx, r.client)

	// 总量 + 模型分布（一次查询）
	rows, err := client.QueryContext(ctx, `
		SELECT
			COALESCE(model, '') AS model,
			COUNT(*) AS requests,
			COALESCE(SUM(input_tokens), 0) AS input_tokens,
			COALESCE(SUM(output_tokens), 0) AS output_tokens,
			COALESCE(SUM(total_cost), 0) AS total_cost
		FROM usage_logs
		WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
		GROUP BY COALESCE(model, '')
	`, userID, start, end)
	if err != nil {
		return stats, err
	}
	defer rows.Close()

	for rows.Next() {
		var model string
		var requests, inputTokens, outputTokens int64
		var totalCost float64
		if err := rows.Scan(&model, &requests, &inputTokens, &outputTokens, &totalCost); err != nil {
			return stats, err
		}
		stats.Requests += requests
		stats.InputTokens += inputTokens
		stats.OutputTokens += outputTokens
		stats.TotalCost += totalCost
		if model == "" {
			model = "unknown"
		}
		stats.Models[model] += requests
		stats.ModelTokens[model] += inputTokens + outputTokens
	}
	if err := rows.Err(); err != nil {
		return stats, err
	}

	// 活跃时段（按服务器时区的小时直方图）
	tzName := reportTimezoneName()
	hourRows, err := client.QueryContext(ctx, `
		SELECT EXTRACT(hour FROM (created_at AT TIME ZONE $4))::int AS hour, COUNT(*) AS requests
		FROM usage_logs
		WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
		GROUP BY 1
	`, userID, start, end, tzName)
	if err != nil {
		// 小时分布失败不阻塞报告生成
		return stats, nil
	}
	defer hourRows.Close()
	for hourRows.Next() {
		var hour, requests int
		if err := hourRows.Scan(&hour, &requests); err == nil {
			stats.Hourly[hour] = int64(requests)
		}
	}
	_ = hourRows.Err()

	return stats, nil
}

// FetchUserPrompts 拉取 prompt_audit_events 中的 prompt 片段（全天均匀抽样，按时间正序）。
// 单纯取最新 N 条会只覆盖傍晚的请求、丢掉早晨的工作；这里按 ROW_NUMBER 取模分桶，
// 覆盖整个统计周期。总数不超过 limit 时全量返回。
func (r *reportRepository) FetchUserPrompts(ctx context.Context, userID int64, start, end time.Time, limit int) ([]service.UserPromptSnippet, error) {
	if limit <= 0 {
		limit = 30
	}
	client := clientFromContext(ctx, r.client)

	rows, err := client.QueryContext(ctx, `
		WITH windowed AS (
		    SELECT COALESCE(model, '') AS model,
		           created_at,
		           COALESCE(NULLIF(full_prompt, ''), NULLIF(redacted_preview, ''), '') AS content,
		           ROW_NUMBER() OVER (ORDER BY created_at) AS rn,
		           COUNT(*) OVER () AS total
		    FROM prompt_audit_events
		    WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
		)
		SELECT model, created_at, content
		FROM windowed
		WHERE total <= $4 OR (rn - 1) % ((total + $4 - 1) / $4) = 0
		ORDER BY created_at
		LIMIT $4
	`, userID, start, end, limit)
	if err != nil {
		// 审计表不存在（未开启审计）等情况：返回空，报告退化为纯统计
		return nil, nil
	}
	defer rows.Close()

	out := make([]service.UserPromptSnippet, 0, limit)
	for rows.Next() {
		var snip service.UserPromptSnippet
		if err := rows.Scan(&snip.Model, &snip.CreatedAt, &snip.Content); err != nil {
			return out, err
		}
		out = append(out, snip)
	}
	return out, rows.Err()
}

// ListActiveUserIDs 列出时间窗内有用量的用户。
func (r *reportRepository) ListActiveUserIDs(ctx context.Context, start, end time.Time) ([]int64, error) {
	client := clientFromContext(ctx, r.client)

	rows, err := client.QueryContext(ctx, `
		SELECT DISTINCT user_id FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
	`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]int64, 0, 16)
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return out, err
		}
		out = append(out, uid)
	}
	return out, rows.Err()
}

// attachUsernames 批量补齐 username（users 表 LEFT JOIN 等价实现）。
func (r *reportRepository) attachUsernames(ctx context.Context, items []*service.Report) {
	if len(items) == 0 {
		return
	}
	ids := make([]int64, 0, len(items))
	seen := map[int64]struct{}{}
	for _, it := range items {
		if it == nil || it.UserID <= 0 {
			continue
		}
		if _, ok := seen[it.UserID]; ok {
			continue
		}
		seen[it.UserID] = struct{}{}
		ids = append(ids, it.UserID)
	}
	if len(ids) == 0 {
		return
	}

	users, err := r.client.User.Query().
		Where(user.IDIn(ids...)).
		All(ctx)
	if err != nil {
		return
	}
	names := make(map[int64]string, len(users))
	for _, u := range users {
		names[u.ID] = u.Username
	}
	for _, it := range items {
		if it != nil {
			it.Username = names[it.UserID]
		}
	}
}

func applyReportEntityToService(dst *service.Report, src *dbent.Report) {
	if dst == nil || src == nil {
		return
	}
	dst.ID = src.ID
	dst.UserID = src.UserID
	dst.Type = src.Type
	dst.PeriodStart = src.PeriodStart
	dst.PeriodEnd = src.PeriodEnd
	dst.Stats = src.Stats
	dst.AISummary = src.AiSummary
	dst.Status = src.Status
	dst.Error = src.Error
	dst.CreatedAt = src.CreatedAt
	dst.UpdatedAt = src.UpdatedAt
}

func reportEntityToService(m *dbent.Report) *service.Report {
	if m == nil {
		return nil
	}
	return &service.Report{
		ID:          m.ID,
		UserID:      m.UserID,
		Type:        m.Type,
		PeriodStart: m.PeriodStart,
		PeriodEnd:   m.PeriodEnd,
		Stats:       m.Stats,
		AISummary:   m.AiSummary,
		Status:      m.Status,
		Error:       m.Error,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
}

func reportTimezoneName() string {
	if name := timezone.Name(); name != "" && name != "Local" {
		return name
	}
	return "UTC"
}
