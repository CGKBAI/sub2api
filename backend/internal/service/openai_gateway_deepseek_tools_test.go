package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestDeepSeekToolCapabilityRequiresDeepSeekAPIKeyAccount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		account *Account
		want    bool
	}{
		{name: "deepseek API key", account: &Account{Platform: PlatformDeepseek, Type: AccountTypeAPIKey}, want: true},
		{name: "deepseek oauth", account: &Account{Platform: PlatformDeepseek, Type: AccountTypeOAuth}},
		{name: "deepseek upstream", account: &Account{Platform: PlatformDeepseek, Type: AccountTypeUpstream}},
		{name: "openai API key", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}},
		{name: "nil account"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, tt.account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityDeepSeekTools))
		})
	}
}

func TestDeepSeekToolSchedulerSelectsOnlyDeepSeekAPIKeyAccounts(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()

	groupID := int64(79)
	oauth := Account{
		ID: 301, Platform: PlatformDeepseek, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
	}
	apiKey := Account{
		ID: 302, Platform: PlatformDeepseek, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 1,
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	newSvc := func(accounts []Account) *OpenAIGatewayService {
		return &OpenAIGatewayService{
			accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
			cache:              &schedulerTestGatewayCache{},
			cfg:                cfg,
			concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		}
	}

	selection, _, err := newSvc([]Account{oauth, apiKey}).SelectAccountWithSchedulerForCapability(
		context.Background(), &groupID, "", "", "", nil,
		OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityDeepSeekTools,
		false, false, false, PlatformDeepseek,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, apiKey.ID, selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}

	selection, _, err = newSvc([]Account{oauth}).SelectAccountWithSchedulerForCapability(
		context.Background(), &groupID, "", "", "", nil,
		OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityDeepSeekTools,
		false, false, false, PlatformDeepseek,
	)
	require.Error(t, err)
	require.Nil(t, selection)
}

func TestForwardDeepSeekToolForwardsWireAndBillsSuccessfulResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		endpoint DeepSeekToolEndpoint
		request  []byte
		response []byte
		wantPath string
	}{
		{
			name:     "search",
			endpoint: DeepSeekToolSearch,
			request:  []byte("{\n  \"query\": \"latest AI news\",\n  \"future_field\": {\"keep\": true}\n}"),
			response: []byte("{\n  \"results\": [{\"title\": \"AI news\", \"url\": \"https://example.com\"}],\n  \"future_field\": 42\n}"),
			wantPath: "/v1/tools/search",
		},
		{
			name:     "fetch",
			endpoint: DeepSeekToolFetch,
			request:  []byte("{\n  \"url\": \"https://example.com/article\",\n  \"future_field\": [1, 2, 3]\n}"),
			response: []byte("{\n  \"content\": \"page body\",\n  \"future_field\": \"keep\"\n}"),
			wantPath: "/v1/tools/fetch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/tools/"+string(tt.endpoint), bytes.NewReader(tt.request))

			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json; charset=utf-8"},
					"X-Request-Id": []string{"deepseek-tool-request"},
				},
				Body: io.NopCloser(bytes.NewReader(tt.response)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{
				ID:          101,
				Platform:    PlatformDeepseek,
				Type:        AccountTypeAPIKey,
				Concurrency: 1,
				Credentials: map[string]any{
					"api_key":  "sk-deepseek",
					"base_url": "https://relay.example.com/v1/",
				},
			}

			result, err := svc.ForwardDeepSeekTool(context.Background(), c, account, tt.endpoint, tt.request)

			require.NoError(t, err)
			require.NotNil(t, upstream.lastReq)
			require.Equal(t, tt.wantPath, upstream.lastReq.URL.Path)
			require.Equal(t, "Bearer sk-deepseek", upstream.lastReq.Header.Get("Authorization"))
			require.Equal(t, "application/json", upstream.lastReq.Header.Get("Content-Type"))
			require.Equal(t, tt.request, upstream.lastBody, "request body must be forwarded byte-for-byte")
			require.Equal(t, tt.response, recorder.Body.Bytes(), "upstream response body must be returned byte-for-byte")
			require.Equal(t, "application/json; charset=utf-8", recorder.Header().Get("Content-Type"))
			require.NotNil(t, result)
			require.Equal(t, 1, result.WebSearchCalls)
			require.Equal(t, tt.endpoint.Path(), result.UpstreamEndpoint)
		})
	}
}

