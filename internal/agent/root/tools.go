package root

import (
	"context"
	"encoding/json"
	"time"

	"qqbot-ai/internal/agentruntime"
)

// EnterTool 将根会话切换到指定子状态。
type EnterTool struct{}

func (EnterTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "enter", Description: "进入一个子状态", Parameters: agentruntime.ObjectSchema(map[string]any{"id": map[string]any{"type": "string"}})}
}
func (EnterTool) Kind() string { return "control" }
func (EnterTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	id := call.Arguments["id"]
	if id == nil {
		id = call.Arguments["stateId"]
	}
	data, _ := json.Marshal(map[string]any{"ok": true, "entered": id})
	return agentruntime.ToolResult{Kind: "control", Content: string(data)}, nil
}

// BackToPortalTool 将状态导航重置到 portal。
type BackToPortalTool struct{}

func (BackToPortalTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "back", Description: "回到上一级或主入口", Parameters: agentruntime.ObjectSchema(map[string]any{})}
}
func (BackToPortalTool) Kind() string { return "control" }
func (BackToPortalTool) Execute(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	return agentruntime.ToolResult{Kind: "control", Content: `{"ok":true,"stateId":"portal"}`}, nil
}

// ZoneOutTool 记录一次有意的空操作回应。
type ZoneOutTool struct{}

func (ZoneOutTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "zone_out", Description: "暂时发呆，不主动回应", Parameters: agentruntime.ObjectSchema(map[string]any{"thought": map[string]any{"type": "string"}})}
}
func (ZoneOutTool) Kind() string { return "control" }
func (ZoneOutTool) Execute(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	return agentruntime.ToolResult{Kind: "control", Content: `{"ok":true,"zonedOut":true}`}, nil
}

// WaitTool 阻塞当前 Agent 轮次，直到收到新事件或超时。
type WaitTool struct {
	Queue   *agentruntime.EventQueue[any]
	MaxWait time.Duration
}

func (WaitTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "wait", Description: "等待下一条事件", Parameters: agentruntime.ObjectSchema(map[string]any{"timeoutMs": map[string]any{"type": "integer"}})}
}
func (WaitTool) Kind() string { return "control" }
func (t WaitTool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	timeout := t.MaxWait
	if timeout <= 0 {
		timeout = time.Minute
	}
	if v, ok := call.Arguments["timeoutMs"].(float64); ok && v > 0 {
		timeout = time.Duration(v) * time.Millisecond
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if t.Queue == nil {
		<-waitCtx.Done()
		return agentruntime.ToolResult{Kind: "control", Content: `{"ok":true,"event":null}`}, nil
	}
	event, ok := t.Queue.Wait(waitCtx)
	data, _ := json.Marshal(map[string]any{"ok": true, "hasEvent": ok, "event": event})
	return agentruntime.ToolResult{Kind: "control", Content: string(data)}, nil
}

// InvokeTool 允许根 Agent 通过受控入口调用业务工具。
type InvokeTool struct {
	Tools *agentruntime.ToolCatalog
}

func (InvokeTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "invoke", Description: "调用一个业务工具", Parameters: agentruntime.ObjectSchema(map[string]any{"tool": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"}})}
}
func (InvokeTool) Kind() string { return "control" }
func (t InvokeTool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	name, _ := call.Arguments["tool"].(string)
	if name == "" {
		name, _ = call.Arguments["toolName"].(string)
	}
	if name == "" {
		name, _ = call.Arguments["name"].(string)
	}
	args, _ := call.Arguments["arguments"].(map[string]any)
	if args == nil {
		args = map[string]any{}
		for key, value := range call.Arguments {
			if key == "tool" || key == "toolName" || key == "name" || key == "arguments" {
				continue
			}
			args[key] = value
		}
	}
	if t.Tools == nil {
		return agentruntime.ToolResult{Kind: "control", Content: `{"ok":false,"error":"NO_TOOLS"}`}, nil
	}
	return t.Tools.Execute(ctx, agentruntime.ToolCall{ID: call.ID + ":invoke", Name: name, Arguments: args})
}
