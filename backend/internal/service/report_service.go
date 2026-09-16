package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

const reportLLMTimeout = 120 * time.Second

// ReportService 日报/周报核心服务。
type ReportService struct {
	reportRepo  ReportRepository
	settingRepo SettingRepository
	llm         *reportLLMClient
}

// NewReportService 构造报告服务。
func NewReportService(reportRepo ReportRepository, settingRepo SettingRepository) *ReportService {
	return &ReportService{
		reportRepo:  reportRepo,
		settingRepo: settingRepo,
		llm: &reportLLMClient{
			httpClient: &http.Client{Timeout: reportLLMTimeout + 10*time.Second},
		},
	}
}

// ReportPeriod 根据报告类型与基准时间计算周期边界 [start, end)。
// 月报固定覆盖 ref 的上一个自然月：每月 1 日定时生成上月，手动生成语义一致
//（如 ref 在 9 月任意一天 → 生成 8 月月报，8 月月报用 9 月任意日期可重试）。
func ReportPeriod(reportType string, ref time.Time) (time.Time, time.Time, error) {
	switch reportType {
	case domain.ReportTypeDaily:
		start := timezone.StartOfDay(ref)
		return start, start.Add(24 * time.Hour), nil
	case domain.ReportTypeWeekly:
		start := timezone.StartOfWeek(ref)
		return start, start.Add(7 * 24 * time.Hour), nil
	case domain.ReportTypeMonthly:
		end := timezone.StartOfMonth(ref)
		return end.AddDate(0, -1, 0), end, nil
	default:
		return time.Time{}, time.Time{}, domain.ErrReportInvalidType
	}
}

