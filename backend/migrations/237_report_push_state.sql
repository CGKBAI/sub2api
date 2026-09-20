-- 飞书推送状态：记录每篇报告最近一次推送结果（自动/手动均记录）。
-- pushed_at = 最近一次推送成功时间；last_push_error = 最近一次失败原因（成功时清空）。
-- 重复生成走覆盖更新（Update 不触碰这两列），推送状态跨重生成保留。

ALTER TABLE reports ADD COLUMN IF NOT EXISTS pushed_at TIMESTAMPTZ NULL;
ALTER TABLE reports ADD COLUMN IF NOT EXISTS last_push_error TEXT NOT NULL DEFAULT '';
