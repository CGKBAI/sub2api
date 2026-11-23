package securityaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	ErrNoPromptText = errors.New("prompt audit request contains no user text")

	bearerPattern = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+\-/]+=*`)
	apiKeyPattern = regexp.MustCompile(`(?i)\b(sk|rk|pk|api[_-]?key|token|secret|password)[-_:=\s]+[A-Za-z0-9._~+\-/]{8,}`)
	canaryPattern = regexp.MustCompile(`(?i)([A-Z]+_CANARY_)[A-Za-z0-9_-]+`)
	emailPattern  = regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`)
	phonePattern  = regexp.MustCompile(`(?:\+?\d[\d\s().-]{8,}\d)`)
)

const promptAuditPrioritySeparator = "\x00SUB2API_PROMPT_AUDIT_PRIORITY_END\x00"

type promptSegment struct {
	text string
	user bool
	role string
}

func ExtractPromptSnapshot(req Request) (PromptSnapshot, error) {
	return extractPromptSnapshot(req, false)
}

// ExtractBlockingPromptSnapshot builds the narrow, low-latency blocking input
// when configured. Asynchronous auditing always uses ExtractPromptSnapshot so
// the complete client-controlled transcript is retained for review.
func ExtractBlockingPromptSnapshot(req Request, latestTurnOnly bool) (PromptSnapshot, error) {
	return extractPromptSnapshot(req, latestTurnOnly)
}

func extractPromptSnapshot(req Request, latestTurnOnly bool) (PromptSnapshot, error) {
	var document any
	if err := json.Unmarshal(req.Body, &document); err != nil {
		return PromptSnapshot{}, errors.New("prompt audit request JSON is invalid")
	}
	extracted := extractProtocolSegments(req.Protocol, document)
	segments := normalizeSegmentsLatestUserFirst(extracted)
	if latestTurnOnly {
		segments = blockingSegmentsLatestUserAndPreviousOutput(extracted)
	}
	if len(segments) == 0 {
		return PromptSnapshot{}, ErrNoPromptText
	}
	scanText, metadataText := buildPrioritizedScanText(segments)
	digest := sha256.Sum256([]byte(metadataText))
	stage := strings.TrimSpace(req.Stage)
	if stage == "" {
		stage = "http"
	}
	return PromptSnapshot{
		RequestID: req.RequestID, SessionID: req.SessionID, UserID: req.UserID, UsernameSnapshot: req.Username,
		UserEmailSnapshot: req.UserEmail, APIKeyID: req.APIKeyID, APIKeyNameSnapshot: req.APIKeyName,
		GroupID: cloneInt64Ptr(req.GroupID), GroupName: req.GroupName, Provider: req.Provider,
		Endpoint: req.Endpoint, Protocol: req.Protocol, Model: req.Model,
		PromptHash: hex.EncodeToString(digest[:]), RedactedPreview: BuildPromptPreview(metadataText, DefaultPromptPreviewMaxRunes),
		FullPrompt:   buildFullPromptFromScanText(scanText, DefaultFullPromptMaxRunes),
		UserText:     LatestUserTextFromScanText(scanText),
		PromptLength: utf8.RuneCountInString(metadataText), MessageCount: len(segments), Stage: stage,
		ScanText: scanText,
	}, nil
}

// DefaultPromptPreviewMaxRunes caps how much sanitized prompt text may be
// considered before BuildPromptPreview withholds the majority for storage/UI.
const DefaultPromptPreviewMaxRunes = 96

// DefaultFullPromptMaxRunes caps how much unredacted prompt text is persisted
// on an audit event for admin review. It is deliberately generous so realistic
// prompts are kept intact while bounding per-row storage.
const DefaultFullPromptMaxRunes = 65536

// fullPromptHeadRunes 是超长 prompt 退化截断（只保头+尾）时优先保留的头部长度。
//
// 拍平后的 full_prompt 结构为「该次请求最新一条用户消息 + 其余上下文（system /
// 历史，保持原顺序）」且段间无角色标记。主路径已改为按段分配预算
// （buildFullPromptFromScanText），这里只作为兜底：段信息不可用或预算过小时使用。
const fullPromptHeadRunes = 8192