// GenerateReport 为单个用户生成指定周期（日/周/月）的报告。
// 已存在同周期报告时覆盖更新（手动重试语义）。
func (s *ReportService) GenerateReport(ctx context.Context, userID int64, reportType string, ref time.Time) (*Report, error) {
	if s == nil || s.reportRepo == nil {
		return nil, errors.New("report repository not initialized")
	}
	if userID <= 0 {
		return nil, errors.New("invalid user id")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	start, end, err := ReportPeriod(reportType, ref)
	if err != nil {
		return nil, err
	}

	stats, err := s.reportRepo.AggregateUserUsage(ctx, userID, start, end)
	if err != nil {
		return nil, err
	}
	if stats.Requests == 0 {
		return nil, ErrReportGenerateUserNotFound
	}
	stats.Models = orEmptyCounts(stats.Models)
	stats.ModelTokens = orEmptyCounts(stats.ModelTokens)
	stats.Hourly = orEmptyHourly(stats.Hourly)

	report := &Report{
		UserID:      userID,
		Type:        reportType,
		PeriodStart: start,
		PeriodEnd:   end,
		Stats:       stats,
		Status:      domain.ReportStatusDone,
	}

	cfg, err := s.GetReportConfig(ctx)
	if err != nil {
		return nil, err
	}

	// 姓名进报告标题（如「工作日报（2026-09-15）- 谢翔宇」）
	username, err := s.reportRepo.GetUsername(ctx, userID)
	if err != nil {
		return nil, err
	}
	report.Username = username

	summary, llmErr := s.buildSummary(ctx, cfg, reportType, start, end, userID, username, &stats)
	if llmErr != nil {
		if errors.Is(llmErr, domain.ErrReportLLMNotConfigured) {
			// LLM 未配置：产出纯统计报告，不算失败
			summary = ""
		} else {
			report.Status = domain.ReportStatusFailed
			report.Error = truncateReportError(llmErr.Error())
			report.AISummary = ""
			report.Stats = stats
			return s.persist(ctx, report)
		}
	}
	report.AISummary = summary
	report.Error = ""
	report.Stats = stats
	return s.persist(ctx, report)
}

// GenerateForAllUsers 为周期内所有活跃用户生成报告（scheduler / 管理端手动触发用）。
// 返回成功生成的数量与首个错误。
func (s *ReportService) GenerateForAllUsers(ctx context.Context, reportType string, ref time.Time) (int, error) {
	if s == nil || s.reportRepo == nil {
		return 0, errors.New("report repository not initialized")
	}
	start, end, err := ReportPeriod(reportType, ref)
	if err != nil {
		return 0, err
	}

	userIDs, err := s.reportRepo.ListActiveUserIDs(ctx, start, end)
	if err != nil {
		return 0, err
	}

	generated := 0
	var firstErr error
	for _, uid := range userIDs {
		_, gErr := s.GenerateReport(ctx, uid, reportType, ref)
		if gErr != nil {
			if errors.Is(gErr, ErrReportGenerateUserNotFound) {
				continue
			}
			logger.LegacyPrintf("service.report",
				"[Report] generate %s report failed for user %d: %v", reportType, uid, gErr)
			if firstErr == nil {
				firstErr = gErr
			}
			continue
		}
		generated++
	}
	return generated, firstErr
}

// GetReport 按 ID 获取报告。
func (s *ReportService) GetReport(ctx context.Context, id int64) (*Report, error) {
	if s == nil || s.reportRepo == nil {
		return nil, errors.New("report repository not initialized")
	}
	return s.reportRepo.GetByID(ctx, id)
}

// ListReports 管理端列表（可按人过滤）。
func (s *ReportService) ListReports(ctx context.Context, params pagination.PaginationParams, filters ReportListFilters) ([]Report, *pagination.PaginationResult, error) {
	if s == nil || s.reportRepo == nil {
		return nil, nil, errors.New("report repository not initialized")
	}
	if filters.Type != "" && filters.Type != domain.ReportTypeDaily && filters.Type != domain.ReportTypeWeekly && filters.Type != domain.ReportTypeMonthly {
		return nil, nil, domain.ErrReportInvalidType
	}
	return s.reportRepo.List(ctx, params, filters)
}

// ListUserReports 普通用户列表：强制只看自己。
func (s *ReportService) ListUserReports(ctx context.Context, userID int64, params pagination.PaginationParams, filters ReportListFilters) ([]Report, *pagination.PaginationResult, error) {
	if userID <= 0 {
		return nil, nil, errors.New("invalid user id")
	}
	filters.UserID = userID
	return s.ListReports(ctx, params, filters)
}

// persist 创建或覆盖更新（按 user_id+type+period_start）。
func (s *ReportService) persist(ctx context.Context, r *Report) (*Report, error) {
	existing, err := s.reportRepo.GetByUserPeriod(ctx, r.UserID, r.Type, r.PeriodStart)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		r.ID = existing.ID
		r.CreatedAt = existing.CreatedAt
		r.Username = existing.Username
		if err := s.reportRepo.Update(ctx, r); err != nil {
			return nil, err
		}
		return r, nil
	}
	if err := s.reportRepo.Create(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

// buildSummary 组装 LLM 上下文并调用模型。
// 周报聚合本周日报摘要；月报优先聚合当月周报摘要、降级聚合当月日报摘要；
// 聚合不到子报告时与日报一致：直接拉取周期内 prompt 片段。
// LLM 只输出正文两个小节，标题（含姓名）由后端拼接，保证姓名/日期准确。
func (s *ReportService) buildSummary(
	ctx context.Context,
	cfg *ReportLLMConfig,
	reportType string,
	start, end time.Time,
	userID int64,
	username string,
	stats *ReportStats,
) (string, error) {
	systemPrompt := reportSystemPrompt(reportType)

	var userPrompt strings.Builder
	userPrompt.WriteString(fmt.Sprintf("周期：%s ~ %s\n", start.Format("2006-01-02 15:04"), end.Format("2006-01-02 15:04")))

	aggregated := false
	switch reportType {
	case domain.ReportTypeWeekly:
		aggregated = s.appendSubReports(ctx, &userPrompt, domain.ReportTypeDaily, start, end, 7, userID)
	case domain.ReportTypeMonthly:
		aggregated = s.appendSubReports(ctx, &userPrompt, domain.ReportTypeWeekly, start, end, 6, userID)
		if !aggregated {
			aggregated = s.appendSubReports(ctx, &userPrompt, domain.ReportTypeDaily, start, end, 31, userID)
		}
	}

	if !aggregated && (cfg.Enabled || (cfg.BaseURL != "" && cfg.APIKey != "" && cfg.Model != "")) {
		prompts, err := s.reportRepo.FetchUserPrompts(ctx, userID, start, end, cfg.MaxPrompts)
		if err == nil && len(prompts) > 0 {
			stats.PromptCount = int64(len(prompts))
			userPrompt.WriteString(fmt.Sprintf("\n### Prompt 片段（全天均匀抽样 %d 条，每条截断 %d 字符，按时间正序）\n", len(prompts), cfg.PromptTruncateChars))
			for _, p := range prompts {
				content := truncateRunes(p.Content, cfg.PromptTruncateChars)
				if strings.TrimSpace(content) == "" {
					continue
				}
				userPrompt.WriteString(fmt.Sprintf("- [%s][%s] %s\n", p.CreatedAt.Format("15:04"), p.Model, content))
			}
		}
	}

	userPrompt.WriteString("\n### 统计数字\n")
	userPrompt.WriteString(fmt.Sprintf("- 请求次数：%d\n", stats.Requests))
	userPrompt.WriteString(fmt.Sprintf("- 输入/输出 tokens：%d / %d\n", stats.InputTokens, stats.OutputTokens))
	userPrompt.WriteString(fmt.Sprintf("- 费用：%.4f\n", stats.TotalCost))
	userPrompt.WriteString("- 模型分布：" + formatModelCounts(stats.Models) + "\n")
	userPrompt.WriteString("- 活跃时段：" + formatHourly(stats.Hourly) + "\n")

	if !(cfg.Enabled || (cfg.BaseURL != "" && cfg.APIKey != "" && cfg.Model != "")) {
		return "", domain.ErrReportLLMNotConfigured
	}

	llmCtx, cancel := context.WithTimeout(ctx, reportLLMTimeout)
	defer cancel()
	content, err := s.llm.ChatComplete(llmCtx, cfg, systemPrompt, userPrompt.String())
	if err != nil {
		return "", err
	}
	return reportTitle(reportType, start, username) + "\n\n" + content, nil
}

// reportTitle 构造报告标题行（姓名来自 users.username，不依赖 LLM）。
// 例：# 工作日报（2026-09-15）- 谢翔宇
func reportTitle(reportType string, start time.Time, username string) string {
	suffix := ""
	if name := strings.TrimSpace(username); name != "" {
		suffix = "- " + name
	}
	switch reportType {
	case domain.ReportTypeWeekly:
		lastDay := start.AddDate(0, 0, 6)
		return fmt.Sprintf("# 工作周报（%s-%s）%s", start.Format("01.02"), lastDay.Format("01.02"), suffix)
	case domain.ReportTypeMonthly:
		return fmt.Sprintf("# 工作月报（%d年%d月）%s", start.Year(), int(start.Month()), suffix)
	default:
		return fmt.Sprintf("# 工作日报（%s）%s", start.Format("2006-01-02"), suffix)
	}
}

// appendSubReports 把周期内已生成的子报告（日报/周报）AI 摘要按时间正序追加进 LLM
// 上下文。没有任何可用摘要时返回 false（调用方降级取 prompt 片段）。
func (s *ReportService) appendSubReports(
	ctx context.Context,
	w *strings.Builder,
	subType string,
	start, end time.Time,
	pageSize int,
	userID int64,
) bool {
	items, _, err := s.reportRepo.List(ctx, pagination.PaginationParams{Page: 1, PageSize: pageSize}, ReportListFilters{
		Type:      subType,
		UserID:    userID,
		StartDate: start,
		EndDate:   end,
	})
	if err != nil {
		return false
	}
	label := "日报"
	if subType == domain.ReportTypeWeekly {
		label = "周报"
	}
	found := false
	// repo 按 period_start 倒序返回，倒序遍历得到时间正序
	for i := len(items) - 1; i >= 0; i-- {
		d := &items[i]
		if d.AISummary == "" {
			continue
		}
		found = true
		w.WriteString(fmt.Sprintf("\n### %s %s摘要\n%s\n", d.PeriodStart.Format("2006-01-02"), label, d.AISummary))
	}
	return found
}

// reportSystemPrompt 按报告类型构造 system prompt，输出格式对齐团队日报/周报/月报
// 模板（标题 + 两个小节：核心工作 + 计划，平铺编号条目，无分类/优先级标注）。
// 标题由后端拼接，LLM 只输出两个小节正文。
func reportSystemPrompt(reportType string) string {
	var b strings.Builder
	b.WriteString("你是团队 AI 网关的工作报告助手，根据用户在网关上的请求统计与 prompt 片段，推断该用户本周期做了什么，用简体中文输出 Markdown。\n\n")

	b.WriteString("素材规则：\n")
	b.WriteString("1. prompt 片段是「用户最新消息 + 历史上下文」的拍平文本：优先依据开头的用户真实输入判断工作意图；其中大段重复出现的系统提示词、工具注入的规则/环境说明属于噪音，仅用于识别所用工具与场景，不要当作工作内容复述。\n")
	b.WriteString("2. 同类工作必须合并：多次调试/提交同一功能合并为一条（如「完成 XX 功能开发与调试」）。\n")
	b.WriteString("3. 每条一句话、不超过 30 字，动词开头：完成/修复/优化/调研/部署/搭建/实现/升级/开发。\n")
	b.WriteString("4. 条目要具体，可含技术细节（如「升级 Pi 相关依赖至 0.85.1」「session 存储到 Postgres」）；能量化则量化（次数、个数），严禁编造未提供的数字。\n")
	b.WriteString("5. 严禁输出 prompt 原文中的密钥、token、密码等敏感信息；只做摘要，不逐条复述原文。\n\n")

	switch reportType {
	case domain.ReportTypeWeekly:
		b.WriteString("输出格式（周报，聚合自本周各日报摘要，突出本周完成了什么而非逐日罗列。只输出以下两个小节，禁止输出标题或其他小节）：\n")
		b.WriteString("## 一、本周核心工作\n")
		b.WriteString("1. <本周核心工作项>\n2. ...（3-8 条，按重要性排序）\n\n")
		b.WriteString("## 二、下周工作计划\n")
		b.WriteString("1. <从本周工作自然延伸的下一步>（1-4 条，依据工作脉络推断，如本周调研了某框架则下周深入应用/落地）\n")
	case domain.ReportTypeMonthly:
		b.WriteString("输出格式（月报，聚合自当月周报/日报摘要，突出月度重点成果而非罗列每日细节。只输出以下两个小节，禁止输出标题或其他小节）：\n")
		b.WriteString("## 一、本月核心工作\n")
		b.WriteString("1. <本月关键成果>（3-10 条，按重要性排序）\n\n")
		b.WriteString("## 二、下月工作计划\n")
		b.WriteString("1. <从本月工作自然延伸的方向>（1-4 条）\n")
	default:
		b.WriteString("输出格式（日报。只输出以下两个小节，禁止输出标题或其他小节）：\n")
		b.WriteString("## 一、今日核心工作\n")
		b.WriteString("1. <今日工作项>（3-6 条，按重要性排序）\n\n")
		b.WriteString("## 二、明日工作计划\n")
		b.WriteString("1. <从今日工作自然延伸的下一步>（1-4 条，依据工作脉络推断；无法推断时写当前工作的延续推进项）\n")
	}
	return b.String()
}

// =========================
// 格式化辅助
// =========================

func orEmptyCounts(m map[string]int64) map[string]int64 {
	if m == nil {
		return map[string]int64{}
	}
	return m
}

func orEmptyHourly(m map[int]int64) map[int]int64 {
	if m == nil {
		return map[int]int64{}
	}
	return m
}

func truncateRunes(s string, limit int) string {
	s = strings.TrimSpace(s)
	if limit <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

func truncateReportError(s string) string {
	if len(s) > 1000 {
		return s[:1000]
	}
	return s
}

func formatModelCounts(models map[string]int64) string {
	if len(models) == 0 {
		return "无"
	}
	type mc struct {
		name  string
		count int64
	}
	items := make([]mc, 0, len(models))
	for name, count := range models {
		items = append(items, mc{name, count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count != items[j].count {
			return items[i].count > items[j].count
		}
		return items[i].name < items[j].name
	})
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%s×%d", it.name, it.count))
	}
	return strings.Join(parts, ", ")
}

func formatHourly(hourly map[int]int64) string {
	if len(hourly) == 0 {
		return "无"
	}
	hours := make([]int, 0, len(hourly))
	for h := range hourly {
		hours = append(hours, h)
	}
	sort.Ints(hours)
	parts := make([]string, 0, len(hours))
	for _, h := range hours {
		parts = append(parts, fmt.Sprintf("%02d时×%d", h, hourly[h]))
	}
	return strings.Join(parts, ", ")
}
