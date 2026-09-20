package service

import (
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// 周报周期为正常自然周 [本周一 00:00, 下周一 00:00)：定时生成为本周最后一个
// 工作日 19:00（普通周即周五），覆盖周一~周日周期，周末工作靠之后手动重生成补入。
func TestReportPeriodWeeklyMondayStart(t *testing.T) {
	if err := timezone.Init("Asia/Shanghai"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = timezone.Init("UTC") })

	loc := timezone.Location()
	cases := []struct {
		name      string
		ref       time.Time
		wantStart time.Time
	}{
		{"周五晚定时生成", time.Date(2026, 9, 18, 20, 10, 0, 0, loc), time.Date(2026, 9, 14, 0, 0, 0, 0, loc)},
		{"周一任意时刻=当天起", time.Date(2026, 9, 14, 9, 0, 0, 0, loc), time.Date(2026, 9, 14, 0, 0, 0, 0, loc)},
		{"周中回退到本周一", time.Date(2026, 9, 16, 15, 0, 0, 0, loc), time.Date(2026, 9, 14, 0, 0, 0, 0, loc)},
		{"周日仍属本周", time.Date(2026, 9, 20, 21, 0, 0, 0, loc), time.Date(2026, 9, 14, 0, 0, 0, 0, loc)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end, err := ReportPeriod(ReportTypeWeekly, tc.ref)
			if err != nil {
				t.Fatalf("ReportPeriod: %v", err)
			}
			if !start.Equal(tc.wantStart) {
				t.Fatalf("start = %s, want %s", start, tc.wantStart)
			}
			wantEnd := tc.wantStart.Add(7 * 24 * time.Hour)
			if !end.Equal(wantEnd) {
				t.Fatalf("end = %s, want %s", end, wantEnd)
			}
		})
	}
}

// 月报语义为「ref 所在自然月」：选 8 月任意日期 → 8 月月报；
// 调度器每月 1 日传上月 1 日 → 生成上月。
func TestReportPeriodMonthlyRefMonth(t *testing.T) {
	if err := timezone.Init("Asia/Shanghai"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = timezone.Init("UTC") })

	loc := timezone.Location()
	cases := []struct {
		name      string
		ref       time.Time
		wantStart time.Time
	}{
		{"月选择器传 1 日", time.Date(2026, 8, 1, 0, 0, 0, 0, loc), time.Date(2026, 8, 1, 0, 0, 0, 0, loc)},
		{"月中任意日期", time.Date(2026, 8, 17, 15, 30, 0, 0, loc), time.Date(2026, 8, 1, 0, 0, 0, 0, loc)},
		{"调度器 9/1 传 8/1 生成 8 月", time.Date(2026, 8, 1, 20, 20, 0, 0, loc), time.Date(2026, 8, 1, 0, 0, 0, 0, loc)},
		{"跨年 12 月", time.Date(2026, 12, 25, 12, 0, 0, 0, loc), time.Date(2026, 12, 1, 0, 0, 0, 0, loc)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end, err := ReportPeriod(ReportTypeMonthly, tc.ref)
			if err != nil {
				t.Fatalf("ReportPeriod: %v", err)
			}
			if !start.Equal(tc.wantStart) {
				t.Fatalf("start = %s, want %s", start, tc.wantStart)
			}
			wantEnd := tc.wantStart.AddDate(0, 1, 0)
			if !end.Equal(wantEnd) {
				t.Fatalf("end = %s, want %s", end, wantEnd)
			}
		})
	}
}

// 日报周期不受本次改动影响（回归）。
func TestReportPeriodDailyUnchanged(t *testing.T) {
	if err := timezone.Init("Asia/Shanghai"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = timezone.Init("UTC") })

	loc := timezone.Location()
	start, end, err := ReportPeriod(ReportTypeDaily, time.Date(2026, 9, 16, 15, 0, 0, 0, loc))
	if err != nil {
		t.Fatalf("daily: %v", err)
	}
	if !start.Equal(time.Date(2026, 9, 16, 0, 0, 0, 0, loc)) || !end.Equal(time.Date(2026, 9, 17, 0, 0, 0, 0, loc)) {
		t.Fatalf("daily period = [%s, %s)", start, end)
	}
}

// 窗口预算按 session 数均分：总预算 32、单 session 上限 8、最少 2 窗（sampleWindows 防除零下限）。
func TestCollectConversationWindowsBudget(t *testing.T) {
	conv := strings.Repeat("用户要求整理日报模板，助手读取文件并修改代码。", 100) // 2200 runes > 700
	mk := func(n int) []UserPromptSnippet {
		snaps := make([]UserPromptSnippet, 0, n)
		for i := 0; i < n; i++ {
			snaps = append(snaps, UserPromptSnippet{CreatedAt: time.Unix(int64(i), 0), Content: conv})
		}
		return snaps
	}

	if got := len(collectConversationWindows(mk(8))); got != 32 { // 32/8 = 4 窗/session
		t.Fatalf("8 sessions: %d windows, want 32", got)
	}
	if got := len(collectConversationWindows(mk(2))); got != 16 { // 32/2=16 → 封顶 8 窗/session
		t.Fatalf("2 sessions: %d windows, want 16", got)
	}
	if got := len(collectConversationWindows(mk(1))); got != 8 { // 封顶 8
		t.Fatalf("1 session: %d windows, want 8", got)
	}
	if got := len(collectConversationWindows(mk(40))); got != 80 { // 32/40=0 → 保底；sampleWindows 最少 2 窗（防除零）
		t.Fatalf("40 sessions: %d windows, want 80", got)
	}
	if got := collectConversationWindows(nil); got != nil {
		t.Fatalf("nil snaps: got %v, want nil", got)
	}
}
