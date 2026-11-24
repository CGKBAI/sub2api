package service

import (
	"strings"
	"testing"
)

// =========================
// 报告质量校验
// =========================

const validDailyBody = `## 一、今日核心工作
1. 完成登录模块重构并上线
2. 修复报表导出乱码问题
3. 搭建缓存压测环境

## 二、明日工作计划
1. 推进支付渠道对接`

func TestCheckReportQualityDailyHappyPath(t *testing.T) {
	res := checkReportQuality(validDailyBody, ReportTypeDaily, false, false)
	if len(res.Hard) != 0 || len(res.Soft) != 0 || res.Sensitive {
		t.Fatalf("valid body should pass clean, got hard=%v soft=%v sensitive=%v", res.Hard, res.Soft, res.Sensitive)
	}
}

func TestCheckReportQualityRequiresTwoSections(t *testing.T) {
	res := checkReportQuality("今天做了很多事情", ReportTypeDaily, false, false)
	if len(res.Hard) == 0 {
		t.Fatal("missing sections should be a hard violation")
	}
}

func TestCheckReportQualityFlagsToolTalk(t *testing.T) {
	body := `## 一、今日核心工作
1. 使用 opencode 工具完成开发（累计 180 次请求）
2. 在 plan mode 与 build mode 之间切换推进任务
3. 完成登录模块重构

## 二、明日工作计划
1. 推进支付对接`
	res := checkReportQuality(body, ReportTypeDaily, false, false)
	joined := strings.Join(res.Hard, "；")
	for _, want := range []string{"请求次数", "工具名", "模式名"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("hard violations %q should contain %q", joined, want)
		}
	}
}

func TestCheckReportQualityDetectsSensitiveContent(t *testing.T) {
	body := validDailyBody + "\n\n密钥 sk-abcdefgh12345678 配置在环境变量里"
	res := checkReportQuality(body, ReportTypeDaily, false, false)
	if !res.Sensitive {
		t.Fatalf("secret should be flagged sensitive, hard=%v", res.Hard)
	}

	withEmail := validDailyBody + "\n\n联系 zhangsan@example.com 处理"
	if res := checkReportQuality(withEmail, ReportTypeDaily, false, false); !res.Sensitive {
		t.Fatal("email should be flagged sensitive")
	}
}

func TestCheckReportQualityThirdSectionRules(t *testing.T) {
	// 有近期目标但没有第三节 → 硬违规
	res := checkReportQuality(validDailyBody, ReportTypeDaily, true, false)
	if !strings.Contains(strings.Join(res.Hard, "；"), "近期目标计划") {
		t.Fatalf("missing third section should be hard: %v", res.Hard)
	}

	// 没有近期目标却输出第三节 → 硬违规
	withThird := validDailyBody + "\n\n## 三、近期目标计划\n1. 启动新项目"
	res = checkReportQuality(withThird, ReportTypeDaily, false, false)
	if !strings.Contains(strings.Join(res.Hard, "；"), "第三个") {
		t.Fatalf("unexpected third section should be hard: %v", res.Hard)
	}

	// 有目标也有第三节 → 通过
	if res := checkReportQuality(withThird, ReportTypeDaily, true, false); len(res.Hard) != 0 {
		t.Fatalf("goal + third section should pass, got %v", res.Hard)
	}
}

func TestCheckReportQualitySoftIssues(t *testing.T) {
	body := `## 一、今日核心工作
1. 进行了相关工作并且进一步推进了相关事项的处理流程与整体方案的完善工作，同时配合上下游团队完成了多轮联调与回归验证以及上线前的各项准备工作
2. 完成登录模块重构
3. 修复报表导出乱码

## 二、明日工作计划
1. 推进支付渠道对接

` + "```go\nfmt.Println(1)\n```\n"
	res := checkReportQuality(body, ReportTypeDaily, false, false)
	if len(res.Hard) != 0 {
		t.Fatalf("soft issues should not be hard: %v", res.Hard)
	}
	joined := strings.Join(res.Soft, "；")
	for _, want := range []string{"过长条目", "代码块", "空泛措辞"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("soft issues %q should contain %q", joined, want)
		}
	}
}

func TestSanitizeReportBodyStripsLeadingTitle(t *testing.T) {
	raw := "# 工作日报（2026-09-15）- 谢翔宇\n\n" + validDailyBody
	got := sanitizeReportBody(raw)
	if strings.HasPrefix(got, "# 工作日报") {
		t.Fatalf("title should be stripped: %q", got)
	}
	if !strings.HasPrefix(got, "## 一、") {
		t.Fatalf("body should start with first section: %q", got)
	}
}

func TestReportQualityWarningsCompacts(t *testing.T) {
	res := reportQualityResult{Hard: []string{"a", "b"}, Soft: []string{"b", "c"}}
	got := reportQualityWarnings(res)
	if got != "a；b；c" {
		t.Fatalf("warnings = %q, want dedup join", got)
	}
	if reportQualityWarnings(reportQualityResult{}) != "" {
		t.Fatal("clean result should produce empty warnings")
	}
}
