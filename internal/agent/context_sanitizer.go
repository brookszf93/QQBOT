package agent

import (
	"encoding/json"
	"fmt"
	"qqbot-ai/internal/agentruntime"
	"strings"
)

const (
	rootContextMaxMessageChars = 12000
	rootContextMaxMessages     = 120
	rootContextMaxTotalChars   = 70000
)

func sanitizeRootMessages(messages []agentruntime.Message) []agentruntime.Message {
	if len(messages) == 0 {
		return nil
	}
	cleaned := make([]agentruntime.Message, 0, len(messages))
	for _, msg := range messages {
		msg.Content = strings.TrimSpace(msg.Content)
		if msg.Content == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		if len([]rune(msg.Content)) > rootContextMaxMessageChars {
			msg.Content = trimPreview(msg.Content, rootContextMaxMessageChars)
		}
		cleaned = append(cleaned, msg)
	}
	cleaned = dropNoisyToolResultReminders(cleaned)
	cleaned = keepRecentWaitToolTurns(cleaned, 3)
	cleaned = keepRecentStateReminders(cleaned, 3, 2)
	trimmed := trimRootMessagesToBudget(cleaned, rootContextMaxMessages, rootContextMaxTotalChars)
	return dropOrphanToolMessages(trimLeadingToolMessages(trimmed))
}

func keepRecentWaitToolTurns(messages []agentruntime.Message, max int) []agentruntime.Message {
	if max <= 0 || len(messages) == 0 {
		return messages
	}
	keep := make([]bool, len(messages))
	waitSeen := 0
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if isWaitAssistantMessage(msg) {
			waitSeen++
			keep[i] = waitSeen <= max
			continue
		}
		if msg.Role == "tool" && isWaitResumeContent(msg.Content) {
			keep[i] = waitSeen <= max
			continue
		}
		keep[i] = true
	}
	out := make([]agentruntime.Message, 0, len(messages))
	for i, msg := range messages {
		if keep[i] {
			out = append(out, msg)
		}
	}
	return out
}

func isWaitResumeContent(content string) bool {
	return strings.HasPrefix(strings.TrimSpace(content), "休息结束了")
}

func isWaitAssistantMessage(msg agentruntime.Message) bool {
	if msg.Role != "assistant" {
		return false
	}
	for _, call := range msg.ToolCalls {
		if call.Name == "wait" {
			return true
		}
	}
	return false
}

func dropNoisyToolResultReminders(messages []agentruntime.Message) []agentruntime.Message {
	out := make([]agentruntime.Message, 0, len(messages))
	for _, msg := range messages {
		content := msg.Content
		if strings.Contains(content, "<system_reminder>工具 wait 执行结果：") ||
			strings.Contains(content, "<system_reminder>工具 back 执行结果：") ||
			strings.Contains(content, "<system_reminder>工具 zone_out 执行结果：") ||
			strings.Contains(content, `kind="tool_result" tool="wait"`) ||
			strings.Contains(content, `kind="tool_result" tool="back"`) ||
			strings.Contains(content, `kind="tool_result" tool="zone_out"`) ||
			strings.Contains(content, "\"availableChildren\"") {
			continue
		}
		out = append(out, msg)
	}
	return out
}

func sanitizeToolResultContent(content string) string {
	if strings.TrimSpace(content) == "" {
		return content
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return trimPreview(content, 2000)
	}
	changed := false
	if _, ok := payload["context"]; ok {
		delete(payload, "context")
		changed = true
	}
	if articleContent, ok := payload["content"].(string); ok && len([]rune(articleContent)) > 500 {
		payload["contentPreview"] = trimPreview(articleContent, 500)
		delete(payload, "content")
		changed = true
	}
	if !changed {
		return content
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return trimPreview(content, 2000)
	}
	return string(data)
}

func keepRecentStateReminders(messages []agentruntime.Message, maxWake, maxPortal int) []agentruntime.Message {
	keep := make([]bool, len(messages))
	wakeSeen := 0
	portalSeen := 0
	for i := len(messages) - 1; i >= 0; i-- {
		content := messages[i].Content
		switch {
		case isWakeReminderMessage(content):
			wakeSeen++
			keep[i] = wakeSeen <= maxWake
		case isPortalReminderMessage(content):
			portalSeen++
			keep[i] = portalSeen <= maxPortal
		default:
			keep[i] = true
		}
	}
	out := make([]agentruntime.Message, 0, len(messages))
	for i, msg := range messages {
		if keep[i] {
			out = append(out, msg)
		}
	}
	return out
}

func trimRootMessagesToBudget(messages []agentruntime.Message, maxMessages, maxChars int) []agentruntime.Message {
	totalChars := 0
	for _, msg := range messages {
		totalChars += len([]rune(msg.Content))
	}
	dropped := 0
	for len(messages) > 30 && (len(messages) > maxMessages || totalChars > maxChars) {
		totalChars -= len([]rune(messages[0].Content))
		messages = messages[1:]
		dropped++
	}
	messages = trimLeadingToolMessages(messages)
	if dropped > 0 {
		summary := fmt.Sprintf("<conversation_summary>较早的 %d 条运行时上下文已清理，以避免请求过大；保留最近对话、状态和必要工具结果。</conversation_summary>", dropped)
		messages = append([]agentruntime.Message{{Role: "user", Content: summary}}, messages...)
	}
	return messages
}

func trimLeadingToolMessages(messages []agentruntime.Message) []agentruntime.Message {
	for len(messages) > 0 && messages[0].Role == "tool" {
		messages = messages[1:]
	}
	return messages
}

func dropOrphanToolMessages(messages []agentruntime.Message) []agentruntime.Message {
	toolOutputs := map[string]bool{}
	for _, msg := range messages {
		if msg.Role == "tool" && strings.TrimSpace(msg.ToolCallID) != "" {
			toolOutputs[msg.ToolCallID] = true
		}
	}
	seenToolCalls := map[string]bool{}
	out := make([]agentruntime.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "assistant" {
			filteredCalls := make([]agentruntime.ToolCall, 0, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				if strings.TrimSpace(call.ID) != "" && toolOutputs[call.ID] {
					seenToolCalls[call.ID] = true
					filteredCalls = append(filteredCalls, call)
				}
			}
			msg.ToolCalls = filteredCalls
			if strings.TrimSpace(msg.Content) == "" && len(msg.ToolCalls) == 0 {
				continue
			}
			out = append(out, msg)
			continue
		}
		if msg.Role == "tool" {
			if strings.TrimSpace(msg.ToolCallID) == "" || !seenToolCalls[msg.ToolCallID] {
				continue
			}
		}
		out = append(out, msg)
	}
	return out
}

func isWakeReminderMessage(content string) bool {
	return strings.Contains(content, `<system_reminder kind="time"`) ||
		strings.Contains(content, "<system_reminder>当前时间为北京时间")
}

func isPortalReminderMessage(content string) bool {
	return strings.Contains(content, `current_state: portal`) ||
		(strings.Contains(content, "你当前处于门户状态。") && strings.Contains(content, "可进入目标"))
}
