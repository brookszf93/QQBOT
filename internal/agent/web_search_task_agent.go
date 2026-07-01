package agent

import (
	"QqBot/internal/agentruntime"
	"QqBot/internal/capabilities/websearch"
	"QqBot/internal/common"
	"QqBot/internal/prompts"
	"context"
	"encoding/json"
)

type WebSearchTaskAgentTool struct {
	service         websearch.Service
	model           agentruntime.Model
	systemPrompt    func() string
	contextMessages func() []agentruntime.Message
	topLevelTools   func() *agentruntime.ToolCatalog
}

func NewWebSearchTaskAgentTool(service websearch.Service) *WebSearchTaskAgentTool {
	return &WebSearchTaskAgentTool{service: service}
}

func (t *WebSearchTaskAgentTool) SetModel(model agentruntime.Model) {
	t.model = model
}

func (t *WebSearchTaskAgentTool) SetTaskContext(systemPrompt func() string, contextMessages func() []agentruntime.Message, topLevelTools func() *agentruntime.ToolCatalog) {
	t.systemPrompt = systemPrompt
	t.contextMessages = contextMessages
	t.topLevelTools = topLevelTools
}

func (t *WebSearchTaskAgentTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "search_web", Description: "读取网页 URL 或把自然语言问题交给网页搜索子 Agent，并返回可靠摘要。完整 URL 会优先直接读取页面。", Parameters: agentruntime.ObjectSchema(map[string]any{
		"question": map[string]any{"type": "string", "description": "需要查询的自然语言问题或网页 URL。"},
		"query":    map[string]any{"type": "string", "description": "question 的兼容别名，可传自然语言问题或网页 URL。"},
	})}
}

func (t *WebSearchTaskAgentTool) Kind() string { return "business" }

func (t *WebSearchTaskAgentTool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	query := common.AsString(call.Arguments["query"])
	if query == "" {
		query = common.AsString(call.Arguments["question"])
	}
	if t.model == nil {
		raw, err := websearch.SearchWebRawTool{Service: t.service}.Execute(ctx, agentruntime.ToolCall{ID: call.ID + ":raw", Name: "search_web_raw", Arguments: map[string]any{"query": query, "maxResults": 5}})
		if err != nil {
			return raw, err
		}
		return raw, nil
	}

	tools := t.taskTools()
	kernel := agentruntime.ReActKernel{Model: t.model}
	systemPrompt := prompts.WebSearchSystemPrompt()
	messages := []agentruntime.Message{{Role: "user", Content: prompts.WebSearchInstruction(query)}}
	if t.contextMessages != nil {
		messages = append(t.contextMessages(), agentruntime.Message{Role: "user", Content: prompts.WebSearchInstruction(query)})
	}
	var last agentruntime.RoundResult
	for i := 0; i < 4; i++ {
		result, err := kernel.RunRound(ctx, agentruntime.RoundInput{
			SystemPrompt: systemPrompt,
			Messages:     messages,
			Tools:        tools,
			ToolChoice:   "required",
		})
		if err != nil {
			return agentruntime.ToolResult{}, err
		}
		last = result
		messages = append(messages, result.Assistant)
		for _, execution := range result.ToolExecutions {
			if execution.Call.Name == "finalize_web_search" || (execution.Call.Name == "invoke" && common.AsString(execution.Call.Arguments["tool"]) == "finalize_web_search") {
				return agentruntime.ToolResult{Kind: "business", Content: execution.Result.Content}, nil
			}
			messages = append(messages, agentruntime.Message{Role: "tool", ToolCallID: execution.Call.ID, Content: execution.Result.Content})
		}
	}
	content := last.Assistant.Content
	if content == "" {
		data, _ := json.Marshal(map[string]any{"answer": "网页搜索未能形成最终摘要", "sources": []any{}})
		content = string(data)
	}
	return agentruntime.ToolResult{Kind: "business", Content: content}, nil
}

func (t *WebSearchTaskAgentTool) taskTools() *agentruntime.ToolCatalog {
	return agentruntime.NewToolCatalog(
		websearch.SearchWebRawTool{Service: t.service},
		websearch.FinalizeWebSearchTool{},
	)
}

type outOfScopeTool struct {
	inner  agentruntime.Tool
	reason string
}

func (t outOfScopeTool) Definition() agentruntime.ToolDefinition { return t.inner.Definition() }
func (t outOfScopeTool) Kind() string                            { return t.inner.Kind() }
func (t outOfScopeTool) Execute(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	data, _ := json.Marshal(map[string]any{"ok": false, "error": "OUT_OF_SCOPE", "tool": t.Definition().Name, "message": t.reason})
	return agentruntime.ToolResult{Kind: t.Kind(), Content: string(data)}, nil
}

type webSearchInvokeTool struct {
	definition agentruntime.ToolDefinition
	tools      *agentruntime.ToolCatalog
}

func (t webSearchInvokeTool) Definition() agentruntime.ToolDefinition { return t.definition }
func (webSearchInvokeTool) Kind() string                              { return "control" }
func (t webSearchInvokeTool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	name := common.AsString(call.Arguments["tool"])
	args, _ := call.Arguments["arguments"].(map[string]any)
	if args == nil {
		args = map[string]any{}
		for key, value := range call.Arguments {
			if key == "tool" || key == "arguments" {
				continue
			}
			args[key] = value
		}
	}
	switch name {
	case "search_web_raw", "finalize_web_search":
		return t.tools.Execute(ctx, agentruntime.ToolCall{ID: call.ID + ":invoke", Name: name, Arguments: args})
	default:
		data, _ := json.Marshal(map[string]any{"ok": false, "error": "INVOKE_TOOL_NOT_FOUND", "tool": name, "message": "网页搜索子任务中只能调用 search_web_raw 或 finalize_web_search。", "availableTools": []string{"search_web_raw", "finalize_web_search"}})
		return agentruntime.ToolResult{Kind: "control", Content: string(data)}, nil
	}
}

func webSearchOutOfScopeReason(name string) string {
	switch name {
	case "enter":
		return `在网页搜索子任务中不可调用 enter。请用 invoke(tool="search_web_raw", ...) 检索，必要时反复，信息足够后用 invoke(tool="finalize_web_search", summary=...) 输出最终摘要。`
	case "back":
		return "在网页搜索子任务中不可调用 back。"
	case "back_to_portal":
		return "在网页搜索子任务中不可调用 back_to_portal。"
	case "wait":
		return "在网页搜索子任务中不可调用 wait。"
	case "search_web":
		return "网页搜索子任务内禁止再次调用 search_web，否则会无限嵌套。"
	case "search_memory":
		return "在网页搜索子任务中不可调用 search_memory。"
	case "help":
		return "在网页搜索子任务中不可调用 help。"
	default:
		return "该工具不属于当前网页搜索子任务的作用域。"
	}
}
