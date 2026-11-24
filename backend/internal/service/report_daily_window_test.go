package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

type rollingFakeRepo struct {
	ReportRepository
	turns []UserPromptSnippet
}

func (f *rollingFakeRepo) GetReportGoal(context.Context, int64) (string, error) { return "", nil }

func (f *rollingFakeRepo) FetchUserTurns(context.Context, int64, time.Time, time.Time, int) ([]UserPromptSnippet, error) {
	return f.turns, nil
}

func (f *rollingFakeRepo) FetchPromptSnapshots(context.Context, int64, time.Time, time.Time, int) ([]UserPromptSnippet, error) {
	return nil, nil
}

func TestReportDailyWindowIsRolling24h(t *testing.T) {
	if err := timezone.Init("Asia/Shanghai"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = timezone.Init("UTC") })

	loc := timezone.Location()
	reportStart, dataStart, dataEnd := reportDailyWindow(time.Date(2026, 11, 24, 20, 0, 0, 0, loc), 20, 0)
	if got := reportStart.Format("2006-01-02 15:04"); got != "2026-11-24 00:00" {
		t.Fatalf("report start = %s", got)
	}
	if got := dataStart.Format("2006-01-02 15:04"); got != "2026-11-23 20:00" {
		t.Fatalf("data start = %s", got)
	}
	if got := dataEnd.Format("2006-01-02 15:04"); got != "2026-11-24 20:00" {
		t.Fatalf("data end = %s", got)
	}
}

func TestReportDailyWindowBoundaryIsHalfOpen(t *testing.T) {
	if err := timezone.Init("Asia/Shanghai"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = timezone.Init("UTC") })

	loc := timezone.Location()
	_, dataStart, dataEnd := reportDailyWindow(time.Date(2026, 11, 24, 12, 0, 0, 0, loc), 20, 0)

	justBefore := time.Date(2026, 11, 23, 19, 59, 59, 0, loc)
	atStart := time.Date(2026, 11, 23, 20, 0, 0, 0, loc)
	if !justBefore.Before(dataStart) {
		t.Fatal("19:59:59 the previous day should be outside the window")
	}
	if atStart.Before(dataStart) || !atStart.Before(dataEnd) {
		t.Fatal("20:00:00 the previous day should be inside the window")
	}
	justBeforeEnd := time.Date(2026, 11, 24, 19, 59, 59, 0, loc)
	atEnd := time.Date(2026, 11, 24, 20, 0, 0, 0, loc)
	if !justBeforeEnd.Before(dataEnd) {
		t.Fatal("19:59:59 should be inside the window")
	}
	if atEnd.Before(dataEnd) {
		t.Fatal("20:00:00 must not be inside the current window")
	}
}

func TestReportDailyWindowCatchUpStillCutsAtCutoff(t *testing.T) {
	if err := timezone.Init("Asia/Shanghai"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = timezone.Init("UTC") })

	loc := timezone.Location()
	// 20:00 定时失败、22:00 才补跑：窗口仍固定在 D-1 20:00 ~ D 20:00。
	_, dataStart, dataEnd := reportDailyWindow(time.Date(2026, 11, 24, 22, 0, 0, 0, loc), 20, 0)
	if got := dataStart.Format("2006-01-02 15:04"); got != "2026-11-23 20:00" {
		t.Fatalf("catch-up data start = %s", got)
	}
	if got := dataEnd.Format("2006-01-02 15:04"); got != "2026-11-24 20:00" {
		t.Fatalf("catch-up data end = %s", got)
	}
}

func TestReportDailyCutoffFallsBackWhenScheduleInvalid(t *testing.T) {
	cfg := &ReportLLMConfig{DailySchedule: "not-a-cron"}
	hour, minute, label := reportDailyCutoff(cfg)
	if hour != 19 || minute != 0 || label != "19:00" {
		t.Fatalf("cutoff = %02d:%02d (%s), want 19:00", hour, minute, label)
	}

	cfg.DailySchedule = "30 20 * * *"
	hour, minute, label = reportDailyCutoff(cfg)
	if hour != 20 || minute != 30 || label != "20:30" {
		t.Fatalf("cutoff = %02d:%02d (%s), want 20:30", hour, minute, label)
	}
}

func TestBuildSummaryPromptUsesRollingWindow(t *testing.T) {
	loc := time.Local
	reportStart := time.Date(2026, 11, 24, 0, 0, 0, 0, loc)
	dataStart := time.Date(2026, 11, 23, 20, 0, 0, 0, loc)
	dataEnd := time.Date(2026, 11, 24, 20, 0, 0, 0, loc)

	repo := &rollingFakeRepo{turns: []UserPromptSnippet{{
		Model: "m", CreatedAt: dataStart.Add(90 * time.Minute), CleanText: true,
		Content: "昨晚收尾导出任务空数据报错",
	}}}
	svc := NewReportService(repo, nil, nil)
	cfg := &ReportLLMConfig{Enabled: true, BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m",
		MaxPrompts: 30, PromptTruncateChars: 500, DailySchedule: "0 20 * * *"}
	normalizeReportLLMConfig(cfg)
	stats := ReportStats{Requests: 5}
	windows := reportWindows{
		Start: dataStart, End: dataEnd,
		ReportStart: reportStart, ReportEnd: dataEnd, CutoffLabel: "20:00",
	}

	prompt, _, err := svc.buildSummaryPrompt(context.Background(), cfg, ReportTypeDaily, windows, 9, &stats)
	if err != nil {
		t.Fatalf("buildSummaryPrompt: %v", err)
	}
	if !strings.Contains(prompt, "2026-11-23 20:00 ~ 2026-11-24 20:00") {
		t.Fatalf("prompt should use rolling 24h window:\n%s", prompt)
	}
	if !strings.Contains(prompt, "昨晚收尾导出任务空数据报错") {
		t.Fatalf("previous-evening material should stay in the main window:\n%s", prompt)
	}
}

func TestStripReportAnnex(t *testing.T) {
	body := "## 一、今日核心工作\n1. a\n\n## 二、明日工作计划\n1. b\n\n## 附：昨晚 20:00 后补充\n1. c"
	got := stripReportAnnex(body)
	if strings.Contains(got, "附：") || strings.Contains(got, "1. c") {
		t.Fatalf("annex should be stripped:\n%s", got)
	}
	if !strings.Contains(got, "## 二、明日工作计划") {
		t.Fatalf("main sections should be preserved:\n%s", got)
	}

	if unchanged := stripReportAnnex("## 一、今日核心工作\n1. a"); unchanged != "## 一、今日核心工作\n1. a" {
		t.Fatalf("body without annex should be unchanged: %q", unchanged)
	}
}
