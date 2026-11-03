package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// DeepSeekTool proxies the standalone DeepSeek Search and Fetch APIs.
// Only DeepSeek API-key groups may use these endpoints.
func (h *OpenAIGatewayHandler) DeepSeekTool(c *gin.Context, endpoint service.DeepSeekToolEndpoint) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.Group == nil {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	if apiKey.Group.Platform != service.PlatformDeepseek {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Search and Fetch APIs are only available for DeepSeek groups")
		return
	}
	if !endpoint.Valid() {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Unknown DeepSeek tool endpoint")
		return
	}
	if apiKey.GroupID == nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "group required")
		return
	}

	subject, _ := middleware2.GetAuthSubjectFromContext(c)
	reqLog := requestLogger(
		c,
		"handler.openai_gateway.deepseek_tools",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
		zap.String("deepseek_tool", string(endpoint)),
	)
	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}

	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	// Search queries are user prompts and must pass the same prompt audit gate as
	// text-generation traffic. Fetch only accepts URLs; pages are returned by the
	// upstream and are not a user-authored prompt, so it is intentionally excluded.
	if endpoint == service.DeepSeekToolSearch {
		if query := strings.TrimSpace(gjson.GetBytes(body, "query").String()); query != "" {
			auditBody, _ := json.Marshal(map[string]any{
				"messages": []map[string]any{{
					"role": "user", "content": query,
				}},
			})
			if decision := h.checkSecurityAudit(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIChat, endpoint.UsageModel(), auditBody); decision != nil && !decision.AllowNextStage {
				h.openAISecurityAuditError(c, decision)
				return
			}
		}
	}

	streamStarted := false
	userRelease, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, false, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userRelease != nil {
		defer userRelease()
	}

	usageModel := endpoint.UsageModel()
	setOpsRequestContext(c, usageModel, false)
	setOpsEndpointContext(c, "", int16(service.RequestTypeSync))

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}

	inflightDone, inflightErr := reserveInflightBalance(c, h.billingCacheService, h.gatewayService, apiKey, subscription, service.InflightEstimateRequest{
		Model:          usageModel,
		Kind:           service.InflightEstimatePerRequest,
		WebSearchCalls: 1,
	})
	if inflightErr != nil {
		status, code, message, retryAfter := billingErrorDetails(inflightErr)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}
	defer inflightDone()

	pricingCtx, _ := h.gatewayService.WithOpenAIRequestPricingContext(c.Request.Context(), apiKey.GroupID)
	c.Request = c.Request.WithContext(pricingCtx)

	maxAccountSwitches := h.maxAccountSwitches
	if maxAccountSwitches <= 0 {
		maxAccountSwitches = 3
	}
	failedAccountIDs := make(map[int64]struct{})
	var lastFailoverErr *service.UpstreamFailoverError
	switchCount := 0

	for {
		selection, _, selectErr := h.gatewayService.SelectAccountWithSchedulerForCapability(
			c.Request.Context(),
			apiKey.GroupID,
			"",
			"",
			"",
			failedAccountIDs,
			service.OpenAIUpstreamTransportHTTPSSE,
			service.OpenAIEndpointCapabilityDeepSeekTools,
			false,
			false,
			false,
			service.PlatformDeepseek,
		)
		if selectErr != nil || selection == nil || selection.Account == nil {
			if lastFailoverErr != nil {
				writeDeepSeekToolRawError(c, lastFailoverErr)
				return
			}
			if failoverClientGone(c) {
				return
			}
			reqLog.Warn("deepseek_tools.account_select_failed", zap.Error(selectErr))
			markOpsRoutingCapacityLimitedIfNoAvailable(c, selectErr)
			h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "No available DeepSeek accounts")
			return
		}

		account := selection.Account
		if !isDeepSeekToolAccount(account) {
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			failedAccountIDs[account.ID] = struct{}{}
			continue
		}
		setOpsSelectedAccount(c, account.ID, account.Platform)

		accountRelease, slotResult := h.acquireOpenAIAccountSlot(c, apiKey.GroupID, "", selection, false, &streamStarted, reqLog, nil)
		if slotResult != openAISlotAcquireOK {
			return
		}

		writerSizeBeforeForward := c.Writer.Size()
		result, forwardErr := func() (*service.OpenAIForwardResult, error) {
			if accountRelease != nil {
				defer accountRelease()
			}
			return h.gatewayService.ForwardDeepSeekTool(c.Request.Context(), c, account, endpoint, body)
		}()

		if forwardErr == nil {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account, usageModel, true, nil)
			if result != nil {
				h.recordDeepSeekToolUsage(c, apiKey, account, subscription, endpoint, body, result, subject.UserID)
			}
			return
		}

		var failoverErr *service.UpstreamFailoverError
		if !errors.As(forwardErr, &failoverErr) {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account, usageModel, false, nil, forwardErr)
			if c.Writer.Size() == writerSizeBeforeForward && !failoverClientGone(c) {
				reqLog.Warn("deepseek_tools.forward_failed", zap.Int64("account_id", account.ID), zap.Error(forwardErr))
				h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
			}
			return
		}

		if c.Writer.Size() != writerSizeBeforeForward {
			return
		}
		if failoverClientGone(c) {
			return
		}

		if failoverErr.ShouldReportAccountScheduleFailure() {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account, usageModel, false, nil, failoverErr)
		}
		lastFailoverErr = failoverErr

		if !isDeepSeekToolRetryableStatus(failoverErr.StatusCode) {
			writeDeepSeekToolRawError(c, failoverErr)
			return
		}
		if switchCount >= maxAccountSwitches {
			writeDeepSeekToolRawError(c, failoverErr)
			return
		}

		h.gatewayService.RecordOpenAIAccountSwitch()
		failedAccountIDs[account.ID] = struct{}{}
		switchCount++
		reqLog.Warn("deepseek_tools.failover_switching",
			zap.Int64("account_id", account.ID),
			zap.Int("upstream_status", failoverErr.StatusCode),
			zap.Int("switch_count", switchCount),
		)
	}
}

