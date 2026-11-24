package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

type annexFakeRepo struct {
	ReportRepository
	annexStart time.Time
	mainTurns  []UserPromptSnippet
	annexTurns []UserPromptSnippet
}

func (f *annexFakeRepo) GetReportGoal(context.Context, int64) (string, error) { return "", nil }

func (f *annexFakeRepo) FetchUserTurns(_ context.Context, _ int64, start, _ time.Time, _ int) ([]UserPromptSnippet, error) {
	if start.Equal(f.annexStart) {
		return f.annexTurns, nil
	}
	return f.mainTurns, nil
}

func (f *annexFakeRepo) FetchPromptSnapshots(context.Context, int64, time.Time, time.Time, int) ([]UserPromptSnippet, error) {
	return nil, nil
}

func TestReportDailyWindowUsesConfiguredCutoff(t *testing.T) {
	if err := timezone.Init("Asia/Shanghai"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = timezone.Init("UTC") })

	loc := timezone.Location()
	ref := time.Date(2026, 11, 24, 20, 0, 0, 0, loc)
	start, end, annexStart, annexEnd := reportDailyWindow(ref, 20, 0)
	if got := start.Format("2006-01-02 15:04"); got != "2026-11-24 00:00" {
		t.Fatalf("start = %s", got)
	}
	if got := end.Format("2006-01-02 15:04"); got != "2026-11-24 20:00" {
		t.Fatalf("end = %s", got)
	}
	if got := annexStart.Format("2006-01-02 15:04"); got != "2026-11-23 20:00" {
		t.Fatalf("annexStart = %s", got)
	}
	if got := annexEnd.Format("2006-01-02 15:04"); got != "2026-11-24 00:00" {
		t.Fatalf("annexEnd = %s", got)
	}
}

func TestReportDailyWindowBoundaryIsHalfOpen(t *testing.T) {
	if err := timezone.Init("Asia/Shanghai"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = timezone.Init("UTC") })

	loc := timezone.Location()
	start, end, annexStart, annexEnd := reportDailyWindow(time.Date(2026, 11, 24, 12, 0, 0, 0, loc), 20, 0)
	if !annexEnd.Equal(start) {
		t.Fatalf("annex end %s should equal main start %s", annexEnd, start)
	}
	if got := annexStart.Format("2006-01-02 15:04"); got != "2026-11-23 20:00" {
		t.Fatalf("annex start = %s, want 2026-11-23 20:00", got)
	}
	beforeCutoff := time.Date(2026, 11, 24, 19, 59, 59, 0, loc)
	atCutoff := time.Date(2026, 11, 24, 20, 0, 0, 0, loc)
	if !beforeCutoff.Before(end) {
		t.Fatal("19:59:59 should belong to the main window")
	}
	if atCutoff.Before(end) {
		t.Fatal("20:00:00 must not belong to the main window")
	}
	latePrev := time.Date(2026, 11, 23, 23, 59, 59, 0, loc)
	if latePrev.Before(annexStart) || !latePrev.Before(annexEnd) {
		t.Fatal("previous day 23:59:59 should belong to the annex window")
	}
	midnight := time.Date(2026, 11, 24, 0, 0, 0, 0, loc)
	if midnight.Before(annexEnd) {
		t.Fatal("midnight must not belong to the annex window")
	}
}

func TestReportDailyWindowCatchUpStillCutsAtCutoff(t *testing.T) {
	if err := timezone.Init("Asia/Shanghai"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = timezone.Init("UTC") })

	loc := timezone.Location()
	// 20:00 定时失败、22:00 才补跑：正文仍只到 20:00，22:00 前的工作归次日附节。
	_, end, _, _ := reportDailyWindow(time.Date(2026, 11, 24, 22, 0, 0, 0, loc), 20, 0)
	if got := end.Format("2006-01-02 15:04"); got != "2026-11-24 20:00" {
		t.Fatalf("catch-up main end = %s, want 20:00", got)
	}
}

