package agent

import (
	"context"
	"qqbot-ai/internal/agentruntime"
	"qqbot-ai/internal/common"
	"qqbot-ai/internal/llm"
	"strings"
)

type llmModelAdapter struct {
	client *llm.LLMClient
	usage  string
}

func (m llmModelAdapter) Chat(ctx context.Context, system string, messages []agentruntime.Message, tools []agentruntime.ToolDefinition, toolChoice any) (agentruntime.Completion, error) {
	req := llm.LLMChatRequest{
		System:     system,
		Messages:   toLLMMessages(messages),
		Tools:      toLLMTools(tools),
		ToolChoice: toolChoice,
	}
	resp, err := m.client.ChatUsage(ctx, m.usage, req)
	if err != nil {
		return agentruntime.Completion{}, err
	}
	message, _ := resp["message"].(map[string]any)
	toolCalls := toRuntimeToolCalls(message["toolCalls"])
	return agentruntime.Completion{
		Provider: common.AsString(resp["provider"]),
		Model:    common.AsString(resp["model"]),
		OS:       completionOS(resp, message, toolCalls),
		Message: agentruntime.Message{
			Role:      valueOr(common.AsString(message["role"]), "assistant"),
			Content:   common.AsString(message["content"]),
			ToolCalls: toolCalls,
		},
		Usage: toTokenUse(resp["usage"]),
	}, nil
}

func completionOS(resp map[string]any, message map[string]any, toolCalls []agentruntime.ToolCall) string {
	for _, value := range []string{
		common.AsString(resp["os"]),
		common.AsString(resp["OS"]),
		common.AsString(message["os"]),
		common.AsString(message["OS"]),
	} {
		if os := strings.TrimSpace(value); os != "" {
			return os
		}
	}
	for _, call := range toolCalls {
		if os := toolCallOS(call.Arguments); os != "" {
			return os
		}
		if call.Name == "invoke" {
			if args, _ := call.Arguments["arguments"].(map[string]any); args != nil {
				if os := toolCallOS(args); os != "" {
					return os
				}
			}
		}
	}
	return ""
}

func toolCallOS(args map[string]any) string {
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

func toLLMMessages(messages []agentruntime.Message) []llm.LLMMessage {
	out := make([]llm.LLMMessage, 0, len(messages))
	for _, message := range messages {
		out = append(out, llm.LLMMessage{
			Role:       message.Role,
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
			ToolCalls:  toLLMToolCalls(message.ToolCalls),
		})
	}
	return out
}

func toLLMTools(tools []agentruntime.ToolDefinition) []llm.LLMTool {
	out := make([]llm.LLMTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, llm.LLMTool{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
	}
	return out
}

func toLLMToolCalls(calls []agentruntime.ToolCall) []llm.LLMToolCall {
	out := make([]llm.LLMToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, llm.LLMToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
	}
	return out
}

func toRuntimeToolCalls(value any) []agentruntime.ToolCall {
	items, _ := value.([]any)
	out := make([]agentruntime.ToolCall, 0, len(items))
	for _, item := range items {
		call, _ := item.(map[string]any)
		if call == nil {
			continue
		}
		args, _ := call["arguments"].(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		out = append(out, agentruntime.ToolCall{
			ID:        common.AsString(call["id"]),
			Name:      common.AsString(call["name"]),
			Arguments: args,
		})
	}
	return out
}

func toTokenUse(value any) *agentruntime.TokenUse {
	usage, _ := value.(map[string]any)
	if usage == nil {
		return nil
	}
	return &agentruntime.TokenUse{
		PromptTokens:     int(numberAny(usage["promptTokens"])),
		CompletionTokens: int(numberAny(usage["completionTokens"])),
		TotalTokens:      int(numberAny(usage["totalTokens"])),
	}
}

func numberAny(v any) float64 {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case float64:
		return x
	default:
		return 0
	}
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