// 按段分配预算（主路径）的配额。设计目标：同样的 65536 字符，把预算花在
// "这一轮要干什么"和"最近做完了什么"上，而不是被静态注入块和工具输出吃光。
const (
	// fullPromptLatestUserRunes 最新用户消息（段 1）的保留上限。用户粘贴的超长
	// 日志/文件只保留前 16k——报告要的是"在做什么"，不是"贴了什么"。
	fullPromptLatestUserRunes = 16384
	// fullPromptSystemRunes 系统提示词开头（识别客户端/场景）的保留上限。
	fullPromptSystemRunes = 1536
	// fullPromptBlockRunes 尾部每个内容块（按空行切分）的保留上限。
	fullPromptBlockRunes = 2048
	// fullPromptMinTailBlockRunes 尾部块的最小保留长度，低于此值就不再塞块了。
	fullPromptMinTailBlockRunes = 256
)

// fullPromptSegmentSeparator 在段间插入的省略标记（仅用于可读性，不影响语义）。
const fullPromptSegmentSeparator = "\n\n……（此处省略 %d 段）……\n\n"

// fullPromptSkillManifestMarker 静态技能清单的识别特征。opencode 等客户端把整份
// 技能清单塞进系统提示词，实测 44% 的超长请求里它都在前 8k 内，对报告零信息量。
const fullPromptSkillManifestMarker = "</available_skills>"

func extractProtocolSegments(protocol string, document any) []promptSegment {
	root, _ := document.(map[string]any)
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	switch protocol {
	case "openai_chat_completions", "openai_chat", "chat_completions":
		return extractChatLikeSegments(root)
	case "anthropic_messages", "claude_messages", "messages":
		return append(extractAnthropicSystem(root["system"]), extractMessages(root["messages"], clientInstructionRoles...)...)
	case "gemini", "gemini_generate_content":
		return extractGeminiRoot(root)
	case "openai_responses", "responses", "responses_websocket":
		if frameType := stringValue(root["type"]); frameType != "" || protocol == "responses_websocket" {
			if frameType != "response.create" {
				return nil
			}
			if input, exists := root["input"]; exists && input != nil {
				return append(extractInstructions(root["instructions"]), extractResponses(input)...)
			}
			if response, ok := root["response"].(map[string]any); ok {
				return append(extractInstructions(response["instructions"]), extractResponses(response["input"])...)
			}
			return extractInstructions(root["instructions"])
		}
		return append(extractInstructions(root["instructions"]), extractResponses(root["input"])...)
	case "openai_images", "grok_media", "media", "images":
		return userPromptSegments(extractMediaPrompts(root))
	case "typesafe_systemone":
		return extractSystemOneSegments(root)
	default:
		if segments := extractChatLikeSegments(root); len(segments) > 0 {
			return segments
		}
		if responses := append(extractInstructions(root["instructions"]), extractResponses(root["input"])...); len(responses) > 0 {
			return responses
		}
		if gemini := extractGeminiRoot(root); len(gemini) > 0 {
			return gemini
		}
		return userPromptSegments(extractMediaPrompts(root))
	}
}

// extractSystemOneSegments collects every client-controlled text of a TypeSafe
// System One request: question IDs, every question field except the validated
// type, unknown top-level extension fields, and the evaluated state. Object keys
// are text too (Jev reads the whole JSON), so they are collected with values.
// The state comes last so it is the prioritized segment, and keys are visited
// in sorted order to keep the prompt hash stable across requests.
func extractSystemOneSegments(root map[string]any) []promptSegment {
	if root == nil {
		return nil
	}
	texts := make([]string, 0, 4)
	questions, isObject := root["questions"].(map[string]any)
	if !isObject {
		texts = appendJSONStringLeaves(texts, root["questions"])
	}
	for _, id := range sortedJSONKeys(questions) {
		texts = appendJSONStringLeaves(texts, id)
		question, ok := questions[id].(map[string]any)
		if !ok {
			texts = appendJSONStringLeaves(texts, questions[id])
			continue
		}
		for _, field := range sortedJSONKeys(question) {
			switch field {
			case "type":
				continue
			case "instructions", "criteria":
			default:
				texts = appendJSONStringLeaves(texts, field)
			}
			texts = appendJSONStringLeaves(texts, question[field])
		}
	}
	for _, field := range sortedJSONKeys(root) {
		switch field {
		case "model", "stream", "state", "questions":
			continue
		}
		texts = appendJSONStringLeaves(texts, field)
		texts = appendJSONStringLeaves(texts, root[field])
	}
	texts = appendJSONStringLeaves(texts, root["state"])
	return userPromptSegments(texts)
}

