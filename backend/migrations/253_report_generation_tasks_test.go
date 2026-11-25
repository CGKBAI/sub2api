package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 253 把「失败重试表」升级为「生成任务表」，必须保持纯增量且与旧镜像兼容：
//   - 只加列（带默认值），不改列类型；
//   - 放宽 CHECK 允许 skipped/failed；
//   - 不改 generation_active 唯一索引的谓词（旧镜像的 ON CONFLICT 依赖它）。
func TestReportGenerationTasksMigrationIsAdditive(t *testing.T) {
	content, err := FS.ReadFile("253_report_generation_tasks.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")

	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS lease_owner")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS lease_until")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS outcome")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS suppress_push")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS last_attempt_at")

	require.Contains(t, sql, "CHECK (status IN ('pending', 'done', 'skipped', 'failed', 'manual_repair'))")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_report_retry_tasks_lease")

	// 兼容性红线：唯一索引谓词不允许变化，也不能被 DROP。
	require.NotContains(t, sql, "DROP INDEX")
	require.NotContains(t, sql, "ALTER COLUMN")
	require.NotContains(t, sql, "DROP COLUMN")
}
