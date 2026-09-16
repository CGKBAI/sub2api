package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

const (
	ReportTypeDaily   = domain.ReportTypeDaily
	ReportTypeWeekly  = domain.ReportTypeWeekly
	ReportTypeMonthly = domain.ReportTypeMonthly

	ReportStatusPending = domain.ReportStatusPending
	ReportStatusDone    = domain.ReportStatusDone
	ReportStatusFailed  = domain.ReportStatusFailed
)

var (
	ErrReportNotFound         = domain.ErrReportNotFound
	ErrReportInvalidType      = domain.ErrReportInvalidType
	ErrReportInvalidPeriod    = domain.ErrReportInvalidPeriod
	ErrReportLLMNotConfigured = domain.ErrReportLLMNotConfigured
	ErrReportLLMCallFailed    = domain.ErrReportLLMCallFailed
)

type Report = domain.Report

type ReportStats = domain.ReportStats

// ReportListFilters 报告列表过滤条件。
type ReportListFilters struct {
	Type      string
	UserID    int64 // 0 = 全部
	StartDate time.Time
	EndDate   time.Time
}

// ReportRepository 是报告实体的持久化接口。
type ReportRepository interface {
	Create(ctx context.Context, r *Report) error
	Update(ctx context.Context, r *Report) error
	GetByID(ctx context.Context, id int64) (*Report, error)
	GetByUserPeriod(ctx context.Context, userID int64, reportType string, periodStart time.Time) (*Report, error)
	List(ctx context.Context, params pagination.PaginationParams, filters ReportListFilters) ([]Report, *pagination.PaginationResult, error)

	// AggregateUserUsage 聚合单个用户在时间窗内的用量统计（usage_logs）。
	AggregateUserUsage(ctx context.Context, userID int64, start, end time.Time) (ReportStats, error)
	// FetchUserPrompts 拉取用户在时间窗内的 prompt 片段（prompt_audit_events）。
	FetchUserPrompts(ctx context.Context, userID int64, start, end time.Time, limit int) ([]UserPromptSnippet, error)
	// ListActiveUserIDs 列出时间窗内有用量的用户 ID。
	ListActiveUserIDs(ctx context.Context, start, end time.Time) ([]int64, error)
	// GetUsername 查询用户显示名（users.username，用于报告标题）。
	GetUsername(ctx context.Context, userID int64) (string, error)
}

// UserPromptSnippet 是用于 LLM 总结的单条 prompt 片段。
type UserPromptSnippet struct {
	Model     string
	CreatedAt time.Time
	Content   string
}

// ReportGenerateResult 手动/定时生成的结果。
type ReportGenerateResult struct {
	Report *Report
	Err    error
}

var (
	ErrReportGenerateUserNotFound = infraerrors.BadRequest("REPORT_USER_NOT_FOUND", "no usage found for this user in the period")
)