func appendJSONStringLeaves(texts []string, value any) []string {
	switch typed := value.(type) {
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			texts = append(texts, text)
		}
	case []any:
		for _, item := range typed {
			texts = appendJSONStringLeaves(texts, item)
		}
	case map[string]any:
		for _, key := range sortedJSONKeys(typed) {
			texts = appendJSONStringLeaves(texts, key)
			texts = appendJSONStringLeaves(texts, typed[key])
		}
	}
	return texts
}

func sortedJSONKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// clientInstructionRoles are roles a client may freely populate. Attackers can
// place jailbreak/PII text in assistant/tool turns, so blocking audit must scan
// them too—not only user/system/developer instructions.
var clientInstructionRoles = []string{"user", "system", "developer", "assistant", "tool"}

func extractChatLikeSegments(root map[string]any) []promptSegment {
	if root == nil {
		return nil
	}
	return extractMessages(root["messages"], clientInstructionRoles...)
}

func extractMessages(value any, wantedRoles ...string) []promptSegment {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	wanted := make(map[string]struct{}, len(wantedRoles))
	for _, role := range wantedRoles {
		wanted[strings.ToLower(strings.TrimSpace(role))] = struct{}{}
	}
	result := make([]promptSegment, 0, len(items))
	for _, item := range items {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(stringValue(message["role"]))
		if _, match := wanted[role]; !match {
			continue
		}
		texts := contentTexts(message["content"])
		for _, text := range texts {
			result = append(result, promptSegment{text: text, user: role == "user", role: role})
		}
	}
	return result
}

func extractInstructions(value any) []promptSegment {
	switch typed := value.(type) {
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			return []promptSegment{{text: text, role: "system"}}
		}
	case []any:
		return systemPromptSegments(contentTexts(typed))
	case map[string]any:
		return systemPromptSegments(contentTexts(typed))
	}
	return nil
}

func extractAnthropicSystem(value any) []promptSegment {
	switch typed := value.(type) {
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			return []promptSegment{{text: text, role: "system"}}
		}
	case []any:
		return systemPromptSegments(contentTexts(typed))
	case map[string]any:
		return systemPromptSegments(contentTexts(typed))
	}
	return nil
}

func extractResponses(value any) []promptSegment {
	switch typed := value.(type) {
	case string:
		return []promptSegment{{text: typed, user: true, role: "user"}}
	case []any:
		result := make([]promptSegment, 0, len(typed))
		for _, item := range typed {
			switch entry := item.(type) {
			case string:
				result = append(result, promptSegment{text: entry, user: true, role: "user"})
			case map[string]any:
				role := strings.ToLower(stringValue(entry["role"]))
				if role != "" && !isClientInstructionRole(role) {
					continue
				}
				if content, exists := entry["content"]; exists {
					for _, text := range contentTexts(content) {
						result = append(result, promptSegment{text: text, user: role == "" || role == "user", role: role})
					}
				} else if text := stringValue(entry["text"]); text != "" {
					result = append(result, promptSegment{text: text, user: role == "" || role == "user", role: role})
				}
			}
		}
		return result
	case map[string]any:
		role := strings.ToLower(stringValue(typed["role"]))
		if role != "" && !isClientInstructionRole(role) {
			return nil
		}
		return promptSegmentsForRole(contentTexts(typed["content"]), role)
	default:
		return nil
	}
}

func isClientInstructionRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user", "system", "developer", "assistant", "tool", "model":
		return true
	default:
		return false
	}
}