func TestForwardDeepSeekToolDoesNotBillEmptySuccessButStillReturnsRawResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		endpoint DeepSeekToolEndpoint
		response []byte
	}{
		{name: "search without results", endpoint: DeepSeekToolSearch, response: []byte("{\n  \"results\": []\n}")},
		{name: "search missing results", endpoint: DeepSeekToolSearch, response: []byte("{\"request_id\":\"empty\"}")},
		{name: "fetch without content", endpoint: DeepSeekToolFetch, response: []byte("{\n  \"content\": \"  \"\n}")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/tools/"+string(tt.endpoint), strings.NewReader(`{"query":"empty"}`))
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(tt.response)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{
				ID:          102,
				Platform:    PlatformDeepseek,
				Type:        AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "sk-deepseek", "base_url": "https://api.deepseek.com"},
			}

			result, err := svc.ForwardDeepSeekTool(context.Background(), c, account, tt.endpoint, []byte(`{"query":"empty"}`))

			require.NoError(t, err)
			require.Nil(t, result, "empty successful responses must not create billable usage")
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, tt.response, recorder.Body.Bytes())
		})
	}
}

func TestForwardDeepSeekToolReturnsRawUpstreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"query":"bad request"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/tools/search", bytes.NewReader(body))
	upstreamBody := []byte("{\n  \"error\": {\"message\": \"invalid query\"}\n}")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusUnprocessableEntity,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"deepseek-error-request"},
		},
		Body: io.NopCloser(bytes.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID:          103,
		Platform:    PlatformDeepseek,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-deepseek", "base_url": "https://api.deepseek.com"},
	}

	result, err := svc.ForwardDeepSeekTool(context.Background(), c, account, DeepSeekToolSearch, body)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusUnprocessableEntity, failoverErr.StatusCode)
	require.Equal(t, upstreamBody, failoverErr.ResponseBody)
	require.Equal(t, "deepseek-error-request", failoverErr.ResponseHeaders.Get("X-Request-Id"))
	require.False(t, c.Writer.Written(), "service layer must leave non-2xx response writing to the handler")
}

type deepSeekToolRateLimitRepo struct {
	AccountRepository
	setErrorCalls       int
	setTempCalls        int
	setRateLimitedCalls int
	updateExtraCalls    int
}

func (r *deepSeekToolRateLimitRepo) SetError(context.Context, int64, string) error {
	r.setErrorCalls++
	return nil
}

func (r *deepSeekToolRateLimitRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.setTempCalls++
	return nil
}

func (r *deepSeekToolRateLimitRepo) SetRateLimited(context.Context, int64, time.Time) error {
	r.setRateLimitedCalls++
	return nil
}

func (r *deepSeekToolRateLimitRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	r.updateExtraCalls++
	return nil
}

