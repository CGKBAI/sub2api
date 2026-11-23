package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

const reportLLMTimeout = 120 * time.Second

// maxReportTurns 单次报告生成最多读取的请求条数（覆盖一天的量级）。
const maxReportTurns = 300

// reportDailyMinRequests 节假日感知关闭（skip_holidays=false）时定时日报的
// 请求阈值：当日请求 ≤ 该值不生成（过滤低用量噪音）；手动生成不受限。
const reportDailyMinRequests = 10

// 报告对话窗口采样参数：快照按 session 分组拉取（每 session 取最全一条，最多
// reportSnapshotCount 个 session）；全部窗口预算 conversationWindowBudget 按
// session 数均分，单 session 不超过 conversationWindowsPerSnapCap 个窗口。
//
// 单个窗口 1000 rune（≈500 汉字）才能装下一条「问题 + 解法」；总预算 48 个窗口
// 让单日素材上限约 4.8 万字符，与 reportUserPromptMaxRunes 总量预算相配。
const (
	reportSnapshotCount           = 8
	conversationWindowBudget      = 48
	conversationWindowsPerSnapCap = 8
	conversationWindowRunes       = 1000
	conversationRegionAnchor      = "</available_skills>"
	// conversationTailRegionPct 尾部优先采样的比例：后 30% 的对话区分到一半窗口。
	// 日报最关心"最近在做什么"，等距盲采会让开头和中段的噪音等概率入选。
	conversationTailRegionPct = 0.3
	// conversationWindowSnapMax 窗口起点向段首吸附的最大步长（rune）。
	conversationWindowSnapMax = 240
	// conversationHeadNoiseRunes 锚点缺失时丢弃的前缀长度（rune），用于绕过
	// 智能体客户端置于头部的系统提示词/工具清单。
	conversationHeadNoiseRunes = 8192
	// reportUserPromptMaxRunes 单篇报告注入 LLM 的 user prompt 总预算（rune）。
	// 超预算时按"先丢最早素材"的顺序裁剪，避免请求体超出模型上下文后由
	// 服务端静默截断（那样丢哪段完全不可控）。
	reportUserPromptMaxRunes = 80000
	// conversationWindowOversampleNum/Den 窗口过采样倍数：先按 1.5 倍预算抽候选，
	// 再按"噪音分"裁回预算。这样降噪不减少素材总量，只是把位置让给更干净的窗口
	// （拍平后没有 role 标记，只能用内容特征近似"tool 段降权"）。
	conversationWindowOversampleNum = 3
	conversationWindowOversampleDen = 2
)

// ReportService 日报/周报核心服务。
type ReportService struct {
	reportRepo  ReportRepository
	groupRepo   ReportGroupRepository
	settingRepo SettingRepository
	llm         *reportLLMClient
	feishu      *reportFeishuClient
}

// NewReportService 构造报告服务。
func NewReportService(reportRepo ReportRepository, groupRepo ReportGroupRepository, settingRepo SettingRepository) *ReportService {
	return &ReportService{
		reportRepo:  reportRepo,
		groupRepo:   groupRepo,
		settingRepo: settingRepo,
		llm: &reportLLMClient{
			httpClient: &http.Client{Timeout: reportLLMTimeout + 10*time.Second},
		},
		feishu: &reportFeishuClient{
			httpClient: &http.Client{Timeout: reportFeishuPushTimeout + 5*time.Second},
		},
	}
}

// ReportPeriod 根据报告类型与基准时间计算周期边界 [start, end)。
// 周报为正常自然周 [本周一 00:00, 下周一 00:00)：定时生成日由 skip_holidays 决定
// （开=本周最后一个工作日，普通周即周五；关=固定周五）；周末/假期后有工作 →
// 之后手动重生成自动补入（手动生成不自动推飞书，需用卡片按钮推），
// ref 在周内任意时刻 → 该周一~周日报告，手动/定时语义一致。
// 月报覆盖 ref 所在自然月（选 8 月任意日期 → 生成 8 月月报）：开=当月第一个
// 工作日 19:00 生成上月（ref 由 scheduler 传上月 1 日）；关=每月最后一天出当月（ref=now）。
func ReportPeriod(reportType string, ref time.Time) (time.Time, time.Time, error) {
	switch reportType {
	case domain.ReportTypeDaily:
		start := timezone.StartOfDay(ref)
		return start, start.Add(24 * time.Hour), nil
	case domain.ReportTypeWeekly:
		start := timezone.StartOfWeek(ref)
		return start, start.Add(7 * 24 * time.Hour), nil
	case domain.ReportTypeMonthly:
		start := timezone.StartOfMonth(ref)
		return start, start.AddDate(0, 1, 0), nil
	default:
		return time.Time{}, time.Time{}, domain.ErrReportInvalidType
	}
}