func extractGemini(value any) []promptSegment {
	var contents []any
	switch typed := value.(type) {
	case []any:
		contents = typed
	case map[string]any:
		contents = []any{typed}
	default:
		return nil
	}
	result := make([]promptSegment, 0, len(contents))
	for _, item := range contents {
		content, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(stringValue(content["role"]))
		if role != "" && !isClientInstructionRole(role) {
			continue
		}
		parts, _ := content["parts"].([]any)
		for _, part := range parts {
			if object, ok := part.(map[string]any); ok {
				if text := stringValue(object["text"]); text != "" {
					result = append(result, promptSegment{text: text, user: role == "" || role == "user", role: role})
				}
			}
		}
	}
	return result
}

func extractGeminiRoot(root map[string]any) []promptSegment {
	if root == nil {
		return nil
	}
	result := extractGeminiSystemInstruction(root["systemInstruction"])
	result = append(result, extractGeminiSystemInstruction(root["system_instruction"])...)
	result = append(result, extractGemini(root["contents"])...)
	result = append(result, extractGemini(root["content"])...)
	result = append(result, extractGeminiInstances(root["instances"])...)
	if requests, ok := root["requests"].([]any); ok {
		for _, item := range requests {
			request, ok := item.(map[string]any)
			if !ok {
				continue
			}
			result = append(result, extractGeminiSystemInstruction(request["systemInstruction"])...)
			result = append(result, extractGeminiSystemInstruction(request["system_instruction"])...)
			result = append(result, extractGemini(request["contents"])...)
			result = append(result, extractGemini(request["content"])...)
			result = append(result, extractGeminiInstances(request["instances"])...)
		}
	}
	return result
}

func extractGeminiSystemInstruction(value any) []promptSegment {
	switch typed := value.(type) {
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			return []promptSegment{{text: text, role: "system"}}
		}
	case map[string]any:
		if parts, ok := typed["parts"].([]any); ok {
			result := make([]promptSegment, 0, len(parts))
			for _, part := range parts {
				if object, ok := part.(map[string]any); ok {
					if text := stringValue(object["text"]); text != "" {
						result = append(result, promptSegment{text: text, role: "system"})
					}
				}
			}
			return result
		}
		return systemPromptSegments(contentTexts(typed))
	case []any:
		segments := extractGemini(typed)
		for index := range segments {
			segments[index].user = false
			segments[index].role = "system"
		}
		return segments
	}
	return nil
}

func extractGeminiInstances(value any) []promptSegment {
	instances, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]promptSegment, 0, len(instances))
	for _, item := range instances {
		if instance, ok := item.(map[string]any); ok {
			if prompt := stringValue(instance["prompt"]); prompt != "" {
				result = append(result, promptSegment{text: prompt, user: true, role: "user"})
			}
		}
	}
	return result
}

func extractMediaPrompts(root map[string]any) []string {
	if root == nil {
		return nil
	}
	result := make([]string, 0, 4)
	seen := map[string]struct{}{}
	var walk func(any, string)
	walk = func(value any, key string) {
		switch typed := value.(type) {
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for childKey := range typed {
				keys = append(keys, childKey)
			}
			sort.Strings(keys)
			for _, childKey := range keys {
				walk(typed[childKey], childKey)
			}
		case []any:
			for _, item := range typed {
				walk(item, key)
			}
		case string:
			if !isMediaPromptKey(key) || looksLikeMediaPayload(typed) {
				return
			}
			text := strings.TrimSpace(typed)
			if text == "" {
				return
			}
			if _, duplicate := seen[text]; duplicate {
				return
			}
			seen[text] = struct{}{}
			result = append(result, text)
		}
	}
	walk(root, "")
	return result
}

func isMediaPromptKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(strings.TrimSpace(key)))
	switch normalized {
	case "prompt", "inputprompt", "textprompt", "description", "query", "lyrics", "negativeprompt",
		"positiveprompt", "gptdescriptionprompt", "prompten", "finalprompt", "finalzhprompt",
		"origprompt", "actualprompt", "imageprompt", "input":
		return true
	default:
		return false
	}
}

