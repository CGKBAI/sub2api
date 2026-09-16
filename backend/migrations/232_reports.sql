-- 日报/周报：每个用户每个周期一行，聚合统计 + LLM 生成的工作摘要。
-- (user_id, type, period_start) 唯一，重复生成走覆盖更新。
-- 用户删除时级联删除其报告。

CREATE TABLE IF NOT EXISTS reports (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL,
    type VARCHAR(10) NOT NULL,
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    stats JSONB NOT NULL DEFAULT '{}'::jsonb,
    ai_summary TEXT NOT NULL DEFAULT '',
    status VARCHAR(20) NOT NULL DEFAULT 'done',
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_reports_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT chk_reports_type CHECK (type IN ('daily', 'weekly')),
    CONSTRAINT chk_reports_status CHECK (status IN ('pending', 'done', 'failed')),
    CONSTRAINT chk_reports_period CHECK (period_end > period_start)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_reports_user_type_period ON reports (user_id, type, period_start);
CREATE INDEX IF NOT EXISTS idx_reports_type_period ON reports (type, period_start DESC, id DESC);

COMMENT ON TABLE reports IS '日报/周报：按用户×周期聚合 usage_logs 并用 LLM 生成工作摘要';
COMMENT ON COLUMN reports.stats IS '聚合统计 JSON：requests/input_tokens/output_tokens/total_cost/models/model_tokens/hourly/prompt_count';
