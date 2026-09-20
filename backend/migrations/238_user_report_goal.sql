-- 238_user_report_goal.sql
-- 日报「近期目标」：用户自行填写的近期工作目标文本（单文本框）。
-- 仅日报生成时读取并注入 LLM 上下文（计划小节对齐目标拆解）；
-- 周报/月报聚合日报摘要，目标经日报摘要自然继承，不直接读取本列。
-- 纯增量加列；不做历史快照，修改后按新目标生成。
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS report_goal TEXT NOT NULL DEFAULT '';