func looksLikeMediaPayload(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "data:image/") || strings.HasPrefix(lower, "data:video/") ||
		strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return true
	}
	if len(trimmed) >= 256 {
		for _, r := range trimmed {
			alphaNumeric := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
			if !alphaNumeric && r != '+' && r != '/' && r != '=' {
				return false
			}
		}
		return true
	}
	return false
}

func contentTexts(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		result := make([]string, 0, len(typed))
		for _, part := range typed {
			object, ok := part.(map[string]any)
			if !ok {
				continue
			}
			typeName := strings.ToLower(stringValue(object["type"]))
			if typeName != "" && typeName != "text" && typeName != "input_text" && typeName != "output_text" {
				continue
			}
			if text := stringValue(object["text"]); text != "" {
				result = append(result, text)
			}
		}
		return result
	case map[string]any:
		if text := stringValue(typed["text"]); text != "" {
			return []string{text}
		}
	}
	return nil
}

func normalizeSegmentsLatestUserFirst(values []promptSegment) []string {
	normalized := normalizedPromptSegments(values)
	if len(normalized) == 0 {
		return nil
	}
	priorityIndex := len(normalized) - 1
	for index := len(normalized) - 1; index >= 0; index-- {
		if isUserSegment(normalized[index]) {
			priorityIndex = index
			break
		}
	}
	result := make([]string, 0, len(normalized))
	result = append(result, normalized[priorityIndex].text)
	for index, segment := range normalized {
		if index != priorityIndex {
			result = append(result, segment.text)
		}
	}
	return result
}

// blockingSegmentsLatestUserAndPreviousOutput limits synchronous guard input to
// the current user turn and the nearest preceding assistant/model turn. It is
// deliberately opt-in because full transcript scanning remains stronger at
// finding client-controlled content placed in older or non-user messages.
func blockingSegmentsLatestUserAndPreviousOutput(values []promptSegment) []string {
	normalized := normalizedPromptSegments(values)
	latestUserStart := latestUserSegmentStart(normalized)
	if latestUserStart < 0 {
		// A request without user content cannot be narrowed safely. Preserve the
		// established full-snapshot behavior for unusual protocol payloads.
		return normalizeSegmentsLatestUserFirst(values)
	}
	latestUserEnd := latestUserStart
	for latestUserEnd < len(normalized) && isUserSegment(normalized[latestUserEnd]) {
		latestUserEnd++
	}
	currentUserText := make([]string, 0, latestUserEnd-latestUserStart)
	for _, segment := range normalized[latestUserStart:latestUserEnd] {
		currentUserText = append(currentUserText, segment.text)
	}
	// A single client turn may have several text content parts. Keep it in one
	// priority segment so every part of the latest input is scanned before the
	// prior output begins.
	selected := []promptSegment{{text: strings.Join(currentUserText, "\n\n"), user: true, role: "user"}}
	for index := latestUserStart - 1; index >= 0; index-- {
		if !isAssistantOutputSegment(normalized[index]) {
			continue
		}
		start := index
		for start > 0 && isAssistantOutputSegment(normalized[start-1]) {
			start--
		}
		selected = append(selected, normalized[start:index+1]...)
		break
	}
	return promptSegmentTexts(selected)
}

func normalizedPromptSegments(values []promptSegment) []promptSegment {
	normalized := make([]promptSegment, 0, len(values))
	for _, value := range values {
		value.text = strings.TrimSpace(value.text)
		if value.text != "" {
			normalized = append(normalized, value)
		}
	}
	return normalized
}

func latestUserSegmentStart(values []promptSegment) int {
	latest := -1
	for index := len(values) - 1; index >= 0; index-- {
		if isUserSegment(values[index]) {
			latest = index
			break
		}
	}
	for latest > 0 && isUserSegment(values[latest-1]) {
		latest--
	}
	return latest
}

func isUserSegment(segment promptSegment) bool {
	return segment.user || segment.role == "user"
}

func isAssistantOutputSegment(segment promptSegment) bool {
	return segment.role == "assistant" || segment.role == "model"
}

func promptSegmentTexts(values []promptSegment) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.text)
	}
	return result
}

