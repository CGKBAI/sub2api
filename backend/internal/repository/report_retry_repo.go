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
	created_at, updated_at
`

func scanReportRetryTask(row interface{ Scan(...any) error }) (*service.ReportRetryTask, error) {
	var t service.ReportRetryTask
	var nextRetry sql.NullTime
	if err := row.Scan(
		&t.ID, &t.Kind,
		&t.ReportID, &t.UserID, &t.ReportType,
		&t.PeriodStart, &t.GroupID,
		&t.AutoPush, &t.Status, &t.Attempts, &t.LastError, &nextRetry,
		&t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if nextRetry.Valid {
		v := nextRetry.Time
		t.NextRetryAt = &v
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
