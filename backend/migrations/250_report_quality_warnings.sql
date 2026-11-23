-- 报告质量校验结果（v4.2）。
-- 生成后跑确定性规则（小节结构/禁语/条目长度/敏感信息），硬违规带反馈重写一次；
-- 重写后仍存在时正文照发，并在此记录遗留问题（多条用「；」分隔）；空串 = 通过。
-- 纯增量：只加可空性有默认值的列，旧镜像继续写入不受影响。
ALTER TABLE reports ADD COLUMN IF NOT EXISTS quality_warnings TEXT NOT NULL DEFAULT '';
