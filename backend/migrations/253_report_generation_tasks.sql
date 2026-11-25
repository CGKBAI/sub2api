-- 报告生成任务队列（把「失败重试表」升级为「先上账、再干活」的任务表）。
--
-- 纯增量：
--   1) 只加可空/带默认值的列，不改既有列类型；
--   2) 只放宽 CHECK 约束（旧镜像只写 pending/manual_repair/done，不受影响）；
--   3) 不动 idx_report_retry_tasks_generation_active 的谓词，保证滚动/回滚期间
--      旧镜像的 ON CONFLICT ... WHERE status IN ('pending','manual_repair') 仍能匹配索引。
--
-- 语义：
--   status = pending     待生成（含首次尝试与 30 分钟重试；next_retry_at 决定何时可领）
--   status = done        已生成
--   status = skipped     设计性跳过（无用量 / 低于日报阈值 / 已有成功报告），不计失败
--   status = failed      重试次数耗尽，需人工介入
--   status = manual_repair 永久错误（401/403/404、敏感内容），停止自动重试
--   running 不写库：pending 且 lease_until > now() 即视为「正在生成」。
--
-- lease_owner/lease_until：worker 领取任务时的租约，进程崩溃后租约过期即可被重新领取。
-- outcome：结局原因（generated / already_done / low_usage / no_usage /
--          sensitive_content / permanent_error / attempts_exhausted），管理端统计用。
-- suppress_push：定时日报在周报/月报发布日被抑制自动推送，重试时必须沿用该标记
--                （旧实现重试时硬编码 false，会把被抑制的日报推出去）。

ALTER TABLE report_retry_tasks
    ADD COLUMN IF NOT EXISTS lease_owner     VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS lease_until     TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS outcome         VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS suppress_push   BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS last_attempt_at TIMESTAMPTZ NULL;

ALTER TABLE report_retry_tasks DROP CONSTRAINT IF EXISTS chk_report_retry_tasks_status;
ALTER TABLE report_retry_tasks ADD CONSTRAINT chk_report_retry_tasks_status
    CHECK (status IN ('pending', 'done', 'skipped', 'failed', 'manual_repair'));

-- 领取任务时按 (kind, lease_until) 过滤过期租约；与既有的 due 索引互补。
CREATE INDEX IF NOT EXISTS idx_report_retry_tasks_lease
    ON report_retry_tasks (kind, lease_until)
    WHERE status = 'pending';

COMMENT ON COLUMN report_retry_tasks.lease_owner IS '当前领取该任务的 worker 实例 ID；空串表示未领取';
COMMENT ON COLUMN report_retry_tasks.lease_until IS '租约到期时间；过期后任务可被其他 worker 重新领取（进程崩溃兜底）';
COMMENT ON COLUMN report_retry_tasks.outcome IS '任务结局原因（generated/low_usage/sensitive_content/permanent_error/attempts_exhausted 等）';
COMMENT ON COLUMN report_retry_tasks.suppress_push IS '生成成功后是否抑制自动推送（周报/月报发布日的日报为 true）';
COMMENT ON COLUMN report_retry_tasks.last_attempt_at IS '最近一次被 worker 领取的时间';