// GenerateReport 为单个用户生成指定周期（日/周/月）的报告。
// trigger 区分生成来源：manual（管理端/用户手动）永不自动推飞书；
// scheduled（定时任务）按开关自动推。已存在同周期报告时覆盖更新（手动重试语义）。
func (s *ReportService) GenerateReport(ctx context.Context, userID int64, reportType string, ref time.Time, trigger ReportTrigger, suppressAutoPush bool) (*Report, error) {
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

	// 节假日感知关闭模式：定时日报仅对当日请求超过阈值的用户生成
	if belowDailyMinRequests(reportType, trigger, cfg.SkipHolidays, stats.Requests) {
		return nil, ErrReportSkippedLowUsage
	}

	// 姓名进报告标题（如「工作日报（2026-09-15）- 谢翔宇」）
	username, err := s.reportRepo.GetUsername(ctx, userID)
	if err != nil {
		return nil, err
	}
	report.Username = username

	summary, qualityWarnings, llmErr := s.buildSummary(ctx, cfg, reportType, start, end, userID, username, &stats)
	if llmErr != nil {
		if errors.Is(llmErr, domain.ErrReportLLMNotConfigured) {
			// LLM 未配置：产出纯统计报告，不算失败
			summary = ""
			qualityWarnings = nil
		} else {
			// LLM 失败：若该周期已有生成成功的报告（含摘要），保留旧报告不覆盖，
			// 避免定时重试/手动重生成偶发失败摧毁原有成果
			existing, gErr := s.reportRepo.GetByUserPeriod(ctx, userID, reportType, start)
			if gErr == nil && existing != nil && existing.Status == domain.ReportStatusDone && strings.TrimSpace(existing.AISummary) != "" {
				logger.LegacyPrintf("service.report",
					"[Report] generate %s report for user %d failed but existing done report preserved: %v",
					reportType, userID, llmErr)
				return existing, nil
			}
			report.Status = domain.ReportStatusFailed
			report.Error = truncateReportError(llmErr.Error())
			report.AISummary = ""
			report.QualityWarnings = ""
			report.Stats = stats
			return s.persist(ctx, report)
		}
	}
	if len(qualityWarnings) > 0 {
		logger.LegacyPrintf("service.report",
			"[Report] %s report for user %d generated with quality warnings: %s",
			reportType, userID, strings.Join(qualityWarnings, "；"))
	}
	report.AISummary = summary
	report.QualityWarnings = truncateQualityWarnings(qualityWarnings)
	report.Error = ""
	report.Stats = stats
	saved, err := s.persist(ctx, report)
	if err != nil {
		return nil, err
	}
	// 仅定时生成自动推飞书；手动生成只在 web 展示，由卡片按钮手动推送。
	// suppressAutoPush：周报/月报发布日抑制日报自动推送（调度器传入），防群消息刷屏
	if trigger == ReportTriggerScheduled && !suppressAutoPush {
		s.maybeAutoPushFeishu(ctx, cfg, saved)
	}
	return saved, nil
}

// maybeAutoPushFeishu 定时生成成功后的飞书自动推送（手动生成不经过此路径）
// （best-effort：失败仅记日志，不影响生成结果）。
// 生效条件：feishu_enabled + 对应类型推送开关 + 用户参与推送（users.report_push_enabled），
// 然后投递到该用户所属的所有启用群组。
//
// 幂等：该报告此前已推送成功（pushed_at 非空）时不再自动推。persist 的 Update
// 不触碰 pushed_at，因此重生成同周期报告时该字段仍是上次的真实推送状态——
// Redis 评估标记丢失、leader 切换、节假日模式切换等导致的"同周期再生成一次"
// 不会把同一份报告重复刷进群。需要补发/更新请用卡片上的手动推送按钮。
func (s *ReportService) maybeAutoPushFeishu(ctx context.Context, cfg *ReportLLMConfig, report *Report) {
	if s == nil || s.feishu == nil || report == nil || report.Status != domain.ReportStatusDone {
		return
	}
	if report.PushedAt != nil {
		logger.LegacyPrintf("service.report",
			"[ReportFeishu] auto push skipped for report %d (%s): already pushed at %s",
			report.ID, report.Type, report.PushedAt.Format("2006-01-02 15:04"))
		return
	}
	if cfg == nil || !cfg.FeishuEnabled {
		return
	}
	switch report.Type {
	case domain.ReportTypeDaily:
		if !cfg.FeishuPushDaily {
			return
		}
	case domain.ReportTypeWeekly:
		if !cfg.FeishuPushWeekly {
			return
		}
	case domain.ReportTypeMonthly:
		if !cfg.FeishuPushMonthly {
			return
		}
	default:
		return
	}
	optIn, err := s.reportRepo.IsReportPushEnabled(ctx, report.UserID)
	if err != nil {
		logger.LegacyPrintf("service.report", "[ReportFeishu] check push opt-in for user %d: %v", report.UserID, err)
		return
	}
	if !optIn {
		return
	}
	s.pushReportToUserGroups(ctx, cfg, report)
}

// markPushResult 持久化推送结果（成功写 pushed_at，失败记 last_push_error）。
// best-effort：落库失败仅记日志，不影响推送调用方；用独立 Background ctx，
// 避免调用方（调度预算/请求）已取消时推送状态丢失。
func (s *ReportService) markPushResult(report *Report, pushErr error) {
	if s == nil || s.reportRepo == nil || report == nil || report.ID <= 0 {
		return
	}
	now := timezone.Now()
	if pushErr != nil {
		msg := truncateReportError(pushErr.Error())
		if err := s.reportRepo.MarkPushResult(context.Background(), report.ID, now, msg); err != nil {
			logger.LegacyPrintf("service.report", "[ReportFeishu] record push failure for report %d: %v", report.ID, err)
		}
		report.LastPushError = msg
		return
	}
	if err := s.reportRepo.MarkPushResult(context.Background(), report.ID, now, ""); err != nil {
		logger.LegacyPrintf("service.report", "[ReportFeishu] record push success for report %d: %v", report.ID, err)
		return
	}
	report.PushedAt = &now
	report.LastPushError = ""
}

