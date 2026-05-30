package llm

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"qqbot-ai/internal/common"
	"strings"
)

func summarizeLLMRequest(req LLMChatRequest) map[string]any {
	messages := make([]map[string]any, 0, len(req.Messages))
	start := 0
	if len(req.Messages) > 12 {
		start = len(req.Messages) - 12
	}
	for i := start; i < len(req.Messages); i++ {
		message := req.Messages[i]
		item := map[string]any{
			"index": i,
			"role":  message.Role,
		}
		for key, value := range summarizeContentForLog(message.Content) {
			item[key] = value
		}
		if message.ToolCallID != "" {
			item["toolCallId"] = message.ToolCallID
		}
		if len(message.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				calls = append(calls, map[string]any{
					"id":               call.ID,
					"name":             call.Name,
					"argumentsPreview": previewJSONValue(call.Arguments, 1000),
				})
			}
			item["toolCalls"] = calls
		}
		messages = append(messages, item)
	}
	tools := make([]string, 0, len(req.Tools))
	for _, tool := range req.Tools {
		tools = append(tools, tool.Name)
	}
	return map[string]any{
		"provider":        req.Provider,
		"model":           req.Model,
		"systemHash":      shortHash(req.System),
		"systemBytes":     len([]byte(req.System)),
		"systemPreview":   trimLog(req.System, 400),
		"messageCount":    len(req.Messages),
		"messagesPreview": messages,
		"toolCount":       len(req.Tools),
		"toolNames":       tools,
		"toolChoice":      previewJSONValue(req.ToolChoice, 800),
	}
}

func summarizeContentForLog(content any) map[string]any {
	text := common.AsString(content)
	if strings.TrimSpace(text) == "" {
		return map[string]any{"contentPreview": previewJSONValue(content, 800)}
	}
	kind := classifyLLMContent(text)
	limit := 800
	switch kind {
	case "conversation_summary":
		limit = 0
	case "system_reminder":
		limit = 240
	case "tool_result":
		limit = 360
	case "qq_message":
		limit = 900
	}
	out := map[string]any{
		"contentKind":  kind,
		"contentHash":  shortHash(text),
		"contentBytes": len([]byte(text)),
	}
	if limit > 0 {
		out["contentPreview"] = trimLog(text, limit)
	}
	return out
}

func classifyLLMContent(text string) string {
	trimmed := strings.TrimSpace(text)
	switch {
	case strings.Contains(trimmed, "<conversation_summary>"):
		return "conversation_summary"
	case strings.Contains(trimmed, "<system_reminder"):
		return "system_reminder"
	case strings.Contains(trimmed, "<qq_message"):
		return "qq_message"
	case strings.Contains(trimmed, "工具 ") && strings.Contains(trimmed, "执行结果"):
		return "tool_result"
	default:
		return "text"
	}
}

func shortHash(text string) string {
	if text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:16]
}

func summarizeLLMResponse(response map[string]any) map[string]any {
	if response == nil {
		return nil
	}
	summary := map[string]any{
		"provider": response["provider"],
		"model":    response["model"],
		"usage":    response["usage"],
	}
	if message, _ := response["message"].(map[string]any); message != nil {
		summary["message"] = map[string]any{
			"contentPreview": trimLog(common.AsString(message["content"]), 2000),
			"toolCalls":      toolCallNames(message["toolCalls"]),
			"toolCallCount":  countSlice(message["toolCalls"]),
		}
	}
	if os := extractResponseOS(response); os != "" {
		summary["osPreview"] = trimLog(os, 1000)
	}
	if reasoning := strings.TrimSpace(common.AsString(response["reasoning"])); reasoning != "" {
		summary["reasoningPreview"] = trimLog(reasoning, 2000)
	}
	return summary
}

func extractResponseOS(response map[string]any) string {
	if response == nil {
		return ""
	}
	for _, value := range []string{common.AsString(response["os"]), common.AsString(response["OS"])} {
		if os := strings.TrimSpace(value); os != "" {
			return os
		}
	}
	message, _ := response["message"].(map[string]any)
	if message == nil {
		return ""
	}
	for _, value := range []string{common.AsString(message["os"]), common.AsString(message["OS"])} {
		if os := strings.TrimSpace(value); os != "" {
			return os
		}
	}
	calls, _ := message["toolCalls"].([]any)
	for _, item := range calls {
		call, _ := item.(map[string]any)
		if call == nil {
			continue
		}
		args, _ := call["arguments"].(map[string]any)
		if os := extractArgsOS(args); os != "" {
			return os
		}
		if common.AsString(call["name"]) == "invoke" {
			nestedArgs, _ := args["arguments"].(map[string]any)
			if os := extractArgsOS(nestedArgs); os != "" {
				return os
			}
		}
	}
	return ""
}

func extractArgsOS(args map[string]any) string {
	if args == nil {
		return ""
	}
	for _, key := range []string{"os", "OS"} {
		if os := strings.TrimSpace(common.AsString(args[key])); os != "" {
			return os
		}
	}
	return ""
}

func summarizeNativePayload(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	summary := map[string]any{}
	for _, key := range []string{"status", "id", "object", "model", "type"} {
		if value, ok := payload[key]; ok {
			summary[key] = value
		}
	}
	if value, ok := payload["error"]; ok {
		summary["errorPreview"] = previewJSONValue(value, 2400)
	}
	if sse := common.AsString(payload["sse"]); sse != "" {
		summary["sseBytes"] = len([]byte(sse))
		summary["ssePreview"] = trimLog(sse, 4000)
		return summary
	}
	if body := common.AsString(payload["body"]); body != "" {
		summary["bodyBytes"] = len([]byte(body))
		summary["bodyPreview"] = trimLog(body, 4000)
		return summary
	}
	data, _ := json.Marshal(payload)
	summary["jsonBytes"] = len(data)
	summary["jsonPreview"] = trimLog(string(data), 4000)
	return summary
}

func previewJSONValue(value any, max int) string {
	if value == nil {
		return ""
	}
	if text := common.AsString(value); strings.TrimSpace(text) != "" {
		return trimLog(text, max)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return trimLog(fmt.Sprint(value), max)
	}
	return trimLog(string(data), max)
}

func countSlice(value any) int {
	switch items := value.(type) {
	case []any:
		return len(items)
	case []map[string]any:
		return len(items)
	default:
		return 0
	}
}
