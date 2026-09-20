package service

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// 生成日规则：cron 只决定触发时刻（默认每天 19:00 评估一次）。
// 开（skip_holidays=true）：日报=工作日、周报=本周最后工作日、月报=本月首个工作日；
// 关（skip_holidays=false）：日报=每天、周报=周五、月报=月底。
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
		skip      bool
		want      bool
	}{
		// ===== 开（skip_holidays=true）：工作日规则 =====
		{"开日报普通周一", "daily", day(2026, 9, 14), time.Time{}, true, true},
		{"开日报普通周六不发", "daily", day(2026, 9, 19), time.Time{}, true, false},
		{"开日报调休补班周日发", "daily", day(2026, 9, 20), time.Time{}, true, true},
		{"开日报法定假不发", "daily", day(2026, 10, 1), time.Time{}, true, false},
		{"开周报周中未到生成日", "weekly", day(2026, 11, 4), time.Time{}, true, false},
		{"开周报普通周周五生成", "weekly", day(2026, 11, 6), time.Time{}, true, true},
		{"开周报本周已生成去重", "weekly", day(2026, 11, 6), at(2026, 11, 2, 19, 0), true, false},
		{"开周报生成后周末去重", "weekly", day(2026, 11, 7), at(2026, 11, 6, 19, 0), true, false},
		{"开周报补班周周五仍未到", "weekly", day(2026, 9, 18), at(2026, 9, 7, 19, 0), true, false},
		{"开周报补班周周日生成", "weekly", day(2026, 9, 20), at(2026, 9, 7, 19, 0), true, true},
		{"开周报国庆前周周三月末工作日", "weekly", day(2026, 9, 30), at(2026, 9, 18, 20, 10), true, true},
		{"开周报调休周周五未到周六", "weekly", day(2026, 10, 9), at(2026, 9, 25, 19, 0), true, false},
		{"开周报调休周周六生成", "weekly", day(2026, 10, 10), at(2026, 9, 25, 19, 0), true, true},
		{"开周报整周法定假不出", "weekly", day(2026, 2, 18), at(2026, 2, 14, 19, 0), true, false},
		{"开月报国庆1日未到首个工作日", "monthly", day(2026, 10, 1), at(2026, 9, 1, 20, 20), true, false},
		{"开月报10月8日首个工作日生成", "monthly", day(2026, 10, 8), at(2026, 9, 1, 20, 20), true, true},
		{"开月报本月已生成去重", "monthly", day(2026, 10, 9), at(2026, 10, 8, 19, 0), true, false},
		{"开月报1日即工作日直接生成", "monthly", day(2026, 9, 1), at(2026, 8, 3, 19, 0), true, true},
		{"开月报元旦1日顺延", "monthly", day(2026, 1, 1), at(2025, 12, 1, 20, 20), true, false},
		{"开月报元旦补班周日生成", "monthly", day(2026, 1, 4), at(2025, 12, 1, 20, 20), true, true},
		// ===== 关（skip_holidays=false）：每天/周五/月底 =====
		{"关日报周末也发", "daily", day(2026, 9, 19), time.Time{}, false, true},
		{"关日报法定假也发", "daily", day(2026, 10, 1), time.Time{}, false, true},
		{"关周报周四不发", "weekly", day(2026, 11, 5), time.Time{}, false, false},
		{"关周报周五生成", "weekly", day(2026, 11, 6), time.Time{}, false, true},
		{"关周报错过周五周六补发", "weekly", day(2026, 11, 7), at(2026, 10, 30, 19, 0), false, true},
		{"关周报本周已生成周日去重", "weekly", day(2026, 11, 8), at(2026, 11, 6, 19, 0), false, false},
		{"关周报节假日周五照发", "weekly", day(2026, 9, 25), at(2026, 9, 18, 19, 0), false, true},
		{"关月报月末前一天不发", "monthly", day(2026, 9, 29), time.Time{}, false, false},
		{"关月报9月30日月底生成", "monthly", day(2026, 9, 30), time.Time{}, false, true},
		{"关月报次月1日不跨月补", "monthly", day(2026, 10, 1), time.Time{}, false, false},
		{"关月报2月28日为月底", "monthly", day(2026, 2, 28), time.Time{}, false, true},
		{"关月报12月31日为月底", "monthly", day(2026, 12, 31), time.Time{}, false, true},
		{"未知类型不出", "hourly", day(2026, 9, 18), time.Time{}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reportDueForDay(tc.kind, tc.now, tc.genMarker, tc.skip); got != tc.want {
				t.Fatalf("reportDueForDay(%s, %s, %v, skip=%v) = %v, want %v",
					tc.kind, tc.now.Format("2006-01-02 15:04"), tc.genMarker, tc.skip, got, tc.want)
			}
		})
	}
}

// 关模式定时日报的请求阈值过滤（>reportDailyMinRequests 才生成）。
func TestBelowDailyMinRequests(t *testing.T) {
	cases := []struct {
		name     string
		typ      string
		trigger  ReportTrigger
		skip     bool
		requests int64
		want     bool
	}{
		{"关模式定时10条以下过滤", "daily", ReportTriggerScheduled, false, 10, true},
		{"关模式定时11条放行", "daily", ReportTriggerScheduled, false, 11, false},
		{"关模式定时0条过滤", "daily", ReportTriggerScheduled, false, 0, true},
		{"开模式不受阈值限制", "daily", ReportTriggerScheduled, true, 3, false},
		{"手动生成不受阈值限制", "daily", ReportTriggerManual, false, 2, false},
		{"周报不受阈值限制", "weekly", ReportTriggerScheduled, false, 3, false},
		{"月报不受阈值限制", "monthly", ReportTriggerScheduled, false, 3, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := belowDailyMinRequests(tc.typ, tc.trigger, tc.skip, tc.requests); got != tc.want {
				t.Fatalf("belowDailyMinRequests(%s, %s, skip=%v, %d) = %v, want %v",
					tc.typ, tc.trigger, tc.skip, tc.requests, got, tc.want)
			}
		})
	}
}
