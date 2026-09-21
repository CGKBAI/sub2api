package admin

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// ReportHandler handles admin daily/weekly report management
type ReportHandler struct {
	reportService *service.ReportService
}

// NewReportHandler creates a new admin report handler
func NewReportHandler(reportService *service.ReportService) *ReportHandler {
	return &ReportHandler{
		reportService: reportService,
	}
}

// List handles listing reports with filters
// GET /api/v1/admin/reports?type=daily&user_id=&date=YYYY-MM-DD
func (h *ReportHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)

	filters, ok := parseReportFilters(c)
	if !ok {
		return
	}

	params := pagination.PaginationParams{
		Page:     page,
		PageSize: pageSize,
	}

	items, paginationResult, err := h.reportService.ListReports(c.Request.Context(), params, filters)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]dto.Report, 0, len(items))
	for i := range items {
		out = append(out, *dto.ReportFromService(&items[i]))
	}
	response.Paginated(c, out, paginationResult.Total, page, pageSize)
}

// GetByID handles getting a report by ID
// GET /api/v1/admin/reports/:id
func (h *ReportHandler) GetByID(c *gin.Context) {
	reportID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || reportID <= 0 {
		response.BadRequest(c, "Invalid report ID")
		return
	}

	item, err := h.reportService.GetReport(c.Request.Context(), reportID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.ReportFromService(item))
}

// Push handles manually pushing a report to Feishu
// POST /api/v1/admin/reports/:id/push
func (h *ReportHandler) Push(c *gin.Context) {
	reportID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || reportID <= 0 {
		response.BadRequest(c, "Invalid report ID")
		return
	}

	item, err := h.reportService.PushReport(c.Request.Context(), reportID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.ReportFromService(item))
}

type generateReportRequest struct {
	Type   string `json:"type" binding:"required,oneof=daily weekly monthly"`
	UserID int64  `json:"user_id" binding:"required,gt=0"`
	// Date 基准日期（YYYY-MM-DD，服务器时区）；为空取当天
	Date string `json:"date" binding:"omitempty,datetime=2006-01-02"`
}

type generateAllReportRequest struct {
	Type string `json:"type" binding:"required,oneof=daily weekly monthly"`
	// Date 基准日期（YYYY-MM-DD，服务器时区）；为空取当天
	Date string `json:"date" binding:"omitempty,datetime=2006-01-02"`
}

// Generate handles generating a report for one user (manual trigger / retry)
// POST /api/v1/admin/reports/generate
func (h *ReportHandler) Generate(c *gin.Context) {
	var req generateReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	ref, ok := reportRefDate(c, req.Date)
	if !ok {
		return
	}

	item, err := h.reportService.GenerateReport(c.Request.Context(), req.UserID, req.Type, ref, service.ReportTriggerManual, false)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.ReportFromService(item))
}

// reportBatchGenerateTimeout caps a manual generate-all run. It mirrors the
// scheduler budget (report_scheduler.go) so a full sequential pass — one LLM
// call per active user — can finish even with many users.
const reportBatchGenerateTimeout = 30 * time.Minute

// GenerateAll handles generating reports for all active users (manual trigger)
// POST /api/v1/admin/reports/generate-all
func (h *ReportHandler) GenerateAll(c *gin.Context) {
	var req generateAllReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	ref, ok := reportRefDate(c, req.Date)
	if !ok {
		return
	}

	// Detach from the request context: the batch runs one LLM call per user and
	// can outlive the HTTP request. If the client (or its 30s axios timeout)
	// disconnects, cancelling the request context would abort the remaining
	// users' reports. Run on a background context so the batch always completes.
	ctx, cancel := context.WithTimeout(context.Background(), reportBatchGenerateTimeout)
	defer cancel()

	generated, firstErr := h.reportService.GenerateForAllUsers(ctx, req.Type, ref, service.ReportTriggerManual, false)
	if generated == 0 && firstErr != nil {
		response.ErrorFrom(c, firstErr)
		return
	}
	response.Success(c, gin.H{
		"generated": generated,
		"error":     errorString(firstErr),
	})
}

// GetConfig handles reading report LLM config
// GET /api/v1/admin/reports/config
func (h *ReportHandler) GetConfig(c *gin.Context) {
	cfg, err := h.reportService.GetReportConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, redactReportConfig(cfg))
}

