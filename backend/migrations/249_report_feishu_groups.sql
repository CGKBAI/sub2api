-- 报告飞书多群推送：群组、群成员（多对多）、每个报告×群的投递结果。
-- 纯增量：只建新表/新索引，不修改既有表结构。
-- 一个用户可以属于多个群组（身兼多部门），同一份报告会投递到其所属的所有启用群组。

CREATE TABLE IF NOT EXISTS report_feishu_groups (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    webhook_url TEXT NOT NULL DEFAULT '',
    secret TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_report_feishu_groups_name
    ON report_feishu_groups (name);

CREATE TABLE IF NOT EXISTS report_feishu_group_members (
    group_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (group_id, user_id),
    CONSTRAINT fk_report_feishu_group_members_group
        FOREIGN KEY (group_id) REFERENCES report_feishu_groups (id) ON DELETE CASCADE,
    CONSTRAINT fk_report_feishu_group_members_user
        FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_report_feishu_group_members_user
    ON report_feishu_group_members (user_id);

CREATE TABLE IF NOT EXISTS report_feishu_deliveries (
    id BIGSERIAL PRIMARY KEY,
    report_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    status VARCHAR(16) NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    attempted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    attempt_count INTEGER NOT NULL DEFAULT 1,
    CONSTRAINT fk_report_feishu_deliveries_report
        FOREIGN KEY (report_id) REFERENCES reports (id) ON DELETE CASCADE,
    CONSTRAINT fk_report_feishu_deliveries_group
        FOREIGN KEY (group_id) REFERENCES report_feishu_groups (id) ON DELETE CASCADE,
    CONSTRAINT chk_report_feishu_deliveries_status
        CHECK (status IN ('success', 'failed'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_report_feishu_deliveries_report_group
    ON report_feishu_deliveries (report_id, group_id);

CREATE INDEX IF NOT EXISTS idx_report_feishu_deliveries_group
    ON report_feishu_deliveries (group_id, attempted_at DESC);

COMMENT ON TABLE report_feishu_groups IS '报告飞书推送群组（每个群一个自定义机器人 webhook）';
COMMENT ON TABLE report_feishu_group_members IS '报告群组成员：用户与群组多对多，身兼多部门的用户会在每个群各收到一份报告';
COMMENT ON TABLE report_feishu_deliveries IS '报告×群组最近一次投递结果（UPSERT，attempt_count 记录尝试次数）';

-- 存量迁移：把当前全局 webhook 初始化为「计算群」，避免上线后推送中断。
-- 仅当 settings.report_config 中存在非空 webhook 且尚未创建同名群组时执行。
INSERT INTO report_feishu_groups (name, webhook_url, secret, enabled)
SELECT
    '计算群',
    BTRIM(s.value::jsonb ->> 'feishu_webhook_url'),
    COALESCE(s.value::jsonb ->> 'feishu_secret', ''),
    TRUE
FROM settings s
WHERE s.key = 'report_config'
  AND COALESCE(BTRIM(s.value::jsonb ->> 'feishu_webhook_url'), '') <> ''
  AND NOT EXISTS (SELECT 1 FROM report_feishu_groups g WHERE g.name = '计算群');

-- 当前已参与自动推送（report_push_enabled=true）的用户加入「计算群」。
-- 未勾选的用户保持不推送；管理员可在「报告群组」里按实际部门调整成员。
INSERT INTO report_feishu_group_members (group_id, user_id)
SELECT g.id, u.id
FROM report_feishu_groups g
JOIN users u
  ON u.report_push_enabled = TRUE
 AND u.deleted_at IS NULL
WHERE g.name = '计算群'
ON CONFLICT (group_id, user_id) DO NOTHING;
