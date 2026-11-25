package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// reportRetryRepository 报告生成/推送失败重试队列仓储。
// 任务表结构简单、以队列扫描为主，直接用原生 SQL，避免为队列表引入 ent schema。
type reportRetryRepository struct {
	client *dbent.Client
}

// NewReportRetryRepository 构造报告重试队列仓储。
func NewReportRetryRepository(client *dbent.Client) service.ReportRetryRepository {
	return &reportRetryRepository{client: client}
}

const reportRetryTaskColumns = `
	id, kind,
	COALESCE(report_id, 0), COALESCE(user_id, 0), COALESCE(report_type, ''),
	COALESCE(period_start, to_timestamp(0)), COALESCE(group_id, 0),
	auto_push, status, attempts, last_error, next_retry_at,
	created_at, updated_at,
	lease_owner, lease_until, outcome, suppress_push, last_attempt_at
`

func scanReportRetryTask(row interface{ Scan(...any) error }) (*service.ReportRetryTask, error) {
	var t service.ReportRetryTask
	var nextRetry, leaseUntil, lastAttempt sql.NullTime
	if err := row.Scan(
		&t.ID, &t.Kind,
		&t.ReportID, &t.UserID, &t.ReportType,
		&t.PeriodStart, &t.GroupID,
		&t.AutoPush, &t.Status, &t.Attempts, &t.LastError, &nextRetry,
		&t.CreatedAt, &t.UpdatedAt,
		&t.LeaseOwner, &leaseUntil, &t.Outcome, &t.SuppressPush, &lastAttempt,
	); err != nil {
		return nil, err
	}
	if nextRetry.Valid {
		v := nextRetry.Time
		t.NextRetryAt = &v
	}
	if leaseUntil.Valid {
		v := leaseUntil.Time
		t.LeaseUntil = &v
	}
	if lastAttempt.Valid {
		v := lastAttempt.Time
		t.LastAttemptAt = &v
	}
	return &t, nil
}

func (r *reportRetryRepository) UpsertGeneration(ctx context.Context, t *service.ReportRetryTask) (*service.ReportRetryTask, error) {
	if t == nil || t.UserID <= 0 || t.ReportType == "" || t.PeriodStart.IsZero() {
		return nil, errors.New("invalid generation retry task")
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
		INSERT INTO report_retry_tasks
			(kind, report_id, user_id, report_type, period_start, group_id,
			 auto_push, status, attempts, last_error, next_retry_at)
		VALUES ('generation', NULLIF($1, 0), $2, $3, $4, NULL,
		        $5, $6, 1, $7, $8)
		ON CONFLICT (user_id, report_type, period_start)
			WHERE kind = 'generation' AND status IN ('pending', 'manual_repair')
		DO UPDATE SET
			report_id = COALESCE(EXCLUDED.report_id, report_retry_tasks.report_id),
			auto_push = EXCLUDED.auto_push,
			status = EXCLUDED.status,
			attempts = report_retry_tasks.attempts + 1,
			last_error = EXCLUDED.last_error,
			next_retry_at = EXCLUDED.next_retry_at,
			updated_at = NOW()
		RETURNING `+reportRetryTaskColumns,
		t.ReportID, t.UserID, t.ReportType, t.PeriodStart,
		t.AutoPush, t.Status, t.LastError, t.NextRetryAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("upsert generation retry returned no row")
	}
	return scanReportRetryTask(rows)
}

func (r *reportRetryRepository) UpsertPush(ctx context.Context, t *service.ReportRetryTask) (*service.ReportRetryTask, error) {
	if t == nil || t.ReportID <= 0 || t.GroupID <= 0 {
		return nil, errors.New("invalid push retry task")
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
		INSERT INTO report_retry_tasks
			(kind, report_id, user_id, report_type, period_start, group_id,
			 auto_push, status, attempts, last_error, next_retry_at)
		VALUES ('push', $1, NULLIF($2, 0), NULLIF($3, ''), NULLIF($4, to_timestamp(0)), $5,
		        FALSE, $6, 1, $7, $8)
		ON CONFLICT (report_id, group_id)
			WHERE kind = 'push' AND status IN ('pending', 'manual_repair')
		DO UPDATE SET
			status = EXCLUDED.status,
			attempts = report_retry_tasks.attempts + 1,
			last_error = EXCLUDED.last_error,
			next_retry_at = EXCLUDED.next_retry_at,
			updated_at = NOW()
		RETURNING `+reportRetryTaskColumns,
		t.ReportID, t.UserID, t.ReportType, t.PeriodStart, t.GroupID,
		t.Status, t.LastError, t.NextRetryAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("upsert push retry returned no row")
	}
	return scanReportRetryTask(rows)
}

