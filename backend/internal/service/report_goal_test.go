package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

// =========================
// 日报近期目标：normalize + buildSummary 注入
// =========================

func TestNormalizeReportGoal(t *testing.T) {
	if got := normalizeReportGoal("  完成近期目标功能  "); got != "完成近期目标功能" {
		t.Fatalf("normalizeReportGoal trim: %q", got)
	}
	long := strings.Repeat("目", maxReportGoalRunes+10)
	if got := normalizeReportGoal(long); len([]rune(got)) != maxReportGoalRunes {
		t.Fatalf("normalizeReportGoal truncate: %d runes, want %d", len([]rune(got)), maxReportGoalRunes)
	}
	if got := normalizeReportGoal("   "); got != "" {
		t.Fatalf("normalizeReportGoal blank: %q", got)
	}
}

// reportGoalFakeRepo 只实现目标链路需要的方法，其余方法不会被调用。
type reportGoalFakeRepo struct {
	ReportRepository
	goal string
}

func (f *reportGoalFakeRepo) GetReportGoal(ctx context.Context, userID int64) (string, error) {
	return f.goal, nil
}

func (f *reportGoalFakeRepo) GetUsername(ctx context.Context, userID int64) (string, error) {
	return "测试用户", nil
}

func (f *reportGoalFakeRepo) FetchUserTurns(ctx context.Context, userID int64, start, end time.Time, limit int) ([]UserPromptSnippet, error) {
	return nil, nil
}

func (f *reportGoalFakeRepo) FetchPromptSnapshots(ctx context.Context, userID int64, start, end time.Time, count int) ([]UserPromptSnippet, error) {
	return nil, nil
}

func (f *reportGoalFakeRepo) List(ctx context.Context, params pagination.PaginationParams, filters ReportListFilters) ([]Report, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

// newReportGoalLLMStub 启动一个 OpenAI 兼容的 chat/completions 假服务，
// 捕获请求 messages 并返回固定摘要。
func newReportGoalLLMStub(t *testing.T, captured *[]reportLLMMessage) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read llm request: %v", err)
		}
		var req reportLLMChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("unmarshal llm request: %v", err)
		}
		*captured = req.Messages
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"## 一、今日核心工作\n1. 完成某功能\n\n## 二、明日工作计划\n1. 继续推进"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runGoalBuildSummary 用假 repo + 假 LLM 跑 buildSummary，返回捕获的 system/user prompt。
func runGoalBuildSummary(t *testing.T, reportType, goal string) (sys, user string) {
	t.Helper()
	var captured []reportLLMMessage
	srv := newReportGoalLLMStub(t, &captured)
	svc := NewReportService(&reportGoalFakeRepo{goal: goal}, nil)

	cfg := &ReportLLMConfig{Enabled: true, BaseURL: srv.URL, APIKey: "sk-test", Model: "test-model"}
	normalizeReportLLMConfig(cfg)

	start := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	stats := ReportStats{Requests: 5}
	if _, err := svc.buildSummary(context.Background(), cfg, reportType, start, end, 9, "测试用户", &stats); err != nil {
		t.Fatalf("buildSummary: %v", err)
	}
	for _, m := range captured {
		if m.Role == "system" {
			sys = m.Content
		} else {
			user = m.Content
		}
	}
	if sys == "" || user == "" {
		t.Fatalf("llm messages not captured: %+v", captured)
	}
	return sys, user
}

// 日报 + 目标非空：user prompt 注入「用户近期目标」小节，system prompt 含第三节指令。
func TestBuildSummaryDailyInjectsReportGoal(t *testing.T) {
	sys, user := runGoalBuildSummary(t, ReportTypeDaily, "完成 sub2api 日报近期目标功能开发并上线")
	if !strings.Contains(user, "用户近期目标") || !strings.Contains(user, "完成 sub2api 日报近期目标功能开发并上线") {
		t.Fatalf("daily user prompt missing goal section:\n%s", user)
	}
	if !strings.Contains(sys, "用户近期目标") || !strings.Contains(sys, "## 三、近期目标计划") {
		t.Fatalf("daily system prompt missing goal section instruction:\n%s", sys)
	}
}

// 日报 + 目标为空：不注入目标小节（system prompt 指令按「若素材包含」条件生效）。
func TestBuildSummaryDailyWithoutGoalOmitsSection(t *testing.T) {
	_, user := runGoalBuildSummary(t, ReportTypeDaily, "   ")
	if strings.Contains(user, "用户近期目标") {
		t.Fatalf("daily user prompt should not contain goal section when goal empty:\n%s", user)
	}
}

// 周报不读取目标（目标经日报摘要继承）：目标非空也不注入。
func TestBuildSummaryWeeklyIgnoresReportGoal(t *testing.T) {
	_, user := runGoalBuildSummary(t, ReportTypeWeekly, "完成 sub2api 日报近期目标功能开发并上线")
	if strings.Contains(user, "用户近期目标") {
		t.Fatalf("weekly user prompt should not contain goal section:\n%s", user)
	}
}