func buildPrioritizedScanText(segments []string) (scanText string, metadataText string) {
	metadataText = strings.Join(segments, "\n\n")
	if len(segments) <= 1 {
		return metadataText, metadataText
	}
	return segments[0] + promptAuditPrioritySeparator + strings.Join(segments[1:], "\n\n"), metadataText
}

func promptSegmentsForRole(texts []string, role string) []promptSegment {
	result := make([]promptSegment, 0, len(texts))
	for _, text := range texts {
		result = append(result, promptSegment{text: text, user: role == "" || role == "user", role: role})
	}
	return result
}

func userPromptSegments(texts []string) []promptSegment {
	return promptSegmentsForRole(texts, "user")
}

func systemPromptSegments(texts []string) []promptSegment {
	return promptSegmentsForRole(texts, "system")
}

func RedactPreview(value string, maxRunes int) string {
	value = bearerPattern.ReplaceAllString(value, "Bearer ***")
	value = apiKeyPattern.ReplaceAllStringFunc(value, func(match string) string {
		if index := strings.IndexAny(match, ":= \t"); index >= 0 {
			return match[:index+1] + "***"
		}
		return "***"
	})
	value = canaryPattern.ReplaceAllString(value, "${1}***")
	value = emailPattern.ReplaceAllString(value, "***@***")
	value = phonePattern.ReplaceAllString(value, "***PHONE***")
	return TrimRunes(value, maxRunes)
}

// BuildPromptPreview stores only a short, non-recoverable head of sanitized
// input. Ordinary confidential prompts must not land nearly intact in PostgreSQL
// or the admin UI merely because no secret regex matched.
func BuildPromptPreview(value string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = DefaultPromptPreviewMaxRunes
	}
	redacted := strings.TrimSpace(RedactPreview(value, maxRunes))
	if redacted == "" {
		return ""
	}
	runes := []rune(redacted)
	hadTruncation := strings.HasSuffix(redacted, "…")
	if hadTruncation && len(runes) > 0 {
		runes = runes[:len(runes)-1]
	}
	if len(runes) == 0 {
		return "***…"
	}
	// Short unlabelled secrets would otherwise leak a recoverable prefix (e.g.
	// 20 runes → 5 visible). Fully withhold anything below the keep threshold.
	const minLengthForPartialPreview = 32
	if len(runes) < minLengthForPartialPreview {
		if hadTruncation {
			return "***…"
		}
		return "***"
	}
	// Keep at most a quarter of the already-truncated text, and never more than
	// 24 runes, so the majority of prompt content is withheld by default.
	keep := len(runes) / 4
	if keep > 24 {
		keep = 24
	}
	preview := string(runes[:keep]) + "***"
	if hadTruncation || keep < len(runes) {
		preview += "…"
	}
	return preview
}

// BuildFullPrompt returns the complete prompt text for audit-event storage and
// admin review, without redaction. NUL bytes are stripped because PostgreSQL
// TEXT rejects them, and the result is capped at maxRunes.
//
// 超过 maxRunes 时改为「头 + 尾」保留（见 fullPromptHeadRunes）：头部是该次请求
// 的最新用户消息，尾部是会话最近的内容，中间历史以省略标记衔接。输出长度仍
// 不超过 maxRunes，保证单行存储有界。
func BuildFullPrompt(value string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = DefaultFullPromptMaxRunes
	}
	value = strings.ReplaceAll(value, "\x00", "")
	return TrimRunesHeadTail(strings.TrimSpace(value), maxRunes, fullPromptHeadRunes)
}

// TrimRunesHeadTail 截断到 limit rune：保留头部 head 与尾部 (limit-head-标记长度)，
// 中间替换为省略标记。总长度不超过 limit（含标记），便于保持存储上界不变。
// limit 过小或头部占比过大时退化为只保头部。
func TrimRunesHeadTail(value string, limit, head int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	omitted := len(runes) - limit
	marker := []rune(fmt.Sprintf("\n\n……（此处省略 %d 字）……\n\n", omitted))
	if head <= 0 || head >= limit || len(marker) >= limit-head {
		return string(runes[:limit]) + "…"
	}
	tail := limit - head - len(marker)
	return string(runes[:head]) + string(marker) + string(runes[len(runes)-tail:])
}