func isDeepSeekToolAccount(account *service.Account) bool {
	return account != nil && account.Platform == service.PlatformDeepseek && account.Type == service.AccountTypeAPIKey
}

func isDeepSeekToolRetryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func writeDeepSeekToolRawError(c *gin.Context, failoverErr *service.UpstreamFailoverError) {
	if failoverErr == nil {
		c.Data(http.StatusBadGateway, "application/json; charset=utf-8", []byte(`{"error":{"type":"upstream_error","message":"Upstream request failed"}}`))
		return
	}
	copyFailoverRetryAfter(c, failoverErr.ResponseHeaders)
	if contentType := strings.TrimSpace(failoverErr.ResponseHeaders.Get("Content-Type")); contentType != "" {
		c.Header("Content-Type", contentType)
	}
	if requestID := strings.TrimSpace(failoverErr.ResponseHeaders.Get("x-request-id")); requestID != "" {
		c.Header("x-request-id", requestID)
	}
	status := failoverErr.StatusCode
	if status <= 0 {
		status = http.StatusBadGateway
	}
	body := failoverErr.ResponseBody
	if len(body) == 0 {
		body = []byte(`{"error":{"type":"upstream_error","message":"Upstream request failed"}}`)
	}
	if c.Writer.Header().Get("Content-Type") == "" {
		c.Header("Content-Type", "application/json; charset=utf-8")
	}
	c.Data(status, c.Writer.Header().Get("Content-Type"), body)
}

func (h *OpenAIGatewayHandler) recordDeepSeekToolUsage(
	c *gin.Context,
	apiKey *service.APIKey,
	account *service.Account,
	subscription *service.UserSubscription,
	endpoint service.DeepSeekToolEndpoint,
	body []byte,
	result *service.OpenAIForwardResult,
	userID int64,
) {
	userAgent := c.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(c)
	sessionID := service.ExtractClientSessionID(c)
	requestPayloadHash := service.HashUsageRequestPayload(body)
	inboundEndpoint := GetInboundEndpoint(c)
	upstreamEndpoint := endpoint.Path()
	quotaPlatform := service.QuotaPlatform(c.Request.Context(), apiKey)

	h.submitMandatoryUsageRecordTask(c.Request.Context(), func(ctx context.Context) {
		if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
			Result:             result,
			APIKey:             apiKey,
			User:               apiKey.User,
			Account:            account,
			Subscription:       subscription,
			InboundEndpoint:    inboundEndpoint,
			UpstreamEndpoint:   upstreamEndpoint,
			UserAgent:          userAgent,
			IPAddress:          clientIP,
			RequestPayloadHash: requestPayloadHash,
			APIKeyService:      h.apiKeyService,
			QuotaPlatform:      quotaPlatform,
			SessionID:          sessionID,
			PricingAt:          service.OpenAIPricingAtFromContext(c.Request.Context()),
		}); err != nil {
			logger.L().With(
				zap.String("component", "handler.openai_gateway.deepseek_tools"),
				zap.Int64("user_id", userID),
				zap.Int64("api_key_id", apiKey.ID),
				zap.Any("group_id", apiKey.GroupID),
				zap.String("deepseek_tool", string(endpoint)),
				zap.Int64("account_id", account.ID),
			).Error("deepseek_tools.record_usage_failed", zap.Error(err))
		}
	})
}
