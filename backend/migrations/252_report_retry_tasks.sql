-- 报告生成/推送失败重试队列。
-- 纯增量：只建新表/新索引，不修改既有表。
--
-- generation：某个用户某周期报告生成失败（LLM 瞬时错误）后的重试任务；
--             有旧成功报告时不进队列，直接保留旧报告并按需补推。
-- push：某份报告投递到某个群失败后的重试任务；已成功的群不重复投递。
--
-- status: pending（等待重试）/ manual_repair（永久错误，需人工修复）/ done（已结束）
-- next_retry_at: 下次重试时间；失败后固定 +30 分钟，成功后置空。

CREATE TABLE IF NOT EXISTS report_retry_tasks (
    id BIGSERIAL PRIMARY KEY,
    kind VARCHAR(16) NOT NULL,
    report_id BIGINT NULL,
    user_id BIGINT NULL,
    report_type VARCHAR(16) NULL,
    period_start TIMESTAMPTZ NULL,
    group_id BIGINT NULL,
    auto_push BOOLEAN NOT NULL DEFAULT FALSE,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    next_retry_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_report_retry_tasks_kind
        CHECK (kind IN ('generation', 'push')),
    CONSTRAINT chk_report_retry_tasks_status
        CHECK (status IN ('pending', 'manual_repair', 'done')),
    CONSTRAINT fk_report_retry_tasks_report
        FOREIGN KEY (report_id) REFERENCES reports (id) ON DELETE CASCADE,
    CONSTRAINT fk_report_retry_tasks_user
        FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
    CONSTRAINT fk_report_retry_tasks_group
        FOREIGN KEY (group_id) REFERENCES report_feishu_groups (id) ON DELETE CASCADE
);

-- 同一用户同一周期只保留一个未结束的生成重试任务。
CREATE UNIQUE INDEX IF NOT EXISTS idx_report_retry_tasks_generation_active
    ON report_retry_tasks (user_id, report_type, period_start)
    WHERE kind = 'generation' AND status IN ('pending', 'manual_repair');

-- 同一份报告同一个群只保留一个未结束的推送重试任务。
CREATE UNIQUE INDEX IF NOT EXISTS idx_report_retry_tasks_push_active
    ON report_retry_tasks (report_id, group_id)
    WHERE kind = 'push' AND status IN ('pending', 'manual_repair');

CREATE INDEX IF NOT EXISTS idx_report_retry_tasks_due
    ON report_retry_tasks (kind, status, next_retry_at)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_report_retry_tasks_report
    ON report_retry_tasks (report_id)
    WHERE report_id IS NOT NULL;

COMMENT ON TABLE report_retry_tasks IS '报告生成/飞书推送失败重试队列（30 分钟一次，永久错误转 manual_repair）';
COMMENT ON COLUMN report_retry_tasks.auto_push IS '生成重试成功后是否按自动推送规则发送（定时 true / 手动 false）';
COMMENT ON COLUMN report_retry_tasks.next_retry_at IS '下次重试时间；NULL 表示不再重试（manual_repair/done）';
