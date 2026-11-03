package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestDeepSeekToolRetryableStatusClassification(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		require.True(t, isDeepSeekToolRetryableStatus(status), "status=%d", status)
	}
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity} {
		require.False(t, isDeepSeekToolRetryableStatus(status), "status=%d", status)
	}
}

func TestWriteDeepSeekToolRawErrorPreservesUpstreamResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/tools/search", nil)
	body := []byte("{\n  \"error\": {\"message\": \"invalid query\"}\n}")

	writeDeepSeekToolRawError(c, &service.UpstreamFailoverError{
		StatusCode:   http.StatusUnprocessableEntity,
		ResponseBody: body,
		ResponseHeaders: http.Header{
			"Content-Type": []string{"application/problem+json"},
			"Retry-After":  []string{"30"},
			"X-Request-Id": []string{"deepseek-upstream-request"},
		},
	})

	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code)
	require.Equal(t, "application/problem+json", recorder.Header().Get("Content-Type"))
	require.Equal(t, "30", recorder.Header().Get("Retry-After"))
	require.Equal(t, "deepseek-upstream-request", recorder.Header().Get("X-Request-Id"))
	require.Equal(t, body, recorder.Body.Bytes())
}
