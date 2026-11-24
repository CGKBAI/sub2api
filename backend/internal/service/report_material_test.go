package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

// =========================
// 日报素材：窗口采样 + 上下文预算
// =========================

func TestCollectConversationWindowsDedupesOverlappingSnapshots(t *testing.T) {
	// 同一 session 的 latest / mid 快照可能内容高度重合，完全相同的窗口应去重，
	// 避免同一段对话在素材里重复出现、白占预算。
	conv := strings.Repeat("用户要求整理日报模板，助手读取文件并修改代码。", 200)
	distinct := len(collectConversationWindows([]UserPromptSnippet{{Content: conv}}))
	collapsed := len(collectConversationWindows([]UserPromptSnippet{{Content: conv}, {Content: conv}}))
	if collapsed != distinct {
		t.Fatalf("identical snapshots: %d windows, want %d after dedupe", collapsed, distinct)
	}
}

func TestSampleWindowsAlignsToParagraphStart(t *testing.T) {
	para := strings.Repeat("段", 300)
	var b strings.Builder
	for i := 0; i < 20; i++ {
		b.WriteString(para)
		b.WriteString("\n\n")
	}
	windows := sampleWindows(b.String(), 6, 500)
	if len(windows) == 0 {
		t.Fatal("no windows sampled")
	}
	for _, w := range windows {
		if !strings.HasPrefix(w, "段") {
			t.Fatalf("window should start at a paragraph boundary, got %q", w)
		}
	}
}

func TestSampleWindowsPrefersTailRegion(t *testing.T) {
	// 前 70% 全是 A，后 30% 全是 B；尾部优先应让至少一半窗口落在 B 区。
	text := strings.Repeat("A", 7000) + strings.Repeat("B", 3000)
	windows := sampleWindows(text, 8, 500)
	if len(windows) == 0 {
		t.Fatal("no windows sampled")
	}
	fromTail := 0
	for _, w := range windows {
		if strings.Contains(w, "B") {
			fromTail++
		}
	}
	if fromTail < 4 {
		t.Fatalf("tail windows = %d, want >= 4 (tail-weighted sampling)", fromTail)
	}
}

func TestInsideFencedCode(t *testing.T) {
	runes := []rune("intro\n```go\ncode\n```\nafter")
	codePos := strings.Index(string(runes), "code")
	afterPos := strings.Index(string(runes), "after")
	if !insideFencedCode(runes, codePos) {
		t.Fatal("position inside fence should be detected")
	}
	if insideFencedCode(runes, afterPos) {
		t.Fatal("position after closing fence should not be detected")
	}
}

// materialFakeRepo 提供 buildSummaryPrompt 需要的素材方法。
type materialFakeRepo struct {
	ReportRepository
	turns []UserPromptSnippet
	snaps []UserPromptSnippet
	goal  string
}

func (f *materialFakeRepo) GetReportGoal(ctx context.Context, userID int64) (string, error) {
	return f.goal, nil
}

func (f *materialFakeRepo) FetchUserTurns(ctx context.Context, userID int64, start, end time.Time, limit int) ([]UserPromptSnippet, error) {
	return f.turns, nil
}

func (f *materialFakeRepo) FetchPromptSnapshots(ctx context.Context, userID int64, start, end time.Time, count int) ([]UserPromptSnippet, error) {
	return f.snaps, nil
}

