package agentruntime

import (
	"context"
	"errors"
)

// Message 是 Go Agent 循环使用的运行时中立聊天消息格式。
type Message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoningContent,omitempty"`
	ToolCalls        []ToolCall `json:"toolCalls,omitempty"`
	ToolCallID       string     `json:"toolCallId,omitempty"`
}

// Completion 是 LLM 适配器返回的标准化模型响应。
type Completion struct {
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	Message  Message   `json:"message"`
	Usage    *TokenUse `json:"usage,omitempty"`
}

// TokenUse 对应供应商的 token 统计，用于指标和上下文压缩决策。
type TokenUse struct {
	PromptTokens     int `json:"promptTokens,omitempty"`
	CompletionTokens int `json:"completionTokens,omitempty"`
	TotalTokens      int `json:"totalTokens,omitempty"`
	CacheHitTokens   int `json:"cacheHitTokens,omitempty"`
	CacheMissTokens  int `json:"cacheMissTokens,omitempty"`
}

// Model 是 ReActKernel 所需的最小 LLM 接口。
type Model interface {
	Chat(context.Context, string, []Message, []ToolDefinition, any) (Completion, error)
}

// RoundInput 包含一次 ReAct 模型/工具轮次所需的全部状态。
type RoundInput struct {
	SystemPrompt string
	Messages     []Message
	Tools        *ToolCatalog
	ToolChoice   any
}

// ToolExecution 记录一次工具调用及其返回内容。
type ToolExecution struct {
	Call   ToolCall
	Result ToolResult
}

// RoundResult 保存助手消息以及所有已执行工具的信息。
type RoundResult struct {
	Completion     Completion
	Assistant      Message
	ToolExecutions []ToolExecution
}

type ModelErrorDecision struct {
	Handled bool
	Retry   bool
}

type ToolErrorDecision struct {
	Handled bool
	Result  *ToolResult
}

type ReActKernelExtension interface {
	OnModelError(context.Context, RoundInput, error) (ModelErrorDecision, error)
	OnToolError(context.Context, RoundInput, Completion, ToolCall, error) (ToolErrorDecision, error)
}

type ReActKernelExtensionBase struct{}

func (ReActKernelExtensionBase) OnModelError(context.Context, RoundInput, error) (ModelErrorDecision, error) {
	return ModelErrorDecision{}, nil
}

func (ReActKernelExtensionBase) OnToolError(context.Context, RoundInput, Completion, ToolCall, error) (ToolErrorDecision, error) {
	return ToolErrorDecision{}, nil
}

// ReActKernel 执行一轮模型调用，并分发返回的工具调用。
type ReActKernel struct {
	Model      Model
	Extensions []ReActKernelExtension
}

// RunRound 调用一次模型，并执行响应中的全部工具调用。
func (k ReActKernel) RunRound(ctx context.Context, input RoundInput) (RoundResult, error) {
	if k.Model == nil {
		return RoundResult{}, errors.New("react kernel requires model")
	}
	tools := []ToolDefinition{}
	if input.Tools != nil {
		tools = input.Tools.Definitions()
	}
	var completion Completion
	for {
		var err error
		completion, err = k.Model.Chat(ctx, input.SystemPrompt, input.Messages, tools, input.ToolChoice)
		if err == nil {
			break
		}
		retry := false
		for _, extension := range k.Extensions {
			decision, extensionErr := extension.OnModelError(ctx, input, err)
			if extensionErr != nil {
				return RoundResult{}, extensionErr
			}
			if decision.Handled && decision.Retry {
				retry = true
				break
			}
		}
		if !retry {
			return RoundResult{}, err
		}
	}
	for i, call := range completion.Message.ToolCalls {
		completion.Message.ToolCalls[i] = NormalizeToolCall(call)
	}
	result := RoundResult{Completion: completion, Assistant: completion.Message}
	if input.Tools == nil {
		return result, nil
	}
	for _, call := range completion.Message.ToolCalls {
		toolResult, err := input.Tools.ExecuteRaw(ctx, call)
		if err != nil {
			handled := false
			for _, extension := range k.Extensions {
				decision, extensionErr := extension.OnToolError(ctx, input, completion, call, err)
				if extensionErr != nil {
					return result, extensionErr
				}
				if decision.Handled {
					handled = true
					if decision.Result != nil {
						toolResult = *decision.Result
					} else {
						toolResult = ToolResult{}
					}
					break
				}
			}
			if !handled {
				toolResult = input.Tools.ErrorResult(call, err)
			}
		}
		result.ToolExecutions = append(result.ToolExecutions, ToolExecution{Call: call, Result: toolResult})
	}
	return result, nil
}
