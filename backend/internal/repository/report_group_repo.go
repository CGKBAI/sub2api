package repository

import (
	"context"
	"errors"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// reportGroupRepository 报告飞书群组/成员/投递结果仓储。
// 群组与成员关系简单、查询以列表和批量为主，直接用原生 SQL，
// 避免为纯关联表引入 ent schema 与代码生成。
type reportGroupRepository struct {
	client *dbent.Client
}

// NewReportGroupRepository 构造报告飞书群组仓储。
func NewReportGroupRepository(client *dbent.Client) service.ReportGroupRepository {
	return &reportGroupRepository{client: client}
}

const reportFeishuGroupColumns = `
	g.id, g.name, g.webhook_url, g.secret, g.enabled,
	g.created_at, g.updated_at,
	(SELECT COUNT(*) FROM report_feishu_group_members m WHERE m.group_id = g.id) AS member_count
`

func scanReportFeishuGroup(row interface{ Scan(...any) error }) (*service.ReportFeishuGroup, error) {
	var g service.ReportFeishuGroup
	if err := row.Scan(
		&g.ID, &g.Name, &g.WebhookURL, &g.Secret, &g.Enabled,
		&g.CreatedAt, &g.UpdatedAt, &g.MemberCount,
	); err != nil {
		return nil, err
	}
	return &g, nil
}

func (r *reportGroupRepository) ListGroups(ctx context.Context) ([]service.ReportFeishuGroup, error) {
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
		SELECT `+reportFeishuGroupColumns+`
		FROM report_feishu_groups g
		ORDER BY g.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]service.ReportFeishuGroup, 0, 8)
	for rows.Next() {
		g, err := scanReportFeishuGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

func (r *reportGroupRepository) GetGroup(ctx context.Context, id int64) (*service.ReportFeishuGroup, error) {
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
		SELECT `+reportFeishuGroupColumns+`
		FROM report_feishu_groups g
		WHERE g.id = $1
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, domain.ErrReportFeishuGroupNotFound
	}
	return scanReportFeishuGroup(rows)
}