func TestHandleDeepSeekToolUpstreamErrorReusesAccountStatePolicy(t *testing.T) {
	tests := []struct {
		name            string
		status          int
		body            string
		wantError       int
		wantTemp        int
		wantRateLimited int
		wantUpdateExtra int
	}{
		{name: "401 marks account error", status: http.StatusUnauthorized, body: `{"error":{"message":"invalid key"}}`, wantError: 1},
		{name: "402 pauses insufficient balance", status: http.StatusPaymentRequired, body: `{"error":{"message":"insufficient balance"}}`, wantTemp: 1, wantUpdateExtra: 1},
		{name: "429 cools down", status: http.StatusTooManyRequests, body: `{"error":{"message":"rate limited"}}`, wantRateLimited: 1},
		{name: "408 is retry-only", status: http.StatusRequestTimeout, body: `{"error":{"message":"timeout"}}`},
		{name: "5xx is retry-only", status: http.StatusBadGateway, body: `{"error":{"message":"bad gateway"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &deepSeekToolRateLimitRepo{}
			svc := &OpenAIGatewayService{rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil)}
			account := &Account{ID: 201, Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-deepseek"}}

			svc.handleDeepSeekToolUpstreamError(context.Background(), account, tt.status, http.Header{}, []byte(tt.body))

			require.Equal(t, tt.wantError, repo.setErrorCalls)
			require.Equal(t, tt.wantTemp, repo.setTempCalls)
			require.Equal(t, tt.wantRateLimited, repo.setRateLimitedCalls)
			require.Equal(t, tt.wantUpdateExtra, repo.updateExtraCalls)
		})
	}
}

func TestDeepSeekToolBillingDefaultsAndOverrides(t *testing.T) {
	t.Parallel()

	svc := &OpenAIGatewayService{billingService: &BillingService{}}
	groupID := int64(77)
	apiKey := &APIKey{
		ID:      1,
		GroupID: &groupID,
		Group:   &Group{ID: groupID, Platform: PlatformDeepseek},
	}
	result := &OpenAIForwardResult{Model: "deepseek-search", WebSearchCalls: 1}

	cost, err := svc.calculateOpenAIRecordUsageCost(
		context.Background(), result, apiKey, []string{"deepseek-search"},
		3.0, 1.0, 1.0, 1.0, UsageTokens{}, "", boolPtr(false), false, time.Time{},
	)
	require.NoError(t, err)
	require.Equal(t, string(BillingModePerRequest), cost.BillingMode)
	require.InDelta(t, 0.005, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.005, cost.ActualCost, 1e-12)

	apiKey.Group.WebSearchPricePerCall = float64Ptr(0.007)
	cost, err = svc.calculateOpenAIRecordUsageCost(
		context.Background(), result, apiKey, []string{"deepseek-search"},
		3.0, 1.0, 1.0, 2.0, UsageTokens{}, "", boolPtr(false), false, time.Time{},
	)
	require.NoError(t, err)
	require.InDelta(t, 0.007, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.014, cost.ActualCost, 1e-12)

	apiKey.Group.WebSearchPricePerCall = float64Ptr(0)
	cost, err = svc.calculateOpenAIRecordUsageCost(
		context.Background(), result, apiKey, []string{"deepseek-search"},
		3.0, 1.0, 1.0, 2.0, UsageTokens{}, "", boolPtr(false), false, time.Time{},
	)
	require.NoError(t, err)
	require.Zero(t, cost.TotalCost)
	require.Zero(t, cost.ActualCost)

	apiKey.Group.Platform = PlatformOpenAI
	apiKey.Group.WebSearchPricePerCall = nil
	cost, err = svc.calculateOpenAIRecordUsageCost(
		context.Background(), result, apiKey, []string{"gpt-5.6-sol"},
		3.0, 1.0, 1.0, 1.0, UsageTokens{}, "", boolPtr(false), false, time.Time{},
	)
	require.NoError(t, err)
	require.InDelta(t, 0.01, cost.TotalCost, 1e-12, "OpenAI web search default must remain unchanged")
}

func TestDeepSeekToolInflightEstimateUsesPlatformDefaultAndMultiplier(t *testing.T) {
	t.Parallel()

	groupID := int64(78)
	apiKey := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformDeepseek}}
	deps := inflightEstimateDeps{billing: &BillingService{}}

	cost := deps.estimateOne(
		context.Background(),
		apiKey,
		"deepseek-search",
		InflightEstimateRequest{Kind: InflightEstimatePerRequest, WebSearchCalls: 1},
		2,
		1,
	)

	require.InDelta(t, 0.01, cost, 1e-12)
}
