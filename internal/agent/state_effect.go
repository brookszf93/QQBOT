package agent

import (
	"encoding/json"
	"fmt"
	"qqbot-ai/internal/agentruntime"
	"qqbot-ai/internal/common"
	"qqbot-ai/internal/prompts"
	"strings"
)

// StateEffect 是工具执行后对根状态机和上下文产生的结构化影响。
//
// 工具可以继续返回 JSON 给协议层；运行时只从这里抽取真正需要给模型看的
// 可见上下文，避免把 availableChildren、requeuedEventCount 等内部字段混进长期上下文。
type StateEffect struct {
	ToolName       string
	FocusedStateID string
	VisibleContext string
	AllowedTools   []string
	Error          string
	HasEvent       bool
	Ephemeral      bool
}

func stateEffectFromToolResult(toolName, content string) StateEffect {
	effect := StateEffect{ToolName: toolName}
	var payload map[string]any
	_ = json.Unmarshal([]byte(content), &payload)

	effect.FocusedStateID = common.AsString(payload["focusedStateId"])
	if tools, ok := payload["allowedTools"].([]string); ok {
		effect.AllowedTools = append(effect.AllowedTools, tools...)
	} else if tools, ok := payload["allowedTools"].([]any); ok {
		for _, tool := range tools {
			if name := common.AsString(tool); strings.TrimSpace(name) != "" {
				effect.AllowedTools = append(effect.AllowedTools, name)
			}
		}
	} else if tools, ok := payload["availableTools"].([]any); ok {
		for _, tool := range tools {
			if name := common.AsString(tool); strings.TrimSpace(name) != "" {
				effect.AllowedTools = append(effect.AllowedTools, name)
			}
		}
	}
	if errText := common.AsString(payload["error"]); strings.TrimSpace(errText) != "" {
		effect.Error = strings.TrimSpace(errText)
	}
	if ephemeral, _ := payload["ephemeral"].(bool); ephemeral {
		effect.Ephemeral = true
		return effect
	}

	switch toolName {
	case "wait":
		effect.Ephemeral = true
		effect.HasEvent, _ = payload["hasEvent"].(bool)
		return effect
	case "back", "zone_out":
		effect.Ephemeral = true
	case "enter":
		if effect.Error != "" {
			effect.VisibleContext = enterErrorReminder(effect.Error, effect.FocusedStateID)
			return effect
		}
	}

	if contextText := toolContextMessage(content); contextText != "" {
		effect.VisibleContext = contextText
		return effect
	}
	if effect.Error != "" {
		effect.VisibleContext = toolErrorReminder(toolName, effect.Error, content)
		return effect
	}
	if fallback := visibleToolResultReminder(toolName, content); fallback != "" {
		effect.VisibleContext = fallback
	}
	return effect
}

func enterErrorReminder(errText, focusedStateID string) string {
	stateLine := ""
	if strings.TrimSpace(focusedStateID) != "" {
		stateLine = "\nfocused_state: " + focusedStateID
	}
	return fmt.Sprintf(`<system_reminder kind="tool_error" tool="enter">
error: %s%s
instruction: if switching target, call back first, then enter target; do not repeat enter for the current state.
</system_reminder>`, trimPreview(errText, 300), stateLine)
}

func toolErrorReminder(toolName, errText, content string) string {
	sanitized := sanitizeToolResultContent(content)
	if strings.TrimSpace(sanitized) == "" {
		return fmt.Sprintf(`<system_reminder kind="tool_error" tool="%s">
error: %s
</system_reminder>`, toolName, trimPreview(errText, 300))
	}
	return fmt.Sprintf(`<system_reminder kind="tool_error" tool="%s">
error: %s
</system_reminder>`, toolName, trimPreview(sanitized, 1000))
}

func visibleToolResultReminder(toolName, content string) string {
	switch toolName {
	case "wait", "enter", "back", "zone_out":
		return ""
	case "invoke":
		var payload map[string]any
		_ = json.Unmarshal([]byte(content), &payload)
		if ok, _ := payload["ok"].(bool); ok {
			return ""
		}
	}
	sanitized := sanitizeToolResultContent(content)
	if strings.TrimSpace(sanitized) == "" {
		return ""
	}
	return fmt.Sprintf(`<system_reminder kind="tool_result" tool="%s">
result: %s
</system_reminder>`, toolName, trimPreview(sanitized, 1000))
}

func toolContextMessage(content string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return ""
	}
	contextText, _ := payload["context"].(string)
	if strings.TrimSpace(contextText) != "" {
		return strings.TrimSpace(contextText)
	}
	title := common.AsString(payload["title"])
	articleContent := common.AsString(payload["content"])
	if title != "" && articleContent != "" {
		url := common.AsString(payload["url"])
		publishedAt := common.AsString(payload["publishedAt"])
		contentSource := common.AsString(payload["contentSource"])
		truncated, _ := payload["truncated"].(bool)
		maxChars := intValue(payload["maxChars"])
		return prompts.ITHomeArticleDetail(title, publishedAt, url, articleContent, contentSource == "rss_summary", truncated, maxChars)
	}
	return ""
}

func waitResultHasEvent(content string) bool {
	return stateEffectFromToolResult("wait", content).HasEvent
}

func isTerminalConversationAction(call agentruntime.ToolCall) bool {
	if call.Name == "zone_out" {
		return true
	}
	if call.Name != "invoke" {
		return false
	}
	name := invokeToolName(call)
	return name == "send_message" || name == "zone_out"
}

func invokeToolName(call agentruntime.ToolCall) string {
	name := common.AsString(call.Arguments["tool"])
	if name == "" {
		name = common.AsString(call.Arguments["toolName"])
	}
	if name == "" {
		name = common.AsString(call.Arguments["name"])
	}
	return name
}
