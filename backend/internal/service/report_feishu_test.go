package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

func TestSplitCardTitle(t *testing.T) {
	title, rest := splitCardTitle("# 工作日报（2026-09-17）- 张三\n\n## 一、今日核心工作\n1. xxx")
	if title != "工作日报（2026-09-17）- 张三" {
		t.Fatalf("title = %q", title)
	}
	if !strings.HasPrefix(rest, "## 一、今日核心工作") {
		t.Fatalf("rest = %q", rest)
	}

	title2, rest2 := splitCardTitle("## 无标题正文\n内容")
	if title2 != "" || rest2 != "## 无标题正文\n内容" {
		t.Fatalf("title2 = %q, rest2 = %q", title2, rest2)
	}
}

func TestFeishuHeadingToBold(t *testing.T) {
	in := "## 一、今日核心工作\n1. 完成某功能\n\n### 小节\n正文"
	want := "**一、今日核心工作**\n1. 完成某功能\n\n**小节**\n正文"
	if got := feishuHeadingToBold(in); got != want {
		t.Fatalf("got = %q, want = %q", got, want)
	}
	if got := feishuHeadingToBold("1. 普通行不改"); got != "1. 普通行不改" {
		t.Fatalf("普通行被改: %q", got)
	}
}

func TestFeishuCardContentWithSummary(t *testing.T) {
	report := &Report{
		Type:        domain.ReportTypeDaily,
		PeriodStart: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC),
		Username:    "张三",
		AISummary:   "# 工作日报（2026-09-17）- 张三\n\n## 一、今日核心工作\n1. 修复登录问题",
	}
	title, body := feishuCardContent(report)
	if title != "工作日报（2026-09-17）- 张三" {
		t.Fatalf("title = %q", title)
	}
	if !strings.HasPrefix(body, "**一、今日核心工作**\n1. 修复登录问题") {
		t.Fatalf("body = %q", body)
	}
}

func TestFeishuCardContentFallbacks(t *testing.T) {
	// 无 AI 摘要：标题退化为 reportTitle，正文退化为统计行
	report := &Report{
		Type:        domain.ReportTypeWeekly,
		PeriodStart: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC),
		Username:    "",
		Stats:       ReportStats{Requests: 3, InputTokens: 10, OutputTokens: 20, TotalCost: 0.5},
	}
	title, body := feishuCardContent(report)
	if title != "工作周报（09.12-09.18）" {
		t.Fatalf("title = %q", title)
	}
	if !strings.Contains(body, "请求：3") {
		t.Fatalf("body = %q", body)
	}
}

func TestTruncateBytesRunesafe(t *testing.T) {
	if got := truncateBytesRunesafe("abc", 100); got != "abc" {
		t.Fatalf("短文本被改: %q", got)
	}
	long := strings.Repeat("工作", 100) // 400 字节
	got := truncateBytesRunesafe(long, 50)
	if !strings.HasSuffix(got, "（内容过长已截断）") {
		t.Fatalf("缺少截断标记: %q", got)
	}
	trimmed := strings.TrimSuffix(got, "\n…（内容过长已截断）")
	if len(trimmed) > 50 {
		t.Fatalf("超限: %d bytes", len(trimmed))
	}
	if !utf8ValidString(trimmed) {
		t.Fatalf("截断破坏了 rune: %q", trimmed)
	}
}

func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			// 粗略检测：replacement char 通常意味着解码失败
			return false
		}
	}
	return true
}

func TestFeishuSign(t *testing.T) {
	// 与官方算法一致：key = "timestamp\nsecret"，消息体为空，HMAC-SHA256 后 base64
	secret := "test-secret"
	ts := int64(1652254800)
	mac := hmac.New(sha256.New, []byte(fmt.Sprintf("%d\n%s", ts, secret)))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if got := feishuSign(secret, ts); got != want {
		t.Fatalf("got = %q, want = %q", got, want)
	}
	if feishuSign(secret, ts+1) == want {
		t.Fatal("不同时间戳签名应不同")
	}
}

func TestNormalizeReportLLMConfigFeishu(t *testing.T) {
	cfg := &ReportLLMConfig{
		FeishuWebhookURL: "  https://open.feishu.cn/open-apis/bot/v2/hook/xxx \n",
		FeishuSecret:     " sec ",
	}
	normalizeReportLLMConfig(cfg)
	if cfg.FeishuWebhookURL != "https://open.feishu.cn/open-apis/bot/v2/hook/xxx" {
		t.Fatalf("webhook url = %q", cfg.FeishuWebhookURL)
	}
	if cfg.FeishuSecret != "sec" {
		t.Fatalf("secret = %q", cfg.FeishuSecret)
	}
}