func (r *reportGroupRepository) CreateGroup(ctx context.Context, g *service.ReportFeishuGroup) error {
	if g == nil {
		return errors.New("nil report feishu group")
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
		INSERT INTO report_feishu_groups (name, webhook_url, secret, enabled)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at, updated_at
	`, g.Name, g.WebhookURL, g.Secret, g.Enabled)
	if isUniqueViolation(err) {
		return domain.ErrReportFeishuGroupNameDup
	}
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return errors.New("insert report feishu group returned no row")
	}
	return rows.Scan(&g.ID, &g.CreatedAt, &g.UpdatedAt)
}

func (r *reportGroupRepository) UpdateGroup(ctx context.Context, g *service.ReportFeishuGroup) error {
	if g == nil {
		return errors.New("nil report feishu group")
	}
	client := clientFromContext(ctx, r.client)
	res, err := client.ExecContext(ctx, `
		UPDATE report_feishu_groups
		SET name = $2, webhook_url = $3, secret = $4, enabled = $5, updated_at = NOW()
		WHERE id = $1
	`, g.ID, g.Name, g.WebhookURL, g.Secret, g.Enabled)
	if isUniqueViolation(err) {
		return domain.ErrReportFeishuGroupNameDup
	}
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrReportFeishuGroupNotFound
	}
	return nil
}

func (r *reportGroupRepository) DeleteGroup(ctx context.Context, id int64) error {
	client := clientFromContext(ctx, r.client)
	res, err := client.ExecContext(ctx, `DELETE FROM report_feishu_groups WHERE id = $1`, id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrReportFeishuGroupNotFound
	}
	return nil
}

func (r *reportGroupRepository) ListGroupMemberIDs(ctx context.Context, groupID int64) ([]int64, error) {
	if _, err := r.GetGroup(ctx, groupID); err != nil {
		return nil, err
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
		SELECT user_id
		FROM report_feishu_group_members
		WHERE group_id = $1
		ORDER BY user_id
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]int64, 0, 16)
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out = append(out, uid)
	}
	return out, rows.Err()
}

// ReplaceGroupMembers 在同一个事务里「清空 + 批量写入」成员，
// 避免删除成功但写入失败导致成员丢失。
// 注意不能用 `WITH deleted AS (DELETE ...) INSERT ...`：数据修改 CTE 与主语句
// 共享同一快照并发执行，INSERT 可能与尚未删除的旧成员冲突后被 DO NOTHING 吞掉。
func (r *reportGroupRepository) ReplaceGroupMembers(ctx context.Context, groupID int64, userIDs []int64) error {
	if _, err := r.GetGroup(ctx, groupID); err != nil {
		return err
	}
	client := clientFromContext(ctx, r.client)
	tx, err := client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Client().ExecContext(ctx, `
		DELETE FROM report_feishu_group_members WHERE group_id = $1
	`, groupID); err != nil {
		return err
	}
	if len(userIDs) > 0 {
		if _, err := tx.Client().ExecContext(ctx, `
			INSERT INTO report_feishu_group_members (group_id, user_id)
			SELECT $1, unnest($2::bigint[])
			ON CONFLICT (group_id, user_id) DO NOTHING
		`, groupID, pq.Array(userIDs)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *reportGroupRepository) ListEnabledGroupsForUser(ctx context.Context, userID int64) ([]service.ReportFeishuGroup, error) {
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
		SELECT g.id, g.name, g.webhook_url, g.secret, g.enabled
		FROM report_feishu_groups g
		JOIN report_feishu_group_members m ON m.group_id = g.id
		WHERE m.user_id = $1 AND g.enabled = TRUE
		ORDER BY g.id
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]service.ReportFeishuGroup, 0, 4)
	for rows.Next() {
		var g service.ReportFeishuGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.WebhookURL, &g.Secret, &g.Enabled); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (r *reportGroupRepository) UpsertDelivery(ctx context.Context, d *service.ReportPushDelivery) error {
	if d == nil {
		return errors.New("nil report push delivery")
	}
	if d.AttemptedAt.IsZero() {
		d.AttemptedAt = time.Now()
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
		INSERT INTO report_feishu_deliveries
			(report_id, group_id, status, error, attempted_at, attempt_count)
		VALUES ($1, $2, $3, $4, $5, 1)
		ON CONFLICT (report_id, group_id) DO UPDATE SET
			status = EXCLUDED.status,
			error = EXCLUDED.error,
			attempted_at = EXCLUDED.attempted_at,
			attempt_count = report_feishu_deliveries.attempt_count + 1
		RETURNING attempt_count
	`, d.ReportID, d.GroupID, d.Status, d.Error, d.AttemptedAt)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return errors.New("upsert report delivery returned no row")
	}
	return rows.Scan(&d.AttemptCount)
}

func (r *reportGroupRepository) ListDeliveriesByReportIDs(ctx context.Context, reportIDs []int64) (map[int64][]service.ReportPushDelivery, error) {
	out := make(map[int64][]service.ReportPushDelivery, len(reportIDs))
	if len(reportIDs) == 0 {
		return out, nil
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
		SELECT d.report_id, d.group_id, COALESCE(g.name, ''), d.status, d.error,
		       d.attempted_at, d.attempt_count
		FROM report_feishu_deliveries d
		LEFT JOIN report_feishu_groups g ON g.id = d.group_id
		WHERE d.report_id = ANY($1)
		ORDER BY d.report_id, d.group_id
	`, pq.Array(reportIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var d service.ReportPushDelivery
		if err := rows.Scan(
			&d.ReportID, &d.GroupID, &d.GroupName, &d.Status,
			&d.Error, &d.AttemptedAt, &d.AttemptCount,
		); err != nil {
			return nil, err
		}
		out[d.ReportID] = append(out[d.ReportID], d)
	}
	return out, rows.Err()
}