// pushReportWithConfig 手动推送单篇报告到作者所属的所有群组（独立 10s/群超时），
// 并记录每个群的投递结果。与自动推送不同：不检查类型开关与用户参与开关，
// 只要求全局飞书推送已启用且用户至少属于一个启用群组。
func (s *ReportService) pushReportWithConfig(ctx context.Context, report *Report) error {
	cfg, err := s.GetReportConfig(ctx)
	if err != nil {
		return err
	}
	if cfg == nil || !cfg.FeishuEnabled {
		return domain.ErrReportFeishuNotConfigured
	}
	success, failed, pushErr := s.pushReportToUserGroups(ctx, cfg, report)
	if success == 0 && failed == 0 {
		return domain.ErrReportFeishuNoGroups
	}
	return pushErr
}

// PushReport 手动推送指定报告到飞书（管理端按钮）。
// 与自动推送不同：不检查类型开关与用户参与开关，只要求 webhook 已配置。
func (s *ReportService) PushReport(ctx context.Context, reportID int64) (*Report, error) {
	if s == nil || s.reportRepo == nil {
		return nil, errors.New("report repository not initialized")
	}
	report, err := s.GetReport(ctx, reportID)
	if err != nil {
		return nil, err
	}
	if err := s.pushReportWithConfig(ctx, report); err != nil {
		return nil, err
	}
	return report, nil
}

// PushUserReport 用户侧手动推送：只能推自己的报告（无权限时按不存在处理）。
func (s *ReportService) PushUserReport(ctx context.Context, userID, reportID int64) (*Report, error) {
	if userID <= 0 {
		return nil, errors.New("invalid user id")
	}
	report, err := s.GetReport(ctx, reportID)
	if err != nil {
		return nil, err
	}
	if report.UserID != userID {
		return nil, domain.ErrReportNotFound
	}
	if err := s.pushReportWithConfig(ctx, report); err != nil {
		return nil, err
	}
	return report, nil
}

// GenerateForAllUsers 为周期内所有活跃用户生成报告（scheduler / 管理端手动触发用）。
// trigger 语义同 GenerateReport。返回成功生成的数量与首个错误。
// belowDailyMinRequests 判断定时日报是否低于请求阈值（仅节假日感知关闭模式生效）。
func belowDailyMinRequests(reportType string, trigger ReportTrigger, skipHolidays bool, requests int64) bool {
	return trigger == ReportTriggerScheduled &&
		reportType == domain.ReportTypeDaily &&
		!skipHolidays &&
		requests <= reportDailyMinRequests
}

