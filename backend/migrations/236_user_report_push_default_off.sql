-- 236_user_report_push_default_off.sql
-- 飞书自动推送默认关闭：列默认值 true → false，存量用户全部回填 false（v3.4 决策）。
-- 管理员可在用户管理列表逐个打开；用户本人仍可在报告页自行开关（双向）。
-- 手动"发送到飞书"按钮不受此开关限制。
ALTER TABLE users
    ALTER COLUMN report_push_enabled SET DEFAULT false;

UPDATE users SET report_push_enabled = false WHERE report_push_enabled = true;
