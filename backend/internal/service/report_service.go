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
func ReportPeriod(reportType string, ref time.Time) (time.Time, time.Time, error) {
	switch reportType {
	case domain.ReportTypeDaily:
		start := timezone.StartOfDay(ref)
		return start, start.Add(24 * time.Hour), nil
	case domain.ReportTypeWeekly:
		start := timezone.StartOfWeek(ref)
		return start, start.Add(7 * 24 * time.Hour), nil
	default:
		return time.Time{}, time.Time{}, domain.ErrReportInvalidType
	}
}

// GenerateReport 为单个用户生成指定周期（日/周）的报告。
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

	summary, llmErr := s.buildSummary(ctx, cfg, reportType, start, end, userID, stats)
	if llmErr != nil {
		if errors.Is(llmErr, domain.ErrReportLLMNotConfigured) {
			// LLM 未配置：产出纯统计报告，不算失败
			summary = ""
		} else {
			report.Status = domain.ReportStatusFailed
			report.Error = truncateReportError(llmErr.Error())
			report.AISummary = ""
			return s.persist(ctx, report)
		}
	}
	report.AISummary = summary
	report.Error = ""
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
	if filters.Type != "" && filters.Type != domain.ReportTypeDaily && filters.Type != domain.ReportTypeWeekly {
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
func (s *ReportService) buildSummary(
	ctx context.Context,
	cfg *ReportLLMConfig,
	reportType string,
	start, end time.Time,
	userID int64,
	stats ReportStats,
) (string, error) {
	systemPrompt := "你是团队 AI 网关的工作报告助手。根据用户在网关上的请求统计与 prompt 片段，" +
		"用简体中文输出一份结构清晰的 Markdown 报告，包含：## 工作内容要点（推测用户做了什么，3-6 条）、" +
		"## 活跃模型（简述）、## 备注（异常或建议，可省略）。" +
		"严禁输出 prompt 原文中的密钥、token、密码等敏感信息；只做摘要，不逐条复述。"

	var userPrompt strings.Builder
	userPrompt.WriteString(fmt.Sprintf("周期：%s ~ %s\n", start.Format("2006-01-02 15:04"), end.Format("2006-01-02 15:04")))

	// 周报：优先用本周各日报的摘要二次压缩（省 token）
	if reportType == domain.ReportTypeWeekly {
		dailies, _, err := s.reportRepo.List(ctx, pagination.PaginationParams{Page: 1, PageSize: 7}, ReportListFilters{
			Type:      domain.ReportTypeDaily,
			UserID:    userID,
			StartDate: start,
			EndDate:   end,
		})
		if err == nil {
			for i := range dailies {
				d := &dailies[i]
				if d.AISummary == "" {
					continue
				}
				userPrompt.WriteString(fmt.Sprintf("\n### %s 日报摘要\n%s\n", d.PeriodStart.Format("2006-01-02"), d.AISummary))
			}
		}
	} else if cfg.Enabled || (cfg.BaseURL != "" && cfg.APIKey != "" && cfg.Model != "") {
		prompts, err := s.reportRepo.FetchUserPrompts(ctx, userID, start, end, cfg.MaxPrompts)
		if err == nil && len(prompts) > 0 {
			stats.PromptCount = int64(len(prompts))
			userPrompt.WriteString(fmt.Sprintf("\n### Prompt 片段（最新 %d 条，每条截断 %d 字符）\n", len(prompts), cfg.PromptTruncateChars))
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
	return s.llm.ChatComplete(llmCtx, cfg, systemPrompt, userPrompt.String())
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
