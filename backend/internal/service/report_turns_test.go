package service

import (
	"strings"
	"testing"
	"time"
)

func TestExtractUserTurn(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "reminder前缀+中文指令+上下文",
			raw: "<system-reminder>\n# Plan Mode - System Reminder\n\nCRITICAL: Plan mode ACTIVE.\n</system-reminder>\n\n" +
				"我在/home/xxy/fs/work-report放了一个工作日报周报月报模版，整理一下有些没有的就删了\n\n" +
				"You are opencode, an interactive CLI tool that helps users with software engineering tasks.",
			want: "我在/home/xxy/fs/work-report放了一个工作日报周报月报模版，整理一下有些没有的就删了",
		},
		{
			name: "多个reminder块后跟指令",
			raw: "<system-reminder>mode changed</system-reminder>\n\n<system-reminder>another</system-reminder>\n\n继续优化日报格式\n\nThe user has asked to continue.",
			want: "继续优化日报格式",
		},
		{
			name: "纯reminder自动续跑（无用户输入）",
			raw:  "<system-reminder>\n# Plan Mode - System Reminder\n</system-reminder>\n\nYou are opencode, an interactive CLI tool that helps users.",
			want: "",
		},
		{
			name: "直接短指令",
			raw:  "修改",
			want: "修改",
		},
		{
			name: "英文短指令",
			raw:  "fix the login bug",
			want: "fix the login bug",
		},
		{
			name: "工具输出（You are 开头）",
			raw:  "You are Claude Code, Anthropic's official CLI for Claude.\n\nYou are an assistant for performing a web search tool use",
			want: "",
		},
		{
			name: "未闭合reminder块",
			raw:  "<system-reminder>never closed reminder content",
			want: "",
		},
		{
			name: "x-anthropic计费头噪音",
			raw:  "Perform a web search for the query: npx skills\n\nx-anthropic-billing-header: cc_version=2.1; cc_entrypoint=cli;\n\nYou are Claude Code",
			want: "Perform a web search for the query: npx skills",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractUserTurn(tc.raw, 500)
			if got != tc.want {
				t.Fatalf("extractUserTurn() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSampleWindows(t *testing.T) {
	noise := strings.Repeat("You are opencode system prompt tools and skills list. ", 200)
	conv := strings.Repeat("用户要求整理日报模板。助手开始读取文件并修改代码。", 120) // ~2280 runes
	text := noise + "</available_skills>\n\n" + conv

	ws := sampleWindows(text, 8, 700)
	if len(ws) != 8 {
		t.Fatalf("windows = %d, want 8", len(ws))
	}
	for _, w := range ws {
		if strings.Contains(w, "opencode system prompt") {
			t.Fatalf("window leaked system noise: %.80s", w)
		}
		if len([]rune(w)) > 701 {
			t.Fatalf("window too long: %d", len([]rune(w)))
		}
	}

	// 无锚标记：从头部开始
	ws2 := sampleWindows(conv, 4, 700)
	if len(ws2) != 4 {
		t.Fatalf("windows2 = %d, want 4", len(ws2))
	}

	// 短文本：单窗口返回
	ws3 := sampleWindows("短的对话内容", 8, 700)
	if len(ws3) != 1 || ws3[0] != "短的对话内容" {
		t.Fatalf("windows3 = %v", ws3)
	}
}

func TestExtractUserTurnTruncate(t *testing.T) {
	long := strings.Repeat("这是一个非常长的用户指令内容", 100) // 1100 runes
	got := extractUserTurn(long, 500)
	if n := len([]rune(got)); n > 501 { // 截断标记 …
		t.Fatalf("truncated length = %d, want <= 501", n)
	}
}

func TestCollectUserTurnsDedup(t *testing.T) {
	base := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	turns := []UserPromptSnippet{
		{Model: "glm-5.3", CreatedAt: base, Content: "<system-reminder>r1</system-reminder>\n\n整理日报模板并接入项目\n\nYou are opencode"},
		{Model: "glm-5.3", CreatedAt: base.Add(time.Minute), Content: "<system-reminder>r2</system-reminder>\n\n整理日报模板并接入项目\n\nYou are opencode"}, // 逐字重复（自动续跑重发）
		{Model: "glm-5.3", CreatedAt: base.Add(2 * time.Minute), Content: "<system-reminder>r3</system-reminder>\n\nYou are opencode, an interactive CLI tool"},  // 噪音丢弃
		{Model: "glm-5.3", CreatedAt: base.Add(3 * time.Minute), Content: "修复报告页空白问题"},
	}
	items := collectUserTurns(turns, 500)
	if len(items) != 2 {
		t.Fatalf("items = %v, want 2 deduped items", items)
	}
	if !strings.Contains(items[0], "整理日报模板并接入项目") || !strings.Contains(items[1], "修复报告页空白问题") {
		t.Fatalf("unexpected items: %v", items)
	}
	if !strings.Contains(items[0], "[10:00]") || !strings.Contains(items[1], "[10:03]") {
		t.Fatalf("items missing timestamps: %v", items)
	}
}
