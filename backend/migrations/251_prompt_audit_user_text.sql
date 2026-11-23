-- 审计事件增加「抽干净的用户输入」（P2）。
-- 背景：报告侧的「用户提问记录」此前只能从 full_prompt 头部 2000 字里用正则猜，
-- 遇到未闭合的 <system-reminder> 会直接返回空——实测 8.1% 的请求因此丢失提问。
-- 写入时直接存剥掉注入块后的最新用户消息（≤4000 字），报告侧优先读它。
-- 纯增量：加带默认值的列，旧镜像/旧数据不受影响；历史行靠报告侧 COALESCE 兜底。
ALTER TABLE prompt_audit_events ADD COLUMN IF NOT EXISTS user_text TEXT NOT NULL DEFAULT '';