// InsertPending 为「首次生成」上账。同一用户周期已有未结束任务（pending/manual_repair）
// 时保持原样，返回 created=false，避免定时任务/手动补跑重复插入。
func (r *reportRetryRepository) InsertPending(ctx context.Context, t *service.ReportRetryTask) (bool, error) {
	if t == nil || t.UserID <= 0 || t.ReportType == "" || t.PeriodStart.IsZero() {
		return false, errors.New("invalid generation task")
	}
	client := clientFromContext(ctx, r.client)
	res, err := client.ExecContext(ctx, `
		INSERT INTO report_retry_tasks
			(kind, report_id, user_id, report_type, period_start, group_id,
			 auto_push, suppress_push, status, attempts, last_error, next_retry_at, outcome)
		VALUES ('generation', NULL, $1, $2, $3, NULL, $4, $5, 'pending', 0, '', $6, '')
		ON CONFLICT (user_id, report_type, period_start)
			WHERE kind = 'generation' AND status IN ('pending', 'manual_repair')
		DO NOTHING
	`, t.UserID, t.ReportType, t.PeriodStart, t.AutoPush, t.SuppressPush, t.NextRetryAt)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// InsertCompleted 为「已有成功报告」的用户补一条 done 台账，避免管理端
// 「应生成 N 人」漏掉已完成的人。同周期已有任何生成任务时不再插入。
func (r *reportRetryRepository) InsertCompleted(ctx context.Context, t *service.ReportRetryTask) (bool, error) {
	if t == nil || t.UserID <= 0 || t.ReportType == "" || t.PeriodStart.IsZero() {
		return false, errors.New("invalid completed task")
	}
	client := clientFromContext(ctx, r.client)
	res, err := client.ExecContext(ctx, `
		INSERT INTO report_retry_tasks
			(kind, report_id, user_id, report_type, period_start, group_id,
			 auto_push, suppress_push, status, attempts, last_error, next_retry_at, outcome)
		SELECT 'generation', NULL, $1, $2, $3, NULL, $4, $5, 'done', 0, '', NULL, $6
		WHERE NOT EXISTS (
			SELECT 1 FROM report_retry_tasks
			WHERE kind = 'generation' AND user_id = $1 AND report_type = $2 AND period_start = $3
		)
	`, t.UserID, t.ReportType, t.PeriodStart, t.AutoPush, t.SuppressPush, domain.ReportTaskOutcomeAlreadyDone)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// ClaimGeneration 领取到期任务并写入租约。attempts 在「真正失败」时由
// FinishGeneration 递增，预算取消等场景不计次。
func (r *reportRetryRepository) ClaimGeneration(ctx context.Context, workerID string, now time.Time, limit int, leaseTTL time.Duration) ([]service.ReportRetryTask, error) {
	if limit <= 0 {
		limit = 4
	}
	if leaseTTL <= 0 {
		leaseTTL = 8 * time.Minute
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
		UPDATE report_retry_tasks
		SET lease_owner = $1,
		    lease_until = $2::timestamptz + ($3::bigint * interval '1 millisecond'),
		    last_attempt_at = $2::timestamptz,
		    updated_at = NOW()
		WHERE id IN (
			SELECT id FROM report_retry_tasks
			WHERE kind = 'generation'
			  AND status = 'pending'
			  AND (next_retry_at IS NULL OR next_retry_at <= $2::timestamptz)
			  AND (lease_until IS NULL OR lease_until <= $2::timestamptz)
			-- 日报 → 周报 → 月报：周报/月报聚合依赖子报告，必须让子任务先跑完。
			-- 同类型内按到期时间，再按 id 保证稳定顺序。
			ORDER BY CASE report_type
			             WHEN 'daily' THEN 0
			             WHEN 'weekly' THEN 1
			             ELSE 2
			         END,
			         COALESCE(next_retry_at, $2::timestamptz), id
			LIMIT $4
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+reportRetryTaskColumns,
		workerID, now, leaseTTL.Milliseconds(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]service.ReportRetryTask, 0, limit)
	for rows.Next() {
		t, err := scanReportRetryTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// FinishGeneration 写入结局并释放租约。consumeAttempt=true 时 attempts+1。
func (r *reportRetryRepository) FinishGeneration(ctx context.Context, id int64, status, outcome, lastError string, nextRetryAt *time.Time, reportID int64, consumeAttempt bool) error {
	if id <= 0 {
		return nil
	}
	client := clientFromContext(ctx, r.client)
	_, err := client.ExecContext(ctx, `
		UPDATE report_retry_tasks
		SET status = $2,
		    outcome = $3,
		    last_error = $4,
		    next_retry_at = $5,
		    report_id = COALESCE(NULLIF($6::bigint, 0), report_id),
		    attempts = attempts + CASE WHEN $7 THEN 1 ELSE 0 END,
		    lease_owner = '',
		    lease_until = NULL,
		    updated_at = NOW()
		WHERE id = $1
	`, id, status, outcome, lastError, nextRetryAt, reportID, consumeAttempt)
	return err
}

// SummarizeByPeriod 汇总「每个用户最新一条任务」的状态（同一用户历史上可能有
// 多条 done/skipped 记录，只取最新一条，避免重复计数）。
func (r *reportRetryRepository) SummarizeByPeriod(ctx context.Context, reportType string, periodStart time.Time, now time.Time) (service.ReportTaskSummary, error) {
	out := service.ReportTaskSummary{Type: reportType, PeriodStart: periodStart}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE status = 'done'),
			COUNT(*) FILTER (WHERE status = 'skipped'),
			COUNT(*) FILTER (WHERE status = 'failed'),
			COUNT(*) FILTER (WHERE status = 'manual_repair'),
			COUNT(*) FILTER (WHERE status = 'pending' AND lease_until IS NOT NULL AND lease_until > $3::timestamptz),
			COUNT(*) FILTER (WHERE status = 'pending' AND (lease_until IS NULL OR lease_until <= $3::timestamptz))
		FROM (
			SELECT DISTINCT ON (user_id) status, lease_until
			FROM report_retry_tasks
			WHERE kind = 'generation' AND report_type = $1 AND period_start = $2
			ORDER BY user_id, id DESC
		) latest
	`, reportType, periodStart, now)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	if !rows.Next() {
		return out, rows.Err()
	}
	if err := rows.Scan(&out.Total, &out.Done, &out.Skipped, &out.Failed, &out.ManualRepair, &out.Running, &out.Pending); err != nil {
		return out, err
	}
	return out, rows.Err()
}

// ListByPeriod 列出该周期内最新任务处于指定状态的用户。
func (r *reportRetryRepository) ListByPeriod(ctx context.Context, reportType string, periodStart time.Time, statuses []string) ([]service.ReportTaskItem, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
		SELECT t.user_id, COALESCE(u.username, ''), t.status, t.outcome, t.attempts, t.last_error, t.next_retry_at
		FROM (
			SELECT DISTINCT ON (user_id) *
			FROM report_retry_tasks
			WHERE kind = 'generation' AND report_type = $1 AND period_start = $2
			ORDER BY user_id, id DESC
		) t
		LEFT JOIN users u ON u.id = t.user_id
		WHERE t.status = ANY($3)
		ORDER BY t.status, t.user_id
	`, reportType, periodStart, pq.Array(statuses))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]service.ReportTaskItem, 0, 16)
	for rows.Next() {
		var item service.ReportTaskItem
		var nextRetry sql.NullTime
		if err := rows.Scan(&item.UserID, &item.Username, &item.Status, &item.Outcome, &item.Attempts, &item.LastError, &nextRetry); err != nil {
			return nil, err
		}
		if nextRetry.Valid {
			v := nextRetry.Time
			item.NextRetryAt = &v
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ResetByPeriod 把该周期内「每个用户最新一条」处于指定状态的任务重新置为 pending。
// 同时把同周期被取代的旧未结束任务关掉，避免触发唯一索引冲突。
func (r *reportRetryRepository) ResetByPeriod(ctx context.Context, reportType string, periodStart time.Time, statuses []string) (int64, error) {
	if len(statuses) == 0 {
		return 0, nil
	}
	client := clientFromContext(ctx, r.client)
	res, err := client.ExecContext(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (user_id) id
			FROM report_retry_tasks
			WHERE kind = 'generation' AND report_type = $1 AND period_start = $2
			  AND status = ANY($3)
			ORDER BY user_id, id DESC
		), superseded AS (
			UPDATE report_retry_tasks t
			SET status = 'done', next_retry_at = NULL, lease_owner = '', lease_until = NULL, updated_at = NOW()
			WHERE t.kind = 'generation' AND t.report_type = $1 AND t.period_start = $2
			  AND t.status IN ('pending', 'manual_repair')
			  AND t.id NOT IN (SELECT id FROM latest)
			RETURNING t.id
		)
		UPDATE report_retry_tasks t
		SET status = 'pending', next_retry_at = NOW(), attempts = 0, outcome = '',
		    last_error = '', lease_owner = '', lease_until = NULL, updated_at = NOW()
		WHERE t.id IN (SELECT id FROM latest)
	`, reportType, periodStart, pq.Array(statuses))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *reportRetryRepository) MarkDone(ctx context.Context, id int64) error {
	if id <= 0 {
		return nil
	}
	client := clientFromContext(ctx, r.client)
	_, err := client.ExecContext(ctx, `
		UPDATE report_retry_tasks
		SET status = 'done', next_retry_at = NULL, updated_at = NOW()
		WHERE id = $1 AND status IN ('pending', 'manual_repair')
	`, id)
	return err
}

func (r *reportRetryRepository) MarkDoneByGeneration(ctx context.Context, userID int64, reportType string, periodStart time.Time) error {
	if userID <= 0 || reportType == "" || periodStart.IsZero() {
		return nil
	}
	client := clientFromContext(ctx, r.client)
	_, err := client.ExecContext(ctx, `
		UPDATE report_retry_tasks
		SET status = 'done', next_retry_at = NULL, updated_at = NOW()
		WHERE kind = 'generation' AND user_id = $1 AND report_type = $2
		  AND period_start = $3 AND status IN ('pending', 'manual_repair')
	`, userID, reportType, periodStart)
	return err
}

func (r *reportRetryRepository) MarkDoneByPush(ctx context.Context, reportID, groupID int64) error {
	if reportID <= 0 || groupID <= 0 {
		return nil
	}
	client := clientFromContext(ctx, r.client)
	_, err := client.ExecContext(ctx, `
		UPDATE report_retry_tasks
		SET status = 'done', next_retry_at = NULL, updated_at = NOW()
		WHERE kind = 'push' AND report_id = $1 AND group_id = $2
		  AND status IN ('pending', 'manual_repair')
	`, reportID, groupID)
	return err
}

func (r *reportRetryRepository) ListDue(ctx context.Context, kind string, now time.Time, limit int) ([]service.ReportRetryTask, error) {
	if limit <= 0 {
		limit = 50
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
		SELECT `+reportRetryTaskColumns+`
		FROM report_retry_tasks
		WHERE kind = $1 AND status = 'pending'
		  AND next_retry_at IS NOT NULL AND next_retry_at <= $2
		ORDER BY next_retry_at
		LIMIT $3
	`, kind, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]service.ReportRetryTask, 0, limit)
	for rows.Next() {
		t, err := scanReportRetryTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (r *reportRetryRepository) ListActiveByReportIDs(ctx context.Context, reportIDs []int64) (map[int64][]service.ReportRetryTask, error) {
	out := make(map[int64][]service.ReportRetryTask, len(reportIDs))
	if len(reportIDs) == 0 {
		return out, nil
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
		SELECT `+reportRetryTaskColumns+`
		FROM report_retry_tasks
		WHERE report_id = ANY($1) AND status IN ('pending', 'manual_repair')
		ORDER BY id
	`, pq.Array(reportIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		t, err := scanReportRetryTask(rows)
		if err != nil {
			return nil, err
		}
		out[t.ReportID] = append(out[t.ReportID], *t)
	}
	return out, rows.Err()
}

func (r *reportRetryRepository) GetByID(ctx context.Context, id int64) (*service.ReportRetryTask, error) {
	if id <= 0 {
		return nil, domain.ErrReportNotFound
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
		SELECT `+reportRetryTaskColumns+`
		FROM report_retry_tasks WHERE id = $1
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, domain.ErrReportNotFound
	}
	return scanReportRetryTask(rows)
}
