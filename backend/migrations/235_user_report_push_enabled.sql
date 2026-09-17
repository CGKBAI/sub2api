-- 235_user_report_push_enabled.sql
-- 报告飞书推送：用户级"参与推送"开关。纯增量加列，存量用户默认参与；
-- 关闭后该用户的报告不再自动推送到飞书群（手动按钮推送不受此限制）。
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS report_push_enabled BOOLEAN NOT NULL DEFAULT true;