// FullPromptFromScanText reconstructs the display prompt from the worker scan
// payload. buildPrioritizedScanText inserts exactly one priority separator
// between the prioritized segment (latest user message) and the remainder, so
// splitting on it recovers the exact boundary of the latest user turn.
//
// 与旧的「只保头」实现相比，这里按段分配预算（见 fullPromptLatestUserRunes 等）：
//  1. 最新用户消息（段 1）单独享受 16k 预算 —— 这一轮要干什么最重要；
//  2. 剩余内容按空行切块，从末尾往前取，每块最多 2k —— 最近的工作上下文；
//  3. 开头保留一小段系统提示词（1.5k）用于识别客户端/场景；
//  4. 命中 available_skills 静态技能清单的块直接丢弃；
//  5. 被跳过的位置插省略标记，输出总长仍 ≤ DefaultFullPromptMaxRunes。
//
// 段 1 的边界来自优先级分隔符（精确）；其余部分按空行切块，块边界是段边界的
// 细分（段本身是用 "\n\n" 连接的），所以尾部取块只会落在真实内容内部，不会
// 串到更早的内容上。
func FullPromptFromScanText(scanText string) string {
	return buildFullPromptFromScanText(scanText, DefaultFullPromptMaxRunes)
}

func buildFullPromptFromScanText(scanText string, limit int) string {
	if limit <= 0 {
		limit = DefaultFullPromptMaxRunes
	}
	latest := scanText
	rest := ""
	if index := strings.Index(scanText, promptAuditPrioritySeparator); index >= 0 {
		latest = scanText[:index]
		rest = scanText[index+len(promptAuditPrioritySeparator):]
	}
	// NUL 必须在切分之后剥离：优先级分隔符本身含 \x00，先剥会让切分失效。
	latest = strings.TrimSpace(strings.ReplaceAll(latest, "\x00", ""))
	rest = strings.TrimSpace(strings.ReplaceAll(rest, "\x00", ""))

	if runeLen(latest)+runeLen(rest)+2 <= limit {
		if rest == "" {
			return latest
		}
		return latest + "\n\n" + rest
	}

	// ① 最新用户消息：保留前 16k（截断处加标记）。
	var out strings.Builder
	budget := limit
	latestText := latest
	if n := runeLen(latestText); n > fullPromptLatestUserRunes {
		latestText = string([]rune(latestText)[:fullPromptLatestUserRunes]) +
			fmt.Sprintf("\n……（本轮用户消息过长，仅保留前 %d 字）……", fullPromptLatestUserRunes)
	}
	out.WriteString(latestText)
	budget -= runeLen(latestText)

	// ② 其余内容切成块，从末尾往前挑，直到预算用尽。
	// 注意：这里只按**预算**停，不按块数停——块普遍偏小（几百字），限块数会让
	// 几万字符预算闲置（实测平均只用到 16k/65k）。
	blocks := splitPromptBlocks(rest)
	keep := make([]bool, len(blocks))
	keepCount := 0
	for index := len(blocks) - 1; index >= 0; index-- {
		block := blocks[index]
		if isStaticPromptBlock(block) {
			continue
		}
		remaining := budget - 2 // 预留块间分隔符
		if remaining < fullPromptMinTailBlockRunes {
			break
		}
		size := runeLen(block)
		if size > fullPromptBlockRunes {
			size = fullPromptBlockRunes
		}
		if size < fullPromptMinTailBlockRunes {
			// 太短的块（单行备注、"好的"之类）跳过，但继续往前找：不能因为
			// 最后一块很短就放弃整段尾部（曾导致素材只剩 1.9k 字符）。
			continue
		}
		if size > remaining {
			if remaining < fullPromptMinTailBlockRunes {
				break
			}
			size = remaining
		}
		keep[index] = true
		keepCount++
		budget -= size + 2
	}

	// ③ 开头补一小段系统提示词（跳过静态技能清单），便于识别客户端/场景。
	// 系统提示词有意义的是开头（客户端/角色设定），所以这里取块的**前半段**。
	headBlock := -1
	for index := 0; index < len(blocks); index++ {
		if isStaticPromptBlock(blocks[index]) {
			continue
		}
		headBlock = index
		break
	}
	if headBlock >= 0 && !keep[headBlock] {
		if budget > fullPromptSystemRunes {
			keep[headBlock] = true
			budget -= fullPromptSystemRunes + 2
		} else {
			headBlock = -1
		}
	}

	if keepCount == 0 && headBlock < 0 {
		// 完全没有可保留的块：退化为头+尾兜底。
		flat := strings.ReplaceAll(scanText, promptAuditPrioritySeparator, "\n\n")
		return TrimRunesHeadTail(strings.ReplaceAll(flat, "\x00", ""), limit, fullPromptHeadRunes)
	}

	// ④ 按原顺序拼接，跳过的地方插省略标记。
	omitted := 0
	flushOmitted := func() {
		if omitted > 0 {
			out.WriteString(fmt.Sprintf(fullPromptSegmentSeparator, omitted))
			omitted = 0
		}
	}
	for index, block := range blocks {
		if !keep[index] {
			omitted++
			continue
		}
		flushOmitted()
		out.WriteString("\n\n")
		out.WriteString(trimPromptBlock(block, index, headBlock))
	}
	if omitted > 0 {
		out.WriteString(fmt.Sprintf(fullPromptSegmentSeparator, omitted))
	}

	result := strings.TrimSpace(out.String())
	if n := runeLen(result); n > limit {
		result = TrimRunesHeadTail(result, limit, fullPromptHeadRunes)
	}
	return result
}

