-- 234_prompt_audit_session_id.sql
-- 审计表补 session 维度：记录请求所属的客户端会话（来自请求头，如
-- X-Claude-Code-Session-Id / OpenAI 兼容 session-id）。纯增量加列，历史行为空串。
-- 用途：报告生成可按 session 聚合，web 端未来可按会话展示审计记录。
ALTER TABLE prompt_audit_jobs
    ADD COLUMN IF NOT EXISTS session_id VARCHAR(255) NOT NULL DEFAULT '';

ALTER TABLE prompt_audit_events
    ADD COLUMN IF NOT EXISTS session_id VARCHAR(255) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_prompt_audit_events_session
    ON prompt_audit_events (user_id, session_id, created_at DESC)
    WHERE session_id <> '';