func TestFeishuAnnexHeadingBecomesBold(t *testing.T) {
	in := "## 附：昨晚 20:00 后补充\n1. 完成导出任务修复"
	want := "**附：昨晚 20:00 后补充**\n1. 完成导出任务修复"
	if got := feishuHeadingToBold(in); got != want {
		t.Fatalf("got = %q, want = %q", got, want)
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

func TestBuildSummaryPromptIncludesAnnexMaterial(t *testing.T) {
	loc := time.Local
	mainStart := time.Date(2026, 11, 24, 0, 0, 0, 0, loc)
	mainEnd := time.Date(2026, 11, 24, 20, 0, 0, 0, loc)
	annexStart := time.Date(2026, 11, 23, 20, 0, 0, 0, loc)
	annexEnd := mainStart

	repo := &annexFakeRepo{
		annexStart: annexStart,
		mainTurns: []UserPromptSnippet{{
			Model: "m", CreatedAt: mainStart.Add(10 * time.Hour), CleanText: true,
			Content: "完成今天的附录逻辑改造",
		}},
		annexTurns: []UserPromptSnippet{{
			Model: "m", CreatedAt: annexStart.Add(90 * time.Minute), CleanText: true,
			Content: "昨晚收尾导出任务空数据报错",
		}},
	}
	svc := NewReportService(repo, nil, nil)
	cfg := &ReportLLMConfig{Enabled: true, BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m",
		MaxPrompts: 30, PromptTruncateChars: 500, DailySchedule: "0 20 * * *"}
	normalizeReportLLMConfig(cfg)
	stats := ReportStats{Requests: 5, AnnexRequests: 3}
	windows := reportWindows{
		Start: mainStart, End: mainEnd,
		AnnexStart: annexStart, AnnexEnd: annexEnd, AnnexLabel: "20:00",
	}

	prompt, _, hasAnnex, err := svc.buildSummaryPrompt(context.Background(), cfg, ReportTypeDaily, windows, 9, &stats)
	if err != nil {
		t.Fatalf("buildSummaryPrompt: %v", err)
	}
	if !hasAnnex {
		t.Fatal("annex material should be detected")
	}
	if !strings.Contains(prompt, "完成今天的附录逻辑改造") {
		t.Fatalf("main material missing:\n%s", prompt)
	}
	if !strings.Contains(prompt, "前一日 20:00 后素材") || !strings.Contains(prompt, "昨晚收尾导出任务空数据报错") {
		t.Fatalf("annex material missing:\n%s", prompt)
	}
}

func TestCheckReportQualityAnnexRules(t *testing.T) {
	withAnnex := validDailyBody + "\n\n## 附：昨晚 20:00 后补充\n1. 完成导出任务空数据修复\n2. 补齐回填脚本并验证"

	if res := checkReportQuality(withAnnex, ReportTypeDaily, false, true); len(res.Hard) != 0 {
		t.Fatalf("annex body should pass, got %v", res.Hard)
	}

	// 有素材但没输出附节 → 硬违规
	res := checkReportQuality(validDailyBody, ReportTypeDaily, false, true)
	if !strings.Contains(strings.Join(res.Hard, "；"), "附：昨晚") {
		t.Fatalf("missing annex should be hard: %v", res.Hard)
	}

	// 无素材却输出附节 → 硬违规
	res = checkReportQuality(withAnnex, ReportTypeDaily, false, false)
	if !strings.Contains(strings.Join(res.Hard, "；"), "不要输出附节") {
		t.Fatalf("unexpected annex should be hard: %v", res.Hard)
	}

	// 附节条目超过 3 条 → 软问题
	many := validDailyBody + "\n\n## 附：昨晚 20:00 后补充\n1. a\n2. b\n3. c\n4. d"
	res = checkReportQuality(many, ReportTypeDaily, false, true)
	if !strings.Contains(strings.Join(res.Soft, "；"), "附节条目数") {
		t.Fatalf("annex item count should be soft: %v", res.Soft)
	}
}

func TestNormalizeReportAnnex(t *testing.T) {
	body := "## 一、今日核心工作\n1. a\n\n## 二、明日工作计划\n1. b\n\n**附：昨晚 20:00 后补充**\n1. c"
	got := normalizeReportAnnex(body, true, "## 附：昨晚 20:00 后补充")
	if !strings.Contains(got, "## 附：昨晚 20:00 后补充") {
		t.Fatalf("annex heading should be normalized:\n%s", got)
	}
	if strings.Contains(got, "**附：") {
		t.Fatalf("bold annex heading should be replaced:\n%s", got)
	}

	stripped := normalizeReportAnnex(body, false, "")
	if strings.Contains(stripped, "附：") {
		t.Fatalf("annex should be stripped without material:\n%s", stripped)
	}
}
