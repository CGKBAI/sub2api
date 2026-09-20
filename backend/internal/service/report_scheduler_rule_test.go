package service

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// skip_holidays 生成日规则：cron 只决定触发时刻（默认每天 19:00 评估一次），
// 日报=工作日、周报=本周最后工作日、月报=本月首个工作日。
// 日期均为 2026 年内置节假日表覆盖的真实日历。
func TestReportDueForDay(t *testing.T) {
	if err := timezone.Init("Asia/Shanghai"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = timezone.Init("UTC") })

	loc := timezone.Location()
	at := func(y, m, d, hour, min int) time.Time {
		return time.Date(y, time.Month(m), d, hour, min, 0, 0, loc)
	}
	day := func(y, m, d int) time.Time { return at(y, m, d, 19, 0) }

	cases := []struct {
		name      string
		kind      string
		now       time.Time
		genMarker time.Time
		want      bool
	}{
		// 日报：工作日规则
		{"日报普通周一", "daily", day(2026, 9, 14), time.Time{}, true},
		{"日报普通周六不发", "daily", day(2026, 9, 19), time.Time{}, false},
		{"日报调休补班周日发", "daily", day(2026, 9, 20), time.Time{}, true},
		{"日报法定假不发", "daily", day(2026, 10, 1), time.Time{}, false},
		// 周报：本周最后工作日
		{"周报周中未到生成日", "weekly", day(2026, 11, 4), time.Time{}, false},
		{"周报普通周周五生成", "weekly", day(2026, 11, 6), time.Time{}, true},
		{"周报本周已生成去重", "weekly", day(2026, 11, 6), at(2026, 11, 2, 19, 0), false},
		{"周报生成后周末去重", "weekly", day(2026, 11, 7), at(2026, 11, 6, 19, 0), false},
		{"周报补班周周五仍未到", "weekly", day(2026, 9, 18), at(2026, 9, 7, 19, 0), false},
		{"周报补班周周日生成", "weekly", day(2026, 9, 20), at(2026, 9, 7, 19, 0), true},
		{"周报国庆前周周三月末工作日", "weekly", day(2026, 9, 30), at(2026, 9, 18, 20, 10), true},
		{"周报调休周周五未到周六", "weekly", day(2026, 10, 9), at(2026, 9, 25, 19, 0), false},
		{"周报调休周周六生成", "weekly", day(2026, 10, 10), at(2026, 9, 25, 19, 0), true},
		{"周报整周法定假不出", "weekly", day(2026, 2, 18), at(2026, 2, 14, 19, 0), false},
		// 月报：本月首个工作日
		{"月报国庆1日未到首个工作日", "monthly", day(2026, 10, 1), at(2026, 9, 1, 20, 20), false},
		{"月报10月8日首个工作日生成", "monthly", day(2026, 10, 8), at(2026, 9, 1, 20, 20), true},
		{"月报本月已生成去重", "monthly", day(2026, 10, 9), at(2026, 10, 8, 19, 0), false},
		{"月报1日即工作日直接生成", "monthly", day(2026, 9, 1), at(2026, 8, 3, 19, 0), true},
		{"月报元旦1日顺延", "monthly", day(2026, 1, 1), at(2025, 12, 1, 20, 20), false},
		{"月报元旦补班周日生成", "monthly", day(2026, 1, 4), at(2025, 12, 1, 20, 20), true},
		{"未知类型不出", "hourly", day(2026, 9, 18), time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reportDueForDay(tc.kind, tc.now, tc.genMarker); got != tc.want {
				t.Fatalf("reportDueForDay(%s, %s, %v) = %v, want %v",
					tc.kind, tc.now.Format("2006-01-02 15:04"), tc.genMarker, got, tc.want)
			}
		})
	}
}
