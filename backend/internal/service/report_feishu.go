package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// reportFeishuPushTimeout 单次飞书 webhook 推送超时。
const reportFeishuPushTimeout = 10 * time.Second

// feishuCardMaxBytes 卡片正文字节上限（webhook 卡片整体约 30KB 限制，留余量）。
const feishuCardMaxBytes = 28000

// reportFeishuClient 飞书群自定义机器人 webhook 客户端。
type reportFeishuClient struct {
	httpClient *http.Client
}

type feishuCardText struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

type feishuCardElement struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

type feishuCardConfig struct {
	WideScreenMode bool `json:"wide_screen_mode"`
}

type feishuCardHeader struct {
	Template string         `json:"template"`
	Title    feishuCardText `json:"title"`
}

type feishuCard struct {
	Config   *feishuCardConfig   `json:"config,omitempty"`
	Header   *feishuCardHeader   `json:"header,omitempty"`
	Elements []feishuCardElement `json:"elements"`
}

type feishuWebhookRequest struct {
	Timestamp string      `json:"timestamp,omitempty"`
	Sign      string      `json:"sign,omitempty"`
	MsgType   string      `json:"msg_type"`
	Card      *feishuCard `json:"card,omitempty"`
}

// 兼容两种响应格式：{code,msg} 与 {StatusCode,StatusMessage}
type feishuWebhookResponse struct {
	Code          *int   `json:"code"`
	Msg           string `json:"msg"`
	StatusCode    *int   `json:"StatusCode"`
	StatusMessage string `json:"StatusMessage"`
}

// result 返回 (code, msg, ok)；无业务码字段时视为成功（由 HTTP 状态码兜底）。
func (r *feishuWebhookResponse) result() (int, string, bool) {
	code, msg := 0, r.Msg
	switch {
	case r.Code != nil:
		code = *r.Code
	case r.StatusCode != nil:
		code = *r.StatusCode
		if msg == "" {
			msg = r.StatusMessage
		}
	default:
		return 0, "", true
	}
	if code == 0 {
		return 0, "", true
	}
	if msg == "" && r.StatusMessage != "" {
		msg = r.StatusMessage
	}
	return code, msg, false
}

// PushReportCard 以 interactive 卡片推送报告：标题进 header，正文为 markdown 元素。
func (c *reportFeishuClient) PushReportCard(ctx context.Context, cfg *ReportLLMConfig, report *Report) error {
	if cfg == nil || !cfg.FeishuEnabled || strings.TrimSpace(cfg.FeishuWebhookURL) == "" {
		return domain.ErrReportFeishuNotConfigured
	}
	if report == nil {
		return fmt.Errorf("%w: nil report", domain.ErrReportFeishuPushFailed)
	}

	title, body := feishuCardContent(report)
	payload := &feishuWebhookRequest{
		MsgType: "interactive",
		Card: &feishuCard{
			Config: &feishuCardConfig{WideScreenMode: true},
			Header: &feishuCardHeader{
				Template: "blue",
				Title:    feishuCardText{Tag: "plain_text", Content: title},
			},
			Elements: []feishuCardElement{{Tag: "markdown", Content: body}},
		},
	}
	if secret := strings.TrimSpace(cfg.FeishuSecret); secret != "" {
		ts := time.Now().Unix()
		payload.Timestamp = strconv.FormatInt(ts, 10)
		payload.Sign = feishuSign(secret, ts)
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.FeishuWebhookURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", domain.ErrReportFeishuPushFailed, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: read response: %v", domain.ErrReportFeishuPushFailed, err)
	}
	parsed := &feishuWebhookResponse{}
	_ = json.Unmarshal(raw, parsed)
	if code, msg, ok := parsed.result(); !ok {
		return fmt.Errorf("%w: code %d: %s", domain.ErrReportFeishuPushFailed, code, msg)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: http %d", domain.ErrReportFeishuPushFailed, resp.StatusCode)
	}
	return nil
}

// feishuSign 飞书自定义机器人加签：string_to_sign = "timestamp\nsecret"，
// 以其为 HMAC-SHA256 的 key（消息体为空），输出 base64。
func feishuSign(secret string, timestamp int64) string {
	mac := hmac.New(sha256.New, []byte(fmt.Sprintf("%d\n%s", timestamp, secret)))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// feishuCardContent 把报告 markdown 拆成卡片标题与正文：首行 "# " 提取为标题，
// "## "/"### " 小节转粗体（飞书卡片 markdown 元素不支持标题语法），
// 超 feishuCardMaxBytes 按 rune 安全截断；无 AI 摘要时正文退化为统计行。
func feishuCardContent(report *Report) (string, string) {
	title := ""
	body := strings.TrimSpace(report.AISummary)
	if body != "" {
		title, body = splitCardTitle(body)
	}
	if title == "" {
		title = strings.TrimSpace(strings.TrimPrefix(reportTitle(report.Type, report.PeriodStart, report.Username), "# "))
		if title == "" {
			title = "工作报告"
		}
	}
	if strings.TrimSpace(body) == "" {
		body = reportStatsLines(report)
	}
	body = truncateBytesRunesafe(feishuHeadingToBold(body), feishuCardMaxBytes)
	return title, body
}

// splitCardTitle 从 markdown 首行提取 "# " 标题，返回 (标题, 剩余正文)。
func splitCardTitle(md string) (string, string) {
	lines := strings.SplitN(md, "\n", 2)
	first := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(first, "# ") {
		return "", md
	}
	rest := ""
	if len(lines) > 1 {
		rest = strings.TrimSpace(lines[1])
	}
	return strings.TrimSpace(strings.TrimPrefix(first, "# ")), rest
}

func feishuHeadingToBold(md string) string {
	lines := strings.Split(md, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "## "):
			lines[i] = "**" + strings.TrimSpace(strings.TrimPrefix(t, "## ")) + "**"
		case strings.HasPrefix(t, "### "):
			lines[i] = "**" + strings.TrimSpace(strings.TrimPrefix(t, "### ")) + "**"
		}
	}
	return strings.Join(lines, "\n")
}

func reportStatsLines(report *Report) string {
	return fmt.Sprintf("- 请求：%d\n- 输入/输出 tokens：%d / %d\n- 费用：%.4f\n- 模型分布：%s",
		report.Stats.Requests,
		report.Stats.InputTokens,
		report.Stats.OutputTokens,
		report.Stats.TotalCost,
		formatModelCounts(report.Stats.Models),
	)
}

// truncateBytesRunesafe 按字节截断且不切破 UTF-8 rune。
func truncateBytesRunesafe(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	cut := limit
	for i := limit; i > limit-4 && i > 0; i-- {
		if utf8.RuneStart(s[i]) {
			cut = i
			break
		}
	}
	return s[:cut] + "\n…（内容过长已截断）"
}
