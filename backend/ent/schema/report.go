// Package schema 定义 Ent ORM 的数据库 schema。
package schema

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Report 定义日报/周报实体的 schema。
//
// 每个用户每个周期（日/周）一行，(user_id, type, period_start) 唯一，
// 重复生成走覆盖更新。
type Report struct {
	ent.Schema
}

// Annotations 返回 schema 的注解配置。
func (Report) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "reports"},
	}
}

// Fields 定义报告实体的所有字段。
func (Report) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id").
			Comment("所属用户 ID"),
		field.String("type").
			MaxLen(10).
			Comment("报告类型: daily, weekly, monthly"),
		field.Time("period_start").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).
			Comment("周期起点（含），服务器时区"),
		field.Time("period_end").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).
			Comment("周期终点（不含），服务器时区"),
		field.JSON("stats", domain.ReportStats{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("聚合统计（请求/token/费用/模型分布/活跃时段）"),
		field.String("ai_summary").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default("").
			Comment("LLM 生成的工作摘要（Markdown）"),
		field.String("status").
			MaxLen(20).
			Default(domain.ReportStatusDone).
			Comment("状态: pending, done, failed"),
		field.String("error").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default("").
			Comment("生成失败原因"),
		field.Time("pushed_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).
			Comment("最近一次飞书推送成功时间（自动/手动，重生成不清除）"),
		field.String("last_push_error").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default("").
			Comment("最近一次飞书推送失败原因（成功时清空）"),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Indexes 定义数据库索引。
func (Report) Indexes() []ent.Index {
	return []ent.Index{
		// 唯一约束：防双跑/重复点击产生重复报告（前缀已覆盖查询需求，
		// 勿再加同列非唯一索引——与唯一索引同名会导致 ent 自动迁移建索引冲突）
		index.Fields("user_id", "type", "period_start").Unique(),
		index.Fields("type", "period_start"),
	}
}
