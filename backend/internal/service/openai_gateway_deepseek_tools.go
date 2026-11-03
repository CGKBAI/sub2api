package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type DeepSeekToolEndpoint string

const (
	DeepSeekToolSearch DeepSeekToolEndpoint = "search"
	DeepSeekToolFetch  DeepSeekToolEndpoint = "fetch"

	deepSeekToolSearchPath = "/v1/tools/search"
	deepSeekToolFetchPath  = "/v1/tools/fetch"

	// DeepSeek charges both standalone tools at ¥5 per 1,000 successful calls.
	defaultDeepSeekToolPricePerCall = 0.005
)

func (e DeepSeekToolEndpoint) Valid() bool {
	return e == DeepSeekToolSearch || e == DeepSeekToolFetch
}

func (e DeepSeekToolEndpoint) Path() string {
	switch e {
	case DeepSeekToolSearch:
		return deepSeekToolSearchPath
	case DeepSeekToolFetch:
		return deepSeekToolFetchPath
	default:
		return ""
	}
}

func (e DeepSeekToolEndpoint) UsageModel() string {
	switch e {
	case DeepSeekToolSearch:
		return "deepseek-search"
	case DeepSeekToolFetch:
		return "deepseek-fetch"
	default:
		return "deepseek-tool"
	}
}

// ForwardDeepSeekTool proxies one standalone DeepSeek Search/Fetch request.
//
// The request and successful response are forwarded byte-for-byte. A
// non-billable HTTP 200 response is still returned to the client but produces a
// nil result, so the handler does not create a usage log for empty searches or
// pages without extractable content.
func (s *OpenAIGatewayService) ForwardDeepSeekTool(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	endpoint DeepSeekToolEndpoint,
	body []byte,
) (*OpenAIForwardResult, error) {
	if s == nil || c == nil || account == nil {
		return nil, fmt.Errorf("service, context, and account are required")
	}
	if account.Platform != PlatformDeepseek || account.Type != AccountTypeAPIKey {
		return nil, deepSeekToolFailoverError(http.StatusBadGateway, "DeepSeek tool endpoint requires a DeepSeek API-key account")
	}
	if !endpoint.Valid() {
		return nil, fmt.Errorf("unsupported DeepSeek tool endpoint %q", endpoint)
	}

	apiKey := strings.TrimSpace(account.GetOpenAIProtocolAPIKey())
	if apiKey == "" {
		return nil, deepSeekToolFailoverError(http.StatusBadGateway, "DeepSeek account is missing api_key")
	}
	baseURL := strings.TrimSpace(account.GetOpenAIFormatBaseURL())
	if baseURL == "" {
		baseURL = DefaultDeepseekBaseURL
	}
	validatedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return nil, deepSeekToolFailoverError(http.StatusBadGateway, err.Error())
	}
	targetURL := buildOpenAIEndpointURL(validatedBaseURL, endpoint.Path())

	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	defer releaseUpstreamCtx()
	req, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, deepSeekToolFailoverError(http.StatusBadGateway, "Failed to build DeepSeek tool request")
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	account.ApplyHeaderOverrides(req.Header)

	startTime := time.Now()
	resp, err := s.doOpenAIUpstream(req, resolveAccountProxyURL(account), account)
	if err != nil {
		return nil, deepSeekToolFailoverError(http.StatusBadGateway, "DeepSeek tool upstream request failed")
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, fmt.Errorf("read DeepSeek tool response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		s.handleDeepSeekToolUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
		upstreamMessage := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
		setOpsUpstreamError(c, resp.StatusCode, upstreamMessage, "")
		return nil, &UpstreamFailoverError{
			StatusCode:      resp.StatusCode,
			ResponseBody:    append([]byte(nil), respBody...),
			ResponseHeaders: resp.Header.Clone(),
		}
	}

	writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(resp.StatusCode, contentType, respBody)

	if !isBillableDeepSeekToolResponse(endpoint, respBody) {
		return nil, nil
	}
	return &OpenAIForwardResult{
		Model:            endpoint.UsageModel(),
		BillingModel:     endpoint.UsageModel(),
		UpstreamModel:    endpoint.UsageModel(),
		UpstreamEndpoint: endpoint.Path(),
		WebSearchCalls:   1,
		Duration:         time.Since(startTime),
	}, nil
}

func (s *OpenAIGatewayService) handleDeepSeekToolUpstreamError(
	ctx context.Context,
	account *Account,
	statusCode int,
	headers http.Header,
	responseBody []byte,
) {
	if s == nil || s.rateLimitService == nil || account == nil {
		return
	}
	switch statusCode {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusTooManyRequests:
		_ = s.rateLimitService.HandleUpstreamError(ctx, account, statusCode, headers, responseBody)
	}
}

func isBillableDeepSeekToolResponse(endpoint DeepSeekToolEndpoint, body []byte) bool {
	switch endpoint {
	case DeepSeekToolSearch:
		results := gjson.GetBytes(body, "results")
		return results.IsArray() && len(results.Array()) > 0
	case DeepSeekToolFetch:
		return strings.TrimSpace(gjson.GetBytes(body, "content").String()) != ""
	default:
		return false
	}
}

func deepSeekToolFailoverError(statusCode int, message string) *UpstreamFailoverError {
	body := []byte(fmt.Sprintf(`{"error":{"type":"upstream_error","message":%q}}`, message))
	return &UpstreamFailoverError{
		StatusCode:   statusCode,
		ResponseBody: body,
		ResponseHeaders: http.Header{
			"Content-Type": []string{"application/json; charset=utf-8"},
		},
	}
}