type updateReportConfigRequest struct {
	Enabled             *bool   `json:"enabled"`
	BaseURL             *string `json:"base_url"`
	APIKey              *string `json:"api_key"`
	Model               *string `json:"model"`
	MaxPrompts          *int    `json:"max_prompts"`
	PromptTruncateChars *int    `json:"prompt_truncate_chars"`
	DailySchedule       *string `json:"daily_schedule"`
	WeeklySchedule      *string `json:"weekly_schedule"`
	MonthlySchedule     *string `json:"monthly_schedule"`
	SkipHolidays        *bool   `json:"skip_holidays"`
	FeishuEnabled       *bool   `json:"feishu_enabled"`
	// FeishuWebhookURL / FeishuSecret 为空时表示保留旧值（与 api_key 同语义）
	FeishuWebhookURL  *string `json:"feishu_webhook_url"`
	FeishuSecret      *string `json:"feishu_secret"`
	FeishuPushDaily   *bool   `json:"feishu_push_daily"`
	FeishuPushWeekly  *bool   `json:"feishu_push_weekly"`
	FeishuPushMonthly *bool   `json:"feishu_push_monthly"`
}

// UpdateConfig handles updating report LLM config
// PUT /api/v1/admin/reports/config
func (h *ReportHandler) UpdateConfig(c *gin.Context) {
	var req updateReportConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	cfg, err := h.reportService.GetReportConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	if req.Enabled != nil {
		cfg.Enabled = *req.Enabled
	}
	if req.BaseURL != nil {
		cfg.BaseURL = *req.BaseURL
	}
	if req.APIKey != nil && *req.APIKey != "" {
		cfg.APIKey = *req.APIKey
	}
	if req.Model != nil {
		cfg.Model = *req.Model
	}
	if req.MaxPrompts != nil {
		cfg.MaxPrompts = *req.MaxPrompts
	}
	if req.PromptTruncateChars != nil {
		cfg.PromptTruncateChars = *req.PromptTruncateChars
	}
	if req.DailySchedule != nil {
		cfg.DailySchedule = *req.DailySchedule
	}
	if req.WeeklySchedule != nil {
		cfg.WeeklySchedule = *req.WeeklySchedule
	}
	if req.MonthlySchedule != nil {
		cfg.MonthlySchedule = *req.MonthlySchedule
	}
	if req.SkipHolidays != nil {
		cfg.SkipHolidays = *req.SkipHolidays
	}
	if req.FeishuEnabled != nil {
		cfg.FeishuEnabled = *req.FeishuEnabled
	}
	if req.FeishuWebhookURL != nil && *req.FeishuWebhookURL != "" {
		cfg.FeishuWebhookURL = *req.FeishuWebhookURL
	}
	if req.FeishuSecret != nil && *req.FeishuSecret != "" {
		cfg.FeishuSecret = *req.FeishuSecret
	}
	if req.FeishuPushDaily != nil {
		cfg.FeishuPushDaily = *req.FeishuPushDaily
	}
	if req.FeishuPushWeekly != nil {
		cfg.FeishuPushWeekly = *req.FeishuPushWeekly
	}
	if req.FeishuPushMonthly != nil {
		cfg.FeishuPushMonthly = *req.FeishuPushMonthly
	}

	updated, err := h.reportService.UpdateReportConfig(c.Request.Context(), cfg)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, redactReportConfig(updated))
}

// redactReportConfig 返回给前端时隐藏 API Key 与飞书 webhook/加签密钥。
func redactReportConfig(cfg *service.ReportLLMConfig) *service.ReportLLMConfig {
	if cfg == nil {
		return nil
	}
	out := *cfg
	if out.APIKey != "" {
		out.APIKey = "********"
	}
	if out.FeishuWebhookURL != "" {
		out.FeishuWebhookURL = "********"
	}
	if out.FeishuSecret != "" {
		out.FeishuSecret = "********"
	}
	return &out
}

func parseReportFilters(c *gin.Context) (service.ReportListFilters, bool) {
	filters := service.ReportListFilters{
		Type: strings.TrimSpace(c.Query("type")),
	}
	if filters.Type != "" && filters.Type != service.ReportTypeDaily && filters.Type != service.ReportTypeWeekly && filters.Type != service.ReportTypeMonthly {
		response.BadRequest(c, "type must be daily, weekly or monthly")
		return filters, false
	}

	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		uid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || uid <= 0 {
			response.BadRequest(c, "Invalid user_id")
			return filters, false
		}
		filters.UserID = uid
	}

	if raw := strings.TrimSpace(c.Query("date")); raw != "" {
		day, err := timezone.ParseInLocation("2006-01-02", raw)
		if err != nil {
			response.BadRequest(c, "Invalid date, expect YYYY-MM-DD")
			return filters, false
		}
		filters.StartDate = day
		filters.EndDate = day.AddDate(0, 0, 1)
	}

	return filters, true
}

func reportRefDate(c *gin.Context, raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return timezone.Now(), true
	}
	ref, err := timezone.ParseInLocation("2006-01-02", raw)
	if err != nil {
		response.BadRequest(c, "Invalid date, expect YYYY-MM-DD")
		return time.Time{}, false
	}
	return ref, true
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