func (s *ReportService) GenerateForAllUsers(ctx context.Context, reportType string, ref time.Time, trigger ReportTrigger, suppressAutoPush bool) (int, error) {
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
		_, gErr := s.GenerateReport(ctx, uid, reportType, ref, trigger, suppressAutoPush)
		if gErr != nil {
			if errors.Is(gErr, ErrReportGenerateUserNotFound) || errors.Is(gErr, ErrReportSkippedLowUsage) {
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
	item, err := s.reportRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if item != nil {
		items := []Report{*item}
		s.attachDeliveries(ctx, items)
		item.PushDeliveries = items[0].PushDeliveries
	}
	return item, nil
}

// ListReports 管理端列表（可按人过滤）。
func (s *ReportService) ListReports(ctx context.Context, params pagination.PaginationParams, filters ReportListFilters) ([]Report, *pagination.PaginationResult, error) {
	if s == nil || s.reportRepo == nil {
		return nil, nil, errors.New("report repository not initialized")
	}
	if filters.Type != "" && filters.Type != domain.ReportTypeDaily && filters.Type != domain.ReportTypeWeekly && filters.Type != domain.ReportTypeMonthly {
		return nil, nil, domain.ErrReportInvalidType
	}
	items, pageInfo, err := s.reportRepo.List(ctx, params, filters)
	if err != nil {
		return nil, nil, err
	}
	s.attachDeliveries(ctx, items)
	return items, pageInfo, nil
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
//
// 返回质量警告（非空表示正文通过了硬规则但存在软问题，或重写后仍有硬违规），
// 由调用方落库到 reports.quality_warnings 供前端 ⚠️ 标记；正文照常返回。
func (s *ReportService) buildSummary(
	ctx context.Context,
	cfg *ReportLLMConfig,
	reportType string,
	start, end time.Time,
	userID int64,
	username string,
	stats *ReportStats,
) (string, []string, error) {
	userPrompt, hasGoal, err := s.buildSummaryPrompt(ctx, cfg, reportType, start, end, userID, stats)
	if err != nil {
		return "", nil, err
	}
	if !reportLLMConfigured(cfg) {
		return "", nil, domain.ErrReportLLMNotConfigured
	}

	llmCtx, cancel := context.WithTimeout(ctx, reportLLMTimeout)
	defer cancel()

	body, warnings, err := s.completeCheckedReport(llmCtx, cfg, reportSystemPrompt(reportType), userPrompt, reportType, hasGoal)
	if err != nil {
		return "", nil, err
	}
	return reportTitle(reportType, start, username) + "\n\n" + body, warnings, nil
}

func reportLLMConfigured(cfg *ReportLLMConfig) bool {
	return cfg != nil && (cfg.Enabled || (cfg.BaseURL != "" && cfg.APIKey != "" && cfg.Model != ""))
}

// buildSummaryPrompt 组装注入 LLM 的 user prompt，并按 reportUserPromptMaxRunes
// 做总量裁剪。hasGoal 表示日报上下文中注入了「用户近期目标」（决定是否要求第三节）。
func (s *ReportService) buildSummaryPrompt(
	ctx context.Context,
	cfg *ReportLLMConfig,
	reportType string,
	start, end time.Time,
	userID int64,
	stats *ReportStats,
) (string, bool, error) {
	var fixed strings.Builder
	fixed.WriteString(fmt.Sprintf("周期：%s ~ %s\n", start.Format("2006-01-02 15:04"), end.Format("2006-01-02 15:04")))

	// 日报读取用户自行填写的近期目标（users.report_goal）：非空时注入上下文，
	// 供「明日工作计划」围绕目标拆解（读取失败降级为无目标生成，不阻断报告）。
	// 周报/月报聚合日报摘要时目标已自然继承，不直接读取。
	hasGoal := false
	if reportType == domain.ReportTypeDaily {
		goal, gErr := s.reportRepo.GetReportGoal(ctx, userID)
		if gErr != nil {
			logger.LegacyPrintf("service.report", "[Report] fetch report goal for user %d: %v", userID, gErr)
		} else if goal = strings.TrimSpace(goal); goal != "" {
			hasGoal = true
			fixed.WriteString("\n### 用户近期目标（用户自行填写，制定计划时必须对齐）\n")
			fixed.WriteString(goal + "\n")
		}
	}

	// 子报告聚合（周报/月报）：直接读取已生成的日报/周报摘要。
	aggregated := false
	var aggregatedBlock strings.Builder
	switch reportType {
	case domain.ReportTypeWeekly:
		aggregated = s.appendSubReports(ctx, &aggregatedBlock, domain.ReportTypeDaily, start, end, 7, userID)
	case domain.ReportTypeMonthly:
		aggregated = s.appendSubReports(ctx, &aggregatedBlock, domain.ReportTypeWeekly, start, end, 8, userID)
		if !aggregated {
			aggregated = s.appendSubReports(ctx, &aggregatedBlock, domain.ReportTypeDaily, start, end, 31, userID)
		}
	}

	var items []string
	var windows []string
	var turnCount int
	if !aggregated && reportLLMConfigured(cfg) {
		turns, err := s.reportRepo.FetchUserTurns(ctx, userID, start, end, maxReportTurns)
		if err == nil && len(turns) > 0 {
			turnCount = len(turns)
			stats.PromptCount = int64(len(turns))
			items = collectUserTurns(turns, cfg.PromptTruncateChars)
			// max_prompts 配置此前是死配置（一直用 maxReportTurns）：这里按"去重后
			// 最多注入多少条有效提问"生效，超出时保留最近的条目。
			if maxItems := cfg.MaxPrompts; maxItems > 0 && len(items) > maxItems {
				items = items[len(items)-maxItems:]
			}
		}
		// 智能体客户端（opencode 等）的用户输入混在会话历史深处，头部提取拿不到；
		// 补充会话快照的对话区窗口采样，覆盖全周期工作脉络。
		// 快照按 session 分组：每 session 取上下文最全的一条，最多 reportSnapshotCount 个。
		snaps, serr := s.reportRepo.FetchPromptSnapshots(ctx, userID, start, end, reportSnapshotCount)
		if serr == nil && len(snaps) > 0 {
			windows = collectConversationWindows(snaps)
		}
	}

	var statsBlock strings.Builder
	statsBlock.WriteString("\n### 统计数字\n")
	statsBlock.WriteString(fmt.Sprintf("- 请求次数：%d\n", stats.Requests))
	statsBlock.WriteString(fmt.Sprintf("- 输入/输出 tokens：%d / %d\n", stats.InputTokens, stats.OutputTokens))
	statsBlock.WriteString(fmt.Sprintf("- 费用：%.4f\n", stats.TotalCost))
	statsBlock.WriteString("- 模型分布：" + formatModelCounts(stats.Models) + "\n")
	statsBlock.WriteString("- 活跃时段：" + formatHourly(stats.Hourly) + "\n")

	// 总量预算：固定部分优先保留，剩余额度在「提问记录」与「对话片段」之间分配。
	// 超预算时从最早的一条开始丢（两者都按时间正序），保证最近的工作留下。
	budget := reportUserPromptMaxRunes - runeLen(fixed.String()) - runeLen(aggregatedBlock.String()) - runeLen(statsBlock.String())
	if budget < 0 {
		budget = 0
	}
	for runeLen(strings.Join(items, ""))+runeLen(strings.Join(windows, "")) > budget {
		if len(items) == 0 && len(windows) == 0 {
			break
		}
		// 交替裁剪，避免某一类素材被整体丢光。
		if len(items) >= len(windows) && len(items) > 0 {
			items = items[1:]
			continue
		}
		if len(windows) > 0 {
			windows = windows[1:]
			continue
		}
		items = items[1:]
	}

	var b strings.Builder
	b.WriteString(fixed.String())
	b.WriteString(aggregatedBlock.String())
	if len(items) > 0 {
		b.WriteString(fmt.Sprintf("\n### 用户提问记录（共 %d 次请求，提取去重后 %d 条有效提问，按时间正序）\n", turnCount, len(items)))
		for _, it := range items {
			b.WriteString(it + "\n")
		}
	}
	if len(windows) > 0 {
		b.WriteString(fmt.Sprintf("\n### 对话记录片段（截取自最近 %d 个会话的「最新 + 中段」快照，尾部优先采样，按时间排列，含用户输入/助手回复/工具输出）\n", reportSnapshotCount))
		for _, w := range windows {
			b.WriteString("- " + w + "\n")
		}
	}
	b.WriteString(statsBlock.String())
	return b.String(), hasGoal, nil
}

func runeLen(s string) int { return len([]rune(s)) }

// completeCheckedReport 调用 LLM 生成正文并做确定性质量校验。
// 命中硬违规时把违规清单回灌重写一次；重写仍不通过则选更轻的一份照发并返回
// 警告。唯一例外：敏感信息在两次输出中都出现时整篇判失败（不落库、不推送）。
func (s *ReportService) completeCheckedReport(
	ctx context.Context,
	cfg *ReportLLMConfig,
	systemPrompt, userPrompt, reportType string,
	hasGoal bool,
) (string, []string, error) {
	body, err := s.llm.ChatComplete(ctx, cfg, systemPrompt, userPrompt)
	if err != nil {
		return "", nil, err
	}
	body = sanitizeReportBody(body)
	first := checkReportQuality(body, reportType, hasGoal)
	if len(first.Hard) == 0 {
		// 软问题（条目数/过长/代码块/空泛措辞）只记日志，不打扰作者：
		// 若软问题也打 ⚠️，多数正常报告都会带标记，徽标就失去意义了。
		logReportQualitySoft(reportType, first.Soft)
		return body, nil, nil
	}

	// 预算已耗尽（调度器批量生成的 30min 上限）时不再重试，直接照发并标记。
	if ctx.Err() != nil {
		if first.Sensitive {
			return "", nil, domain.ErrReportSensitiveContent
		}
		return body, reportQualityWarningsList(first), nil
	}

	logger.LegacyPrintf("service.report",
		"[Report] quality check failed for %s report, retrying once: %s", reportType, strings.Join(first.Hard, "；"))

	retried, retryErr := s.llm.ChatComplete(ctx, cfg, systemPrompt+reportQualityRetryNote(first), userPrompt)
	if retryErr != nil {
		logger.LegacyPrintf("service.report", "[Report] quality retry call failed: %v", retryErr)
		if first.Sensitive {
			return "", nil, domain.ErrReportSensitiveContent
		}
		return body, reportQualityWarningsList(first), nil
	}
	retried = sanitizeReportBody(retried)
	second := checkReportQuality(retried, reportType, hasGoal)

	best, bestResult := body, first
	if second.Sensitive != first.Sensitive {
		// 无敏感信息的那一份优先（敏感信息不可用）。
		if first.Sensitive {
			best, bestResult = retried, second
		}
	} else if len(second.Hard) < len(first.Hard) {
		best, bestResult = retried, second
	}
	if bestResult.Sensitive {
		return "", nil, domain.ErrReportSensitiveContent
	}
	return best, reportQualityWarningsList(bestResult), nil
}

// logReportQualitySoft 记录不阻断的软问题（仅供排查，不落库、不打徽标）。
func logReportQualitySoft(reportType string, soft []string) {
	if len(soft) == 0 {
		return
	}
	logger.LegacyPrintf("service.report",
		"[Report] %s report soft quality issues: %s", reportType, strings.Join(soft, "；"))
}

// reportQualityWarningsList 合并硬违规与软问题作为落库警告。
func reportQualityWarningsList(v reportQualityResult) []string {
	all := make([]string, 0, len(v.Hard)+len(v.Soft))
	all = append(all, v.Hard...)
	all = append(all, v.Soft...)
	return uniqueStrings(all)
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

// =========================
// 用户提问提取（日报素材）
// =========================

var userTurnReminderRe = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// userTurnContextMarkers：上下文部分（拼接在用户消息之后）的开头噪音特征，
// 命中即认为用户输入到此为止。
var userTurnContextMarkers = []string{
	"\n\nYou are ", "\n\nThe user ", "\n\n<system", "\n\n```", "\n\n{", "\n\nx-anthropic",
}

// userTurnNoisePrefixes：提取结果若以这些开头，判定为工具/系统输出而非用户输入。
var userTurnNoisePrefixes = []string{
	"You are ", "The user ", "<", "{", "[", "```", "#", "IMPORTANT", "I'll", "I will",
}

// collectUserTurns 从逐次请求快照中提取用户真实提问并去重（时间正序）。
// 同一 session 自动续跑会产生大量内容相同的请求，按前缀去重后得到当天有效提问清单。
func collectUserTurns(turns []UserPromptSnippet, truncateChars int) []string {
	seen := make(map[string]struct{}, len(turns))
	items := make([]string, 0, 16)
	for _, t := range turns {
		var intent string
		if t.CleanText {
			// 写入时已经抽干（user_text 列）：直接截断即可，不要再走正则提取，
			// 否则会被「以 # / { / 开头就判为噪音」之类的启发式误杀。
			intent = truncateRunes(t.Content, truncateChars)
		} else {
			intent = extractUserTurn(t.Content, truncateChars)
		}
		if intent == "" {
			continue
		}
		key := turnDedupKey(intent)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		items = append(items, fmt.Sprintf("- [%s][%s] %s", t.CreatedAt.Format("15:04"), t.Model, intent))
	}
	return items
}

// extractUserTurn 从拍平 prompt 头部提取该次请求的用户真实输入。
// 审计落库顺序为「该次请求最后一条用户消息」在前：剥掉头部注入的
// <system-reminder> 块后，剩余开头即用户文本；再截到上下文噪音边界，
// 过滤纯工具输出，最后按配置截断。
func extractUserTurn(raw string, truncateChars int) string {
	if truncateChars <= 0 {
		truncateChars = 500
	}
	s := strings.TrimSpace(raw)
	// 剥离头部连续的注入块（opencode/agent 会把提醒塞进用户消息开头）
	for strings.HasPrefix(s, "<system-reminder>") {
		loc := userTurnReminderRe.FindStringIndex(s)
		if loc == nil {
			return "" // 未闭合的注入块：整段视为噪音
		}
		s = strings.TrimSpace(s[loc[1]:])
	}
	if s == "" {
		return ""
	}
	for _, marker := range userTurnContextMarkers {
		if i := strings.Index(s, marker); i > 0 {
			s = s[:i]
		}
	}
	s = strings.TrimSpace(s)
	if !looksLikeUserTurn(s) {
		return ""
	}
	return truncateRunes(s, truncateChars)
}

// looksLikeUserTurn 过滤工具输出/系统文本：噪音开头特征直接排除；
// 含中文（团队主要输入语言）放行；纯英文需为短指令。
func looksLikeUserTurn(s string) bool {
	if s == "" {
		return false
	}
	for _, p := range userTurnNoisePrefixes {
		if strings.HasPrefix(s, p) {
			return false
		}
	}
	if countCJKRunes(s) >= 2 {
		return true
	}
	return len([]rune(s)) <= 160
}

func countCJKRunes(s string) int {
	n := 0
	for _, r := range s {
		if (r >= 0x4e00 && r <= 0x9fff) || (r >= 0x3400 && r <= 0x4dbf) {
			n++
		}
	}
	return n
}

// turnDedupKey 取前 64 个 rune（忽略空白）作为去重键。
func turnDedupKey(s string) string {
	var b strings.Builder
	count := 0
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		b.WriteRune(r)
		count++
		if count >= 64 {
			break
		}
	}
	return b.String()
}

// collectConversationWindows 从每个会话快照的「对话区」抽取窗口。
// 总窗口预算按快照数均分（单 session 上限 conversationWindowsPerSnapCap）：
// 快照多时每份窗口变少、少时变多，总传输量稳定在预算附近。
// 同一 session 的 latest 与 mid 两条快照可能包含重叠内容，完全相同的结果会去重。
// 对话区定位：优先取 available_skills 清单结束标记之后的内容（opencode 等
// 智能体客户端把系统提示词/工具/技能清单放在前部，真实对话在其后）；
// 无该标记时从头开始（普通聊天客户端历史即对话）。
func collectConversationWindows(snaps []UserPromptSnippet) []string {
	if len(snaps) == 0 {
		return nil
	}
	// 先按 1.5 倍预算抽候选，再由 selectReportWindows 按噪音裁回预算：
	// 降噪不减少素材总量，只把位置让给更干净的窗口。
	per := conversationWindowBudget * conversationWindowOversampleNum / conversationWindowOversampleDen
	if len(snaps) > 0 {
		per = per / len(snaps)
	}
	if per < 1 {
		per = 1
	}
	if per > conversationWindowsPerSnapCap {
		per = conversationWindowsPerSnapCap
	}
	candidates := make([]reportWindowCandidate, 0, len(snaps)*per)
	seen := make(map[string]struct{}, len(snaps)*per)
	for _, s := range snaps {
		for _, w := range sampleWindows(s.Content, per, conversationWindowRunes) {
			if _, dup := seen[w]; dup {
				continue
			}
			seen[w] = struct{}{}
			candidates = append(candidates, reportWindowCandidate{text: w, noise: reportWindowNoise(w)})
		}
	}
	return selectReportWindows(candidates, conversationWindowBudget)
}

// reportWindowCandidate 是一个待选窗口及其噪音分（0=干净，1=纯噪音）。
type reportWindowCandidate struct {
	text  string
	noise float64
}

// selectReportWindows 在候选里保留至多 budget 个窗口：按噪音从高到低淘汰，
// 噪音相同时优先淘汰**更早**的窗口（保住"最近的工作"），输出保持原始时间顺序。
// 所以降噪既不减少素材量，也不打乱时间线。
func selectReportWindows(candidates []reportWindowCandidate, budget int) []string {
	if budget <= 0 {
		budget = 1
	}
	keep := make([]bool, len(candidates))
	for i := range keep {
		keep[i] = true
	}
	if toDrop := len(candidates) - budget; toDrop > 0 {
		order := make([]int, 0, len(candidates))
		for i := range candidates {
			order = append(order, i)
		}
		sort.SliceStable(order, func(a, b int) bool {
			ia, ib := order[a], order[b]
			if candidates[ia].noise != candidates[ib].noise {
				return candidates[ia].noise > candidates[ib].noise
			}
			return ia < ib
		})
		for i := 0; i < toDrop; i++ {
			keep[order[i]] = false
		}
	}
	out := make([]string, 0, len(candidates))
	for i, c := range candidates {
		if keep[i] {
			out = append(out, c.text)
		}
	}
	return out
}

// conversationNoiseMarkers 是工具输出/日志的高置信特征。
var conversationNoiseMarkers = []string{
	"diff --git",
	"+++ b/",
	"--- a/",
	"node_modules/",
	"package-lock.json",
	"Traceback (most recent call last)",
	"npm ERR!",
}

// reportWindowNoise 给窗口打 0~1 的噪音分（越高越像工具输出/代码/日志）。
//
// 为什么用内容特征而不是 role：full_prompt 是拍平文本，段间没有角色标记，
// 读取侧拿不到"这一段是 tool 还是 user"。所以这里用内容特征做**等效降权**——
// 标点密度高、行很长、几乎没有中文、命中 diff/堆栈特征、同一行重复多次，
// 都判为噪音；纯中文叙述的人话得 0 分。
func reportWindowNoise(window string) float64 {
	runes := []rune(window)
	if len(runes) == 0 {
		return 1
	}
	var punct, cjk, ascii int
	lines, cur, maxLine := 1, 0, 0
	for _, r := range runes {
		if r == '\n' {
			lines++
			if cur > maxLine {
				maxLine = cur
			}
			cur = 0
			continue
		}
		cur++
		if r < 128 {
			ascii++
		}
		if unicode.Is(unicode.Han, r) {
			cjk++
		}
		switch r {
		case '{', '}', '[', ']', '(', ')', ';', '=', '<', '>', '"', '\\', '|', '`', '$':
			punct++
		}
	}
	if cur > maxLine {
		maxLine = cur
	}

	total := float64(len(runes))
	cjkRatio := float64(cjk) / total
	punctRatio := float64(punct) / total
	avgLine := total / float64(lines)

	score := 0.0
	if punctRatio > 0.25 {
		score += 0.35
	} else if punctRatio > 0.15 {
		score += 0.15
	}
	// 超长行 + 几乎没有中文 = 压缩 JSON / 打包产物 / 长路径
	if avgLine > 200 && cjkRatio < 0.3 {
		score += 0.3
	}
	// 基本纯英文的代码/日志（中文用户的工作报告不会这样）
	if cjkRatio < 0.05 && ascii > 400 {
		score += 0.25
	}
	for _, marker := range conversationNoiseMarkers {
		if strings.Contains(window, marker) {
			score += 0.4
			break
		}
	}
	if hasRepeatedLongLine(runes) {
		score += 0.2
	}
	if score > 1 {
		score = 1
	}
	return score
}

// hasRepeatedLongLine 判断是否同一行长内容重复出现 ≥4 次（典型的分隔线/日志刷屏）。
func hasRepeatedLongLine(runes []rune) bool {
	counts := make(map[string]int, 16)
	var line strings.Builder
	flush := func() {
		if line.Len() > 40 {
			counts[line.String()]++
		}
		line.Reset()
	}
	for _, r := range runes {
		if r == '\n' {
			flush()
			continue
		}
		if line.Len() <= 4096 {
			line.WriteRune(r)
		}
	}
	flush()
	for _, count := range counts {
		if count >= 4 {
			return true
		}
	}
	return false
}

// sampleWindows 在 region 内抽取 n 个长为 size（rune）的窗口。
//
// 与旧实现（等距盲采 + 硬切）相比有三点差异：
//  1. 尾部优先：后 30% 的对话区分到一半窗口预算，让"最近在做什么"更容易入选；
//  2. 段首对齐：起点向后吸附到最近的段落开头（或代码块边界），不再切在半句/
//     半个代码块中间——素材出现半句话时 LLM 只能猜，猜就是编；
//  3. 跳过代码块：窗口落在 ``` 围栏内的直接丢弃（工具输出对日报价值最低）。
func sampleWindows(text string, n, size int) []string {
	region := text
	anchored := false
	if i := strings.Index(region, conversationRegionAnchor); i >= 0 {
		region = region[i+len(conversationRegionAnchor):]
		anchored = true
	}
	if size <= 0 {
		size = conversationWindowRunes
	}
	if n < 2 {
		n = 2
	}
	runes := []rune(region)
	// 锚点缺失时的兜底：超长文本的前段大概率是系统提示词/工具清单（普通聊天
	// 客户端不会这么长），先丢弃再采样，避免窗口全落在噪音里。
	if !anchored && len(runes) > 4*conversationHeadNoiseRunes {
		runes = runes[conversationHeadNoiseRunes:]
	}
	if len(runes) <= size {
		if w := strings.TrimSpace(string(runes)); w != "" {
			return []string{w}
		}
		return nil
	}
	// 装不下两个互不重叠的窗口时，整段作为单窗返回（小快照不需要采样）。
	if len(runes) < 2*size {
		if w := strings.TrimSpace(string(runes)); w != "" {
			return []string{w}
		}
		return nil
	}

	// 尾部优先：headN 窗口铺在前段，tailN 窗口铺在后 30%。
	tailStart := len(runes) - int(float64(len(runes))*conversationTailRegionPct)
	if tailStart < size {
		tailStart = size
	}
	if tailStart > len(runes)-size {
		tailStart = len(runes) - size
	}
	headN := n / 2
	tailN := n - headN
	// 尾段装不下一个窗口时全部交给前段，避免预算被"短尾段"吞掉。
	tailLen := len(runes) - tailStart
	if tailLen < size {
		headN, tailN = n, 0
	} else {
		// 尾段能容纳的独立窗口数（至少 1 个整块窗口），超出的回填前段，
		// 保证总窗口数稳定不被短区域吞掉。
		maxTail := tailLen / size
		if maxTail < 1 {
			maxTail = 1
		}
		if maxTail < tailN {
			headN += tailN - maxTail
			tailN = maxTail
		}
	}

	windows := make([]string, 0, n)
	windows = append(windows, sampleEvenWindows(runes[:tailStart], headN, size)...)
	windows = append(windows, sampleEvenWindows(runes[tailStart:], tailN, size)...)
	return windows
}

// sampleEvenWindows 在 runes 内等距抽 count 个窗口，起点向段首吸附并跳过代码块。
func sampleEvenWindows(runes []rune, count, size int) []string {
	if count <= 0 || len(runes) == 0 {
		return nil
	}
	if len(runes) <= size {
		if w := strings.TrimSpace(string(runes)); w != "" {
			return []string{w}
		}
		return nil
	}
	span := len(runes) - size
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		start := 0
		if count > 1 {
			start = i * span / (count - 1)
		}
		start = snapToParagraphStart(runes, start, size)
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		if insideFencedCode(runes, start) {
			continue
		}
		if w := strings.TrimSpace(string(runes[start:end])); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// snapToParagraphStart 把窗口起点向后吸附到最近的段落/代码块开头（最多
// conversationWindowSnapMax rune），保证素材不从一个句子的中间开始。
func snapToParagraphStart(runes []rune, start, size int) int {
	if start <= 0 {
		return 0
	}
	maxStart := len(runes) - size
	if maxStart <= 0 {
		return 0
	}
	limit := start + conversationWindowSnapMax
	if limit > maxStart {
		limit = maxStart
	}
	for i := start; i < limit; i++ {
		if runes[i] != '\n' || i+1 >= len(runes) {
			continue
		}
		next := runes[i+1]
		if next == '\n' || next == '#' || next == '-' || next == '*' {
			return i + 1
		}
	}
	return start
}

// insideFencedCode 判断 pos 是否落在 ``` 代码围栏内部（围栏计数为奇数）。
func insideFencedCode(runes []rune, pos int) bool {
	fences := 0
	for i := 0; i+2 < pos; i++ {
		if runes[i] == '`' && runes[i+1] == '`' && runes[i+2] == '`' {
			fences++
			i += 2
		}
	}
	return fences%2 == 1
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
	b.WriteString("1. 素材分两部分：「用户提问记录」是从每次请求中提取的用户真实输入；「对话记录片段」截取自会话快照（含用户输入、助手回复、工具输出，大致按时间排列）。据此推断实际做了什么工作：用户输入表达意图，助手回复和工具输出（读/写文件、执行命令、部署）反映实际操作。\n")
	b.WriteString("2. 素材中混有系统提示词、注入说明、文件内容等噪音——只用于理解场景，不要当作工作内容复述。\n")
	b.WriteString("3. 同类工作必须合并：多次调试/提交同一功能合并为一条（如「完成 XX 功能开发与调试」）。\n")
	b.WriteString("4. 每条一句话、不超过 30 字，动词开头：完成/修复/优化/调研/部署/搭建/实现/升级/开发。\n")
	b.WriteString("5. 条目要写「做了什么」（对象+动作+结果），禁止写「用什么工具/什么模式/多少请求」：\n   ❌ 使用 opencode CLI 工具进行代码开发（累计 180 次请求）\n   ❌ 在 plan mode 与 build mode 模式间切换推进任务\n   ✅ 整理 work-report 模板并接入 sub2api 日报/周报/月报生成\n   ✅ 修复报告页周报/月报查不到数据的问题\n")
	b.WriteString("6. 能量化则量化（次数、个数），严禁编造未提供的数字。\n")
	b.WriteString("7. 严禁输出素材中的密钥、token、密码等敏感信息；只做摘要，不逐条复述原文。\n\n")

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
		b.WriteString("输出格式（日报。除下述小节外禁止输出标题或其他小节）：\n")
		b.WriteString("## 一、今日核心工作\n")
		b.WriteString("1. <今日工作项>（3-6 条，按重要性排序）\n\n")
		b.WriteString("## 二、明日工作计划\n")
		b.WriteString("1. <从今日工作自然延伸的下一步>（1-4 条，依据工作脉络推断；无法推断时写当前工作的延续推进项）\n\n")
		b.WriteString("若素材包含「用户近期目标」（用户自行设定的近期工作目标），必须额外输出第三节，把目标在报告中单独成节呈现：\n")
		b.WriteString("## 三、近期目标计划\n")
		b.WriteString("1. <围绕用户近期目标拆解的具体推进项>（1-6 条；目标涉及多个项目时分别列出，每条一句话动词开头：完成/推进/启动/上线）\n")
		b.WriteString("第三节条目必须来自用户目标与实际素材的结合：已在推进的目标写下一步推进安排，尚未开始的目标写启动计划；「今日核心工作」中与目标相关的工作可标注对目标的推进，「明日工作计划」不再重复目标内容。未提供「用户近期目标」时严禁输出第三节。\n")
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

// truncateQualityWarnings 把质量警告压成落库的一行（多条用「；」分隔，上限 500 字）。
func truncateQualityWarnings(items []string) string {
	return reportQualityWarnings(reportQualityResult{Hard: items})
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
