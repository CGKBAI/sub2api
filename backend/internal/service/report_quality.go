package service

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// 报告质量校验：LLM 生成后跑一遍确定性规则（纯正则/字符串，零额外成本）。
//
// 分级处理：
//   - 硬违规（结构性缺失、禁语、敏感信息）→ 带反馈重写一次；
//   - 软问题（条目数/条目过长/代码块/空泛措辞）→ 不重试，仅标记；
//   - 重写后仍有硬违规 → 照发并在报告上记录警告（quality_warnings），由作者
//     自行修正；唯一例外是敏感信息（重写后仍命中则整篇判失败，绝不落库/推送）。
//
// 为什么"照发"：漏报比质量差更伤信任——当天没日报会被当成系统故障。

const (
	reportItemMaxRunes = 60
	// 条目数区间：与 system prompt 的要求对齐（略放宽，避免因少一条就重写）。
	reportDailyMinItems   = 3
	reportDailyMaxItems   = 8
	reportWeeklyMinItems  = 3
	reportWeeklyMaxItems  = 10
	reportMonthlyMinItems = 3
	reportMonthlyMaxItems = 12
)

var (
	// reportSectionHeadingRe 匹配 "## 一、" 这类小节标题。
	reportSectionHeadingRe = regexp.MustCompile(`(?m)^##\s*([一二三四])、\s*(.*)$`)
	// reportItemLineRe 匹配编号条目。
	reportItemLineRe = regexp.MustCompile(`(?m)^\s*\d+[.、)．]\s*(.+)$`)
	// reportTitleLineRe 匹配正文首行的 H1 标题（标题由后端拼接，LLM 不应输出）。
	reportTitleLineRe = regexp.MustCompile(`(?m)^#\s+.*$`)
	// reportVaguePhraseRe 空泛措辞（仅提示，不重试）。
	reportVaguePhraseRe = regexp.MustCompile(`进行了|相关工作|进一步做好`)
)

// reportBannedPhrase 是"描述用了什么工具/模式/多少请求"类的禁语。
// 这些条目对管理者零信息量，是历史反馈里最集中的噪音。
type reportBannedPhrase struct {
	re   *regexp.Regexp
	hint string
}

var reportBannedPhrases = []reportBannedPhrase{
	{regexp.MustCompile(`\d+\s*次请求`), "不要写请求次数"},
	{regexp.MustCompile(`(?i)\bopencode\b`), "不要写客户端/工具名"},
	{regexp.MustCompile(`(?i)\bclaude\s*code\b`), "不要写客户端/工具名"},
	{regexp.MustCompile(`(?i)\bplan\s*mode\b`), "不要写模式名"},
	{regexp.MustCompile(`(?i)\bbuild\s*mode\b`), "不要写模式名"},
	{regexp.MustCompile(`模式之间切换|模式间切换`), "不要写模式切换过程"},
	{regexp.MustCompile(`(?i)使用.{0,6}工具`), "不要写使用了什么工具"},
	{regexp.MustCompile(`工具调用`), "不要写工具调用次数"},
}

var reportSensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{16,}`),
	regexp.MustCompile(`\b[A-Fa-f0-9]{32,}\b`),
	regexp.MustCompile(`\b1[3-9]\d{9}\b`),
	regexp.MustCompile(`(?i)\b[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}\b`),
}

// reportQualityResult 是一次质量校验的结论。
type reportQualityResult struct {
	// Hard 触发重写的硬违规（每条含修正提示）。
	Hard []string
	// Soft 仅记录、不重试的问题。
	Soft []string
	// Sensitive 命中敏感信息（重写后仍命中则判失败）。
	Sensitive bool
}

// checkReportQuality 校验 LLM 输出的报告正文（不含后端拼接的标题行）。
func checkReportQuality(content, reportType string, hasGoal bool) reportQualityResult {
	var res reportQualityResult
	body := strings.TrimSpace(content)
	if body == "" {
		res.Hard = append(res.Hard, "正文为空")
		return res
	}

	// ① 小节结构
	needThird := reportType == domain.ReportTypeDaily && hasGoal
	sections := map[string]bool{}
	for _, m := range reportSectionHeadingRe.FindAllStringSubmatch(body, -1) {
		sections[m[1]] = true
	}
	var unexpected []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		if reportSectionHeadingRe.MatchString(trimmed) {
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			res.Hard = append(res.Hard, "不要输出标题行（标题由系统拼接）")
			continue
		}
		unexpected = append(unexpected, strings.TrimSpace(strings.TrimLeft(trimmed, "#")))
	}
	if !sections["一"] || !sections["二"] {
		res.Hard = append(res.Hard, "必须包含「## 一、」和「## 二、」两个小节")
	}
	if needThird && !sections["三"] {
		res.Hard = append(res.Hard, "提供了近期目标，必须额外输出「## 三、近期目标计划」小节")
	}
	if !needThird && (sections["三"] || sections["四"]) {
		res.Hard = append(res.Hard, "未提供近期目标，不要输出第三个及以后的小节")
	}
	for _, name := range unexpected {
		res.Soft = append(res.Soft, fmt.Sprintf("出现非模板小节：%s", name))
	}

	// ② 禁语
	for _, banned := range reportBannedPhrases {
		if banned.re.MatchString(body) {
			res.Hard = append(res.Hard, banned.hint)
		}
	}

	// ③ 敏感信息（整篇不可用）
	for _, re := range reportSensitivePatterns {
		if re.MatchString(body) {
			res.Sensitive = true
			res.Hard = append(res.Hard, "正文包含疑似敏感信息（密钥/token/邮箱/手机号/哈希），必须删除")
			break
		}
	}

	// ④ 条目数与条目长度（软问题，仅标记）
	// 只统计第一节（核心工作）的条目数：计划/目标小节的条数本来就少，
	// 合并统计会把合法报告误判为"条目偏多"。
	sectionsBody := splitReportSections(body)
	coreItems := reportItemLineRe.FindAllStringSubmatch(sectionsBody["一"], -1)
	minItems, maxItems := reportItemRange(reportType)
	if len(coreItems) < minItems {
		res.Soft = append(res.Soft, fmt.Sprintf("核心工作条目偏少（%d 条，建议 %d-%d 条）", len(coreItems), minItems, maxItems))
	} else if len(coreItems) > maxItems {
		res.Soft = append(res.Soft, fmt.Sprintf("核心工作条目偏多（%d 条，建议 %d-%d 条）", len(coreItems), minItems, maxItems))
	}
	longest := 0
	for _, m := range reportItemLineRe.FindAllStringSubmatch(body, -1) {
		if n := len([]rune(strings.TrimSpace(m[1]))); n > longest {
			longest = n
		}
	}
	if longest > reportItemMaxRunes {
		res.Soft = append(res.Soft, fmt.Sprintf("存在过长条目（最长 %d 字，建议 ≤%d 字）", longest, reportItemMaxRunes))
	}

	// ⑤ 格式噪音（软问题）
	if strings.Contains(body, "```") {
		res.Soft = append(res.Soft, "正文包含代码块")
	}
	if strings.Contains(body, "| ---") || strings.Contains(body, "|---") {
		res.Soft = append(res.Soft, "正文包含表格")
	}
	if reportVaguePhraseRe.MatchString(body) {
		res.Soft = append(res.Soft, "存在空泛措辞（进行了/相关工作/进一步）")
	}

	return res
}

// splitReportSections 按「## 一、」类标题把正文切成小节，key 为中文序号。
func splitReportSections(body string) map[string]string {
	out := make(map[string]string, 4)
	current := ""
	var b strings.Builder
	flush := func() {
		if current != "" {
			out[current] += b.String()
		}
		b.Reset()
	}
	for _, line := range strings.Split(body, "\n") {
		if m := reportSectionHeadingRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			flush()
			current = m[1]
			continue
		}
		if current != "" {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	flush()
	return out
}

func reportItemRange(reportType string) (int, int) {
	switch reportType {
	case domain.ReportTypeWeekly:
		return reportWeeklyMinItems, reportWeeklyMaxItems
	case domain.ReportTypeMonthly:
		return reportMonthlyMinItems, reportMonthlyMaxItems
	default:
		return reportDailyMinItems, reportDailyMaxItems
	}
}

// reportQualityRetryNote 把硬违规回灌给模型，作为重写时的追加指令。
func reportQualityRetryNote(v reportQualityResult) string {
	if len(v.Hard) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【重要】你上一次的输出违反了以下硬性规则，必须逐条修正后重新输出完整正文（不要解释，不要输出标题）：\n")
	for _, issue := range v.Hard {
		b.WriteString("- ")
		b.WriteString(issue)
		b.WriteString("\n")
	}
	b.WriteString("严格按 system prompt 的小节结构与素材规则输出。\n")
	return b.String()
}

// reportQualityWarnings 把校验结论压成落库的一行文本（多条用「；」分隔）。
func reportQualityWarnings(v reportQualityResult) string {
	all := make([]string, 0, len(v.Hard)+len(v.Soft))
	all = append(all, v.Hard...)
	all = append(all, v.Soft...)
	if len(all) == 0 {
		return ""
	}
	out := strings.Join(uniqueStrings(all), "；")
	if n := []rune(out); len(n) > 500 {
		out = string(n[:500]) + "…"
	}
	return out
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// sanitizeReportBody 做无需重写的确定性清洗：去掉正文首部的 H1 标题行
// （标题由后端 reportTitle 拼接）与残留的 think 标签。
func sanitizeReportBody(content string) string {
	body := strings.TrimSpace(stripReasoningTrace(content))
	// 只裁掉正文开头的标题行，正文中间出现的 # 交给质量校验标记。
	for {
		trimmed := strings.TrimSpace(body)
		loc := reportTitleLineRe.FindStringIndex(trimmed)
		if loc == nil || loc[0] != 0 {
			break
		}
		body = trimmed[loc[1]:]
	}
	return strings.TrimSpace(body)
}
