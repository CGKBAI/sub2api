package handler

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// UserReportHandler handles user-facing daily/weekly reports
type UserReportHandler struct {
	reportService *service.ReportService
}

// NewUserReportHandler creates a new user report handler
func NewUserReportHandler(reportService *service.ReportService) *UserReportHandler {
	return &UserReportHandler{
		reportService: reportService,
	}
}

// List handles listing current user's reports
// GET /api/v1/user/reports?type=daily&date=YYYY-MM-DD
func (h *UserReportHandler) List(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}

	page, pageSize := response.ParsePagination(c)

	filters := service.ReportListFilters{
		Type: strings.TrimSpace(c.Query("type")),
	}
	if filters.Type != "" && filters.Type != service.ReportTypeDaily && filters.Type != service.ReportTypeWeekly && filters.Type != service.ReportTypeMonthly {
		response.BadRequest(c, "type must be daily, weekly or monthly")
		return
	}
	if raw := strings.TrimSpace(c.Query("date")); raw != "" {
		day, err := timezone.ParseInLocation("2006-01-02", raw)
		if err != nil {
			response.BadRequest(c, "Invalid date, expect YYYY-MM-DD")
			return
		}
		filters.StartDate = day
		filters.EndDate = day.AddDate(0, 0, 1)
	}

	params := pagination.PaginationParams{
		Page:     page,
		PageSize: pageSize,
	}

	// 强制只看自己
	items, paginationResult, err := h.reportService.ListUserReports(c.Request.Context(), subject.UserID, params, filters)
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
