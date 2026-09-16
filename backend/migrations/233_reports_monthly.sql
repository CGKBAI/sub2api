-- 233_reports_monthly.sql
-- 新增月报类型：放宽 reports.type 的 CHECK 约束以允许 'monthly'。
-- 纯放宽变更，旧版本代码（只识别 daily/weekly）仍可正常运行。
ALTER TABLE reports
    DROP CONSTRAINT IF EXISTS chk_reports_type;

ALTER TABLE reports
    ADD CONSTRAINT chk_reports_type
    CHECK (type IN ('daily', 'weekly', 'monthly'));