func (f *materialFakeRepo) List(ctx context.Context, params pagination.PaginationParams, filters ReportListFilters) ([]Report, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

func newMaterialTurns(n int) []UserPromptSnippet {
	turns := make([]UserPromptSnippet, 0, n)
	base := time.Date(2026, 11, 23, 9, 0, 0, 0, time.Local)
	for i := 0; i < n; i++ {
		turns = append(turns, UserPromptSnippet{
			Model:     "test-model",
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
			Content:   fmt.Sprintf("问题 %d：调研缓存方案并给出结论", i),
		})
	}
	return turns
}

func TestCollectUserTurnsPrefersCleanText(t *testing.T) {
	turns := []UserPromptSnippet{
		// 写入时抽干的用户输入以 "#" 开头：旧的启发式会把 # 开头的判为噪音，必须保留
		{Model: "m", CreatedAt: time.Now(), Content: "# 整理日报模板并把结论落到文档", CleanText: true},
		// 未抽干的历史行仍走正则提取（此处的注入块会被剥掉）
		{Model: "m", CreatedAt: time.Now(), Content: "<system-reminder>x</system-reminder>修复报表导出"},
	}
	items := collectUserTurns(turns, 100)
	if len(items) != 2 {
		t.Fatalf("items = %d (%v), want 2", len(items), items)
	}
	if !strings.Contains(items[0], "整理日报模板") {
		t.Fatalf("clean text should be kept verbatim: %v", items[0])
	}
	if !strings.Contains(items[1], "修复报表导出") {
		t.Fatalf("legacy row should still be extracted: %v", items[1])
	}
}

func TestReportWindowNoiseClassifiesToolOutput(t *testing.T) {
	prose := strings.Repeat("今天修复了日报导出乱码问题，补充了单元测试，并和同事确认了发布计划。", 20)
	if score := reportWindowNoise(prose); score > 0.4 {
		t.Fatalf("prose noise = %.2f, want <= 0.4", score)
	}

	toolDump := "diff --git a/foo.go b/foo.go\n+++ b/foo.go\n@@ -1,3 +1,4 @@\n" +
		strings.Repeat("+func Foo() { return 1 }\n", 60)
	if score := reportWindowNoise(toolDump); score < 0.6 {
		t.Fatalf("diff noise = %.2f, want >= 0.6", score)
	}

	jsonDump := strings.Repeat(`{"id":1,"name":"x","deps":{"a":"^1.0.0","b":"^2.0.0"}},`, 40)
	if score := reportWindowNoise(jsonDump); score < 0.6 {
		t.Fatalf("json noise = %.2f, want >= 0.6", score)
	}
}

func TestSelectReportWindowsDropsNoisiestKeepingOrder(t *testing.T) {
	cands := []reportWindowCandidate{
		{text: "p1", noise: 0.1}, {text: "j1", noise: 0.9},
		{text: "p2", noise: 0.1}, {text: "j2", noise: 0.9},
		{text: "p3", noise: 0.1}, {text: "j3", noise: 0.9},
		{text: "p4", noise: 0.1}, {text: "j4", noise: 0.9},
	}
	got := selectReportWindows(cands, 5)
	want := []string{"p1", "p2", "p3", "p4", "j4"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("selected = %v, want %v", got, want)
	}

	// 噪音相同时优先淘汰更早的窗口（保住最近的工作）
	flat := []reportWindowCandidate{{text: "a", noise: 0.2}, {text: "b", noise: 0.2}, {text: "c", noise: 0.2}, {text: "d", noise: 0.2}}
	if got := selectReportWindows(flat, 2); strings.Join(got, ",") != "c,d" {
		t.Fatalf("tie-break selected = %v, want [c d]", got)
	}

	// 候选不足预算时全部保留
	if got := selectReportWindows(flat, 10); len(got) != 4 {
		t.Fatalf("under budget should keep all, got %d", len(got))
	}
}

func TestBuildSummaryPromptRespectsTotalBudget(t *testing.T) {
	repo := &materialFakeRepo{turns: newMaterialTurns(400)}
	svc := NewReportService(repo, nil, nil)
	cfg := &ReportLLMConfig{Enabled: true, BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m",
		MaxPrompts: 300, PromptTruncateChars: 800}
	normalizeReportLLMConfig(cfg)

	start := time.Date(2026, 11, 23, 0, 0, 0, 0, time.Local)
	stats := ReportStats{Requests: 400}
	windows := reportWindows{Start: start, End: start.Add(24 * time.Hour)}
	prompt, _, _, err := svc.buildSummaryPrompt(context.Background(), cfg, ReportTypeDaily, windows, 9, &stats)
	if err != nil {
		t.Fatalf("buildSummaryPrompt: %v", err)
	}
	if got := len([]rune(prompt)); got > reportUserPromptMaxRunes {
		t.Fatalf("prompt runes = %d, want <= %d", got, reportUserPromptMaxRunes)
	}
	// 裁剪从最早的条目开始丢，最新的工作必须保留。
	if !strings.Contains(prompt, "问题 399") {
		t.Fatal("latest turn should be preserved after budget trimming")
	}
	if stats.PromptCount != 400 {
		t.Fatalf("stats.PromptCount = %d, want 400", stats.PromptCount)
	}
}

func TestBuildSummaryPromptHonoursMaxPrompts(t *testing.T) {
	repo := &materialFakeRepo{turns: newMaterialTurns(50)}
	svc := NewReportService(repo, nil, nil)
	cfg := &ReportLLMConfig{Enabled: true, BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m",
		MaxPrompts: 10, PromptTruncateChars: 200}
	normalizeReportLLMConfig(cfg)

	start := time.Date(2026, 11, 23, 0, 0, 0, 0, time.Local)
	stats := ReportStats{Requests: 50}
	windows := reportWindows{Start: start, End: start.Add(24 * time.Hour)}
	prompt, _, _, err := svc.buildSummaryPrompt(context.Background(), cfg, ReportTypeDaily, windows, 9, &stats)
	if err != nil {
		t.Fatalf("buildSummaryPrompt: %v", err)
	}
	if got := strings.Count(prompt, "- ["); got != 10 {
		t.Fatalf("turn lines = %d, want 10 (max_prompts)", got)
	}
	if !strings.Contains(prompt, "问题 49") {
		t.Fatal("max_prompts should keep the most recent turns")
	}
}

func TestBuildSummaryPromptDropsSnapshotsBeforeTurnsWhenOverBudget(t *testing.T) {
	turns := newMaterialTurns(300)
	snaps := []UserPromptSnippet{{
		Model:     "test-model",
		CreatedAt: time.Date(2026, 11, 23, 12, 0, 0, 0, time.Local),
		Content:   strings.Repeat("对话片段", 20000),
	}}
	repo := &materialFakeRepo{turns: turns, snaps: snaps}
	svc := NewReportService(repo, nil, nil)
	cfg := &ReportLLMConfig{Enabled: true, BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m",
		MaxPrompts: 300, PromptTruncateChars: 800}
	normalizeReportLLMConfig(cfg)

	start := time.Date(2026, 11, 23, 0, 0, 0, 0, time.Local)
	stats := ReportStats{Requests: 300}
	windows := reportWindows{Start: start, End: start.Add(24 * time.Hour)}
	prompt, _, _, err := svc.buildSummaryPrompt(context.Background(), cfg, ReportTypeDaily, windows, 9, &stats)
	if err != nil {
		t.Fatalf("buildSummaryPrompt: %v", err)
	}
	if got := len([]rune(prompt)); got > reportUserPromptMaxRunes {
		t.Fatalf("prompt runes = %d, want <= %d", got, reportUserPromptMaxRunes)
	}
}
