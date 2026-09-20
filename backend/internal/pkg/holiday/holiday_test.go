package holiday

import (
	"testing"
	"time"
)

func day(y, m, d int) time.Time {
	return time.Date(y, time.Month(m), d, 12, 0, 0, 0, time.Local)
}

// 2026 年表数据与国办公告逐日核对：法定假日不放、调休补班上班、普通周末休。
func TestIsWorkday2026(t *testing.T) {
	cases := []struct {
		name string
		day  time.Time
		want bool
	}{
		{"元旦当天", day(2026, 1, 1), false},
		{"元旦连休周五", day(2026, 1, 2), false},
		{"元旦补班周日", day(2026, 1, 4), true},
		{"春节正月初一", day(2026, 2, 17), false},
		{"春节前补班周六", day(2026, 2, 14), true},
		{"春节后补班周六", day(2026, 2, 28), true},
		{"春节假期后首个工作日", day(2026, 2, 24), true},
		{"清明假期", day(2026, 4, 6), false},
		{"劳动节假期", day(2026, 5, 4), false},
		{"劳动节补班周六", day(2026, 5, 9), true},
		{"端午假期", day(2026, 6, 19), false},
		{"中秋补班周日", day(2026, 9, 20), true},
		{"中秋假期", day(2026, 9, 25), false},
		{"国庆假期", day(2026, 10, 1), false},
		{"国庆假期中周四", day(2026, 10, 8), true},
		{"国庆补班周六", day(2026, 10, 10), true},
		{"普通周六", day(2026, 10, 17), false},
		{"普通周一", day(2026, 11, 2), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsWorkday(tc.day); got != tc.want {
				t.Fatalf("IsWorkday(%s) = %v, want %v", tc.day.Format("2006-01-02"), got, tc.want)
			}
		})
	}
}

// 无表年份回退周末规则（2027 安排公布前）。
func TestIsWorkdayYearWithoutTable(t *testing.T) {
	if HasYearData(2027) {
		t.Fatal("2027 不应有内置表（安排尚未公布）")
	}
	if !IsWorkday(day(2027, 1, 4)) { // 周一
		t.Fatal("无表年份周一应为工作日")
	}
	if IsWorkday(day(2027, 1, 2)) { // 周六
		t.Fatal("无表年份周六应非工作日")
	}
}

func TestLastWorkdayOfWeek(t *testing.T) {
	cases := []struct {
		name string
		day  time.Time
		want string // 2006-01-02；空串=零值（整周无工作日）
	}{
		{"普通周", day(2026, 11, 4), "2026-11-06"},
		{"普通周周日", day(2026, 11, 15), "2026-11-13"},
		{"补班周日即本周最后工作日", day(2026, 9, 20), "2026-09-20"},
		{"国庆前一周周三截止", day(2026, 9, 30), "2026-09-30"},
		{"调休周周六收尾", day(2026, 10, 7), "2026-10-10"},
		{"春节整周无工作日", day(2026, 2, 18), ""},
		{"含补班周六的春节前一周", day(2026, 2, 11), "2026-02-14"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LastWorkdayOfWeek(tc.day)
			if tc.want == "" {
				if !got.IsZero() {
					t.Fatalf("want zero, got %s", got)
				}
				return
			}
			if got.Format("2006-01-02") != tc.want {
				t.Fatalf("got %s, want %s", got.Format("2006-01-02"), tc.want)
			}
		})
	}
}

func TestFirstWorkdayOfMonth(t *testing.T) {
	cases := []struct {
		name string
		day  time.Time
		want string
	}{
		{"普通月1日即工作日", day(2026, 9, 15), "2026-09-01"},
		{"国庆月顺延到10月8日", day(2026, 10, 20), "2026-10-08"},
		{"元旦月顺延到1月4日补班", day(2026, 1, 15), "2026-01-04"},
		{"春节月2月2日", day(2026, 2, 15), "2026-02-02"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FirstWorkdayOfMonth(tc.day)
			if got.Format("2006-01-02") != tc.want {
				t.Fatalf("got %s, want %s", got.Format("2006-01-02"), tc.want)
			}
		})
	}
}
