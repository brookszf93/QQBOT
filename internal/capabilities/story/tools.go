package story

import (
	"context"
	"encoding/json"
	"fmt"

	"qqbot-ai/internal/agentruntime"
)

// CreateStoryTool 允许 Story 任务 Agent 持久化新写出的 Story。
type CreateStoryTool struct {
	Service               Service
	SourceMessageSeqStart int
	SourceMessageSeqEnd   int
}

func (t CreateStoryTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "create_story", Description: "创建一条故事记忆", Parameters: agentruntime.ObjectSchema(map[string]any{"markdown": map[string]any{"type": "string"}})}
}
func (t CreateStoryTool) Kind() string { return "business" }
func (t CreateStoryTool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	markdown, _ := call.Arguments["markdown"].(string)
	story, err := t.Service.Create(ctx, Story{Markdown: markdown, SourceMessageSeqStart: t.SourceMessageSeqStart, SourceMessageSeqEnd: t.SourceMessageSeqEnd})
	if err != nil {
		return agentruntime.ToolResult{}, err
	}
	return jsonResult(story), nil
}

// RewriteStoryTool 替换已有 Story 的 Markdown 内容。
type RewriteStoryTool struct {
	Service               Service
	SourceMessageSeqStart int
	SourceMessageSeqEnd   int
}

func (t RewriteStoryTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "rewrite_story", Description: "重写一条故事记忆", Parameters: agentruntime.ObjectSchema(map[string]any{"id": map[string]any{"type": "string"}, "markdown": map[string]any{"type": "string"}})}
}
func (t RewriteStoryTool) Kind() string { return "business" }
func (t RewriteStoryTool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	id, _ := call.Arguments["id"].(string)
	markdown, _ := call.Arguments["markdown"].(string)
	if id == "" {
		return agentruntime.ToolResult{}, fmt.Errorf("id is required")
	}
	story, err := t.Service.Create(ctx, Story{ID: id, Markdown: markdown, SourceMessageSeqStart: t.SourceMessageSeqStart, SourceMessageSeqEnd: t.SourceMessageSeqEnd})
	if err != nil {
		return agentruntime.ToolResult{}, err
	}
	return jsonResult(story), nil
}

// SearchMemoryTool 向根 Agent 暴露 Story 召回能力。
type SearchMemoryTool struct{ Service Service }

func (t SearchMemoryTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "search_memory", Description: "搜索故事记忆", Parameters: agentruntime.ObjectSchema(map[string]any{"query": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}})}
}
func (t SearchMemoryTool) Kind() string { return "business" }
func (t SearchMemoryTool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	query, _ := call.Arguments["query"].(string)
	topK := 5
	if v, ok := call.Arguments["topK"].(float64); ok && v > 0 {
		topK = int(v)
	}
	if v, ok := call.Arguments["limit"].(float64); ok && v > 0 {
		topK = int(v)
	}
	items, err := t.Service.Search(ctx, query, topK)
	if err != nil {
		return agentruntime.ToolResult{}, err
	}
	return jsonResult(items), nil
}

// FinishStoryBatchTool 是 Story 批处理使用的控制标记。
type FinishStoryBatchTool struct{}

func (FinishStoryBatchTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "finish_story_batch", Description: "结束当前故事批处理", Parameters: agentruntime.ObjectSchema(map[string]any{"reason": map[string]any{"type": "string"}})}
}
func (FinishStoryBatchTool) Kind() string { return "control" }
func (FinishStoryBatchTool) Execute(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	return agentruntime.ToolResult{Kind: "control", Content: `{"ok":true,"finished":true}`}, nil
}

func jsonResult(v any) agentruntime.ToolResult {
	data, _ := json.Marshal(v)
	return agentruntime.ToolResult{Kind: "business", Content: string(data)}
}
