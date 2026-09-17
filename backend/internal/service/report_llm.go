package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ReportLLMConfig 日报/周报功能的管理配置（存 settings 表，key=report_config）。
type ReportLLMConfig struct {
	// 总开关（关闭后 scheduler 不跑，手动生成仍可用）
	Enabled bool `json:"enabled"`
	// OpenAI 兼容接口配置
	BaseURL string `json:"base_url"` // 如 https://dashscope.aliyuncs.com/compatible-mode/v1
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
	// 上下文预算
	MaxPrompts          int `json:"max_prompts"`           // 每次总结最多取多少条 prompt（默认 30）
	PromptTruncateChars int `json:"prompt_truncate_chars"` // 单条 prompt 截断长度（默认 500）
	// 定时计划（cron，5 段式：分 时 日 月 周）
	DailySchedule   string `json:"daily_schedule"`   // 默认 "0 20 * * *"
	WeeklySchedule  string `json:"weekly_schedule"`  // 默认 "10 20 * * 5"
	MonthlySchedule string `json:"monthly_schedule"` // 默认 "20 20 1 * *"（每月 1 日生成上月）
	// 飞书推送（群自定义机器人 Webhook）
	FeishuEnabled     bool   `json:"feishu_enabled"`
	FeishuWebhookURL  string `json:"feishu_webhook_url"`
	FeishuSecret      string `json:"feishu_secret"` // 可选：加签密钥
	FeishuPushDaily   bool   `json:"feishu_push_daily"`
	FeishuPushWeekly  bool   `json:"feishu_push_weekly"`
	FeishuPushMonthly bool   `json:"feishu_push_monthly"`
}

func defaultReportLLMConfig() *ReportLLMConfig {
	return &ReportLLMConfig{
		Enabled:             false,
		BaseURL:             "",
		APIKey:              "",
		Model:               "",
		MaxPrompts:          30,
		PromptTruncateChars: 500,
		DailySchedule:       "0 20 * * *",
		WeeklySchedule:      "10 20 * * 5",
		MonthlySchedule:     "20 20 1 * *",
	}
}

func normalizeReportLLMConfig(cfg *ReportLLMConfig) {
	if cfg == nil {
		return
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.Model = strings.TrimSpace(cfg.Model)
	if cfg.MaxPrompts <= 0 {
		cfg.MaxPrompts = 30
	}
	if cfg.MaxPrompts > 200 {
		cfg.MaxPrompts = 200
	}
	if cfg.PromptTruncateChars <= 0 {
		cfg.PromptTruncateChars = 500
	}
	if cfg.PromptTruncateChars > 8000 {
		cfg.PromptTruncateChars = 8000
	}
	cfg.DailySchedule = strings.TrimSpace(cfg.DailySchedule)
	if cfg.DailySchedule == "" {
		cfg.DailySchedule = "0 20 * * *"
	}
	cfg.WeeklySchedule = strings.TrimSpace(cfg.WeeklySchedule)
	if cfg.WeeklySchedule == "" {
		cfg.WeeklySchedule = "10 20 * * 5"
	}
	cfg.MonthlySchedule = strings.TrimSpace(cfg.MonthlySchedule)
	if cfg.MonthlySchedule == "" {
		cfg.MonthlySchedule = "20 20 1 * *"
	}
	cfg.FeishuWebhookURL = strings.TrimSpace(cfg.FeishuWebhookURL)
	cfg.FeishuSecret = strings.TrimSpace(cfg.FeishuSecret)
}

// GetReportConfig 读取报告配置（无配置时返回默认值）。
func (s *ReportService) GetReportConfig(ctx context.Context) (*ReportLLMConfig, error) {
	defaultCfg := defaultReportLLMConfig()
	if s == nil || s.settingRepo == nil {
		return defaultCfg, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	raw, err := s.settingRepo.GetValue(ctx, SettingKeyReportConfig)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return defaultCfg, nil
		}
		return nil, err
	}

	cfg := &ReportLLMConfig{}
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return defaultCfg, nil
	}
	normalizeReportLLMConfig(cfg)
	return cfg, nil
}

// UpdateReportConfig 更新报告配置。
func (s *ReportService) UpdateReportConfig(ctx context.Context, cfg *ReportLLMConfig) (*ReportLLMConfig, error) {
	if s == nil || s.settingRepo == nil {
		return nil, errors.New("setting repository not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg == nil {
		return nil, errors.New("invalid config")
	}

	normalizeReportLLMConfig(cfg)
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyReportConfig, string(raw)); err != nil {
		return nil, err
	}
	return cfg, nil
}

// =========================
// OpenAI 兼容客户端（最小实现）
// =========================

type reportLLMClient struct {
	httpClient *http.Client
}

type reportLLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type reportLLMChatRequest struct {
	Model       string              `json:"model"`
	Messages    []reportLLMMessage  `json:"messages"`
	Temperature float64             `json:"temperature"`
	MaxTokens   int                 `json:"max_tokens,omitempty"`
}

type reportLLMChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ChatComplete 调用 OpenAI 兼容的 /chat/completions 接口生成文本。
func (c *reportLLMClient) ChatComplete(ctx context.Context, cfg *ReportLLMConfig, systemPrompt, userPrompt string) (string, error) {
	if cfg == nil || cfg.BaseURL == "" || cfg.APIKey == "" || cfg.Model == "" {
		return "", ErrReportLLMNotConfigured
	}

	reqBody := reportLLMChatRequest{
		Model: cfg.Model,
		Messages: []reportLLMMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.3,
		MaxTokens:   4096,
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	url := cfg.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrReportLLMCallFailed, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("%w: read response: %v", ErrReportLLMCallFailed, err)
	}

	parsed := &reportLLMChatResponse{}
	if err := json.Unmarshal(body, parsed); err != nil {
		return "", fmt.Errorf("%w: invalid response (http %d)", ErrReportLLMCallFailed, resp.StatusCode)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", fmt.Errorf("%w: %s", ErrReportLLMCallFailed, parsed.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: http %d", ErrReportLLMCallFailed, resp.StatusCode)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("%w: empty choices", ErrReportLLMCallFailed)
	}

	content := strings.TrimSpace(stripReasoningTrace(parsed.Choices[0].Message.Content))
	if content == "" {
		return "", fmt.Errorf("%w: empty content", ErrReportLLMCallFailed)
	}
	return content, nil
}

// stripReasoningTrace 剥离思考型模型（如 MiniMax-M2）输出中的 <think>...</think>
// 区块，避免思考过程混进报表 Markdown。
func stripReasoningTrace(s string) string {
	for {
		start := strings.Index(s, "<think>")
		if start < 0 {
			return s
		}
		end := strings.Index(s[start:], "</think>")
		if end < 0 {
			// 未闭合：截掉开标签及之后的内容
			return strings.TrimSpace(s[:start])
		}
		s = s[:start] + s[start+end+len("</think>"):]
	}
}