// splitPromptBlocks 按空行切块（段本身也是用 "\n\n" 连接的，块是段的细分）。
func splitPromptBlocks(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	raw := strings.Split(text, "\n\n")
	blocks := make([]string, 0, len(raw))
	for _, block := range raw {
		if block = strings.TrimSpace(block); block != "" {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

// isStaticPromptBlock 判断是否为静态噪音块（技能清单等），这类块不进报告素材。
func isStaticPromptBlock(block string) bool {
	return strings.Contains(block, fullPromptSkillManifestMarker)
}

// trimPromptBlock 截断单个块：系统提示词取开头，尾部块取末尾（越靠后越接近"现在"）。
func trimPromptBlock(block string, index, headBlock int) string {
	runes := []rune(block)
	limit := fullPromptBlockRunes
	if index == headBlock {
		limit = fullPromptSystemRunes
	}
	if len(runes) <= limit {
		return block
	}
	if index == headBlock {
		return string(runes[:limit]) + "…"
	}
	return "…" + string(runes[len(runes)-limit:])
}

func runeLen(value string) int { return len([]rune(value)) }

// latestUserTextRunes 是 prompt_audit_events.user_text 的存储上限。
const latestUserTextRunes = 4000

// systemReminderBlockRe 匹配客户端注入的完整提醒块（可跨行）。
var systemReminderBlockRe = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// LatestUserTextFromScanText 从扫描文本里取出「最新一条用户消息」并剥掉客户端
// 注入的 <system-reminder> 块，供报告侧直接当作用户提问使用。
//
// 为什么要单独存一列：报告侧此前只能读 full_prompt 的头部 2000 字再用正则猜，
// 注入块未闭合时直接判为空——实测 8.1% 的请求因此丢失提问。这里在写入时用
// 完整文本（未截断）抽取，命中率更高。
//
// 未闭合的 <system-reminder> 只去掉标签本身、保留其后内容，尽量把用户输入救回来。
func LatestUserTextFromScanText(scanText string) string {
	latest := scanText
	if index := strings.Index(scanText, promptAuditPrioritySeparator); index >= 0 {
		latest = scanText[:index]
	}
	cleaned := systemReminderBlockRe.ReplaceAllString(latest, "")
	cleaned = strings.ReplaceAll(cleaned, "<system-reminder>", "")
	cleaned = strings.ReplaceAll(cleaned, "</system-reminder>", "")
	cleaned = strings.ReplaceAll(cleaned, "\x00", "")
	return TrimRunes(strings.TrimSpace(cleaned), latestUserTextRunes)
}

func TrimRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func cloneInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
