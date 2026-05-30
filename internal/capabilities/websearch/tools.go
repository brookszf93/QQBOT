package websearch

import (
	"context"
	"encoding/json"

	"qqbot-ai/internal/agentruntime"
)

// SearchWebRawTool 向内部网页搜索任务 Agent 暴露原始搜索结果。
type SearchWebRawTool struct{ Service Service }

func (t SearchWebRawTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "search_web_raw", Description: "执行原始网页搜索", Parameters: agentruntime.ObjectSchema(map[string]any{"query": map[string]any{"type": "string"}, "maxResults": map[string]any{"type": "integer"}})}
}
func (t SearchWebRawTool) Kind() string { return "business" }
func (t SearchWebRawTool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	query, _ := call.Arguments["query"].(string)
	max := 5
	if v, ok := call.Arguments["maxResults"].(float64); ok && v > 0 {
		max = int(v)
	}
	results, err := t.Service.Search(ctx, query, max)
	if err != nil {
		return agentruntime.ToolResult{}, err
	}
	data, _ := json.Marshal(results)
	return agentruntime.ToolResult{Kind: "business", Content: string(data)}, nil
}

// FinalizeWebSearchTool 标记网页搜索任务的最终综合回答。
type FinalizeWebSearchTool struct{}

func (FinalizeWebSearchTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "finalize_web_search", Description: "完成网页搜索任务并返回答案", Parameters: agentruntime.ObjectSchema(map[string]any{"answer": map[string]any{"type": "string"}, "sources": map[string]any{"type": "array"}})}
}
func (FinalizeWebSearchTool) Kind() string { return "control" }
func (FinalizeWebSearchTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	data, _ := json.Marshal(call.Arguments)
	return agentruntime.ToolResult{Kind: "control", Content: string(data)}, nil
}

// SearchWebTool 是面向根 Agent 的网页搜索能力。
type SearchWebTool struct{ Service Service }

func (t SearchWebTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "search_web", Description: "搜索互联网并返回结果", Parameters: agentruntime.ObjectSchema(map[string]any{"query": map[string]any{"type": "string"}})}
}
func (t SearchWebTool) Kind() string { return "business" }
func (t SearchWebTool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	return SearchWebRawTool{Service: t.Service}.Execute(ctx, call)
}
