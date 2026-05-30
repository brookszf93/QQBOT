package agent

import (
	"context"
	"encoding/json"
	"qqbot-ai/internal/agentruntime"
	"qqbot-ai/internal/common"
	"strings"
	"sync/atomic"
	"time"
)

func (a *AgentRuntime) rootControlTools() *agentruntime.ToolCatalog {
	return agentruntime.NewToolCatalog(
		facadeEnterTool{Session: a.session},
		facadeBackTool{Session: a.session},
		facadeZoneOutTool{Session: a.session},
		facadeWaitTool{Queue: a.events, MaxWait: a.waitToolMaxWait()},
		facadeInvokeTool{Tools: a.rootTools, Session: a.session},
	)
}

func (a *AgentRuntime) waitToolMaxWait() time.Duration {
	maxWait := 10 * time.Minute
	if a.cfg != nil && a.cfg.Server.Agent.WaitToolMaxWaitMs > 0 {
		configured := time.Duration(a.cfg.Server.Agent.WaitToolMaxWaitMs) * time.Millisecond
		maxWait = configured
	}
	return maxWait
}

type facadeWaitTool struct {
	Queue   *EventQueue
	MaxWait time.Duration
}

func (facadeWaitTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "wait", Description: "等待下一条事件", Parameters: agentruntime.ObjectSchema(map[string]any{
		"os": map[string]any{"type": "string", "description": "可选。公开展示用的一句话 OS/旁白，不是私密推理链。"},
	})}
}

func (facadeWaitTool) Kind() string { return "control" }

func (t facadeWaitTool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	timeout := t.MaxWait
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if v, ok := call.Arguments["timeoutMs"].(float64); ok && v > 0 {
		requested := time.Duration(v) * time.Millisecond
		if requested < timeout {
			timeout = requested
		}
	}
	if t.Queue == nil {
		timer := time.NewTimer(timeout)
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		timer.Stop()
		return agentruntime.ToolResult{Kind: "control", Content: waitResumeContent("timeout")}, nil
	}
	var timeoutFired atomic.Bool
	timer := time.AfterFunc(timeout, func() {
		timeoutFired.Store(true)
		t.Queue.Enqueue(AgentEvent{Type: "wake", Data: map[string]any{"reason": "wait_timeout"}})
	})
	defer timer.Stop()
	t.Queue.WaitForEvent(ctx)
	reason := "event"
	if timeoutFired.Load() {
		reason = "timeout"
	}
	return agentruntime.ToolResult{Kind: "control", Content: waitResumeContent(reason)}, nil
}

func waitResumeContent(reason string) string {
	switch reason {
	case "timeout":
		return "休息结束了（等待自然超时）"
	default:
		return "休息结束了（有新事件到达，下一轮会处理）"
	}
}

type facadeInvokeTool struct {
	Tools   *agentruntime.ToolCatalog
	Session *rootSession
}

func (facadeInvokeTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "invoke", Description: "调用一个业务工具", Parameters: agentruntime.ObjectSchema(map[string]any{
		"tool":      map[string]any{"type": "string"},
		"arguments": map[string]any{"type": "object"},
		"os":        map[string]any{"type": "string", "description": "可选。公开展示用的一句话 OS/旁白，不是私密推理链。"},
	})}
}

func (facadeInvokeTool) Kind() string { return "control" }

func (t facadeInvokeTool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
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
		return agentruntime.ToolResult{Kind: "control", Content: mustSessionJSON(map[string]any{
			"ok":    false,
			"error": "NO_TOOLS",
		})}, nil
	}
	if t.Session != nil && !t.Session.canInvoke(name) {
		data, _ := json.Marshal(map[string]any{
			"ok":             false,
			"error":          "INVOKE_TOOL_NOT_AVAILABLE",
			"tool":           name,
			"focusedStateId": t.Session.focused(),
			"availableTools": t.Session.availableInvokeTools(),
			"allowedTools":   t.Session.availableInvokeTools(),
		})
		return agentruntime.ToolResult{Kind: "control", Content: string(data)}, nil
	}
	if t.Session != nil && name == "zone_out" {
		t.Session.zoneOut(common.AsString(args["thought"]))
		return agentruntime.ToolResult{Kind: "business", Content: mustSessionJSON(map[string]any{
			"ok":             true,
			"zonedOut":       true,
			"focusedStateId": t.Session.focused(),
			"allowedTools":   t.Session.availableInvokeTools(),
		})}, nil
	}
	if t.Session != nil && name == "send_message" {
		if strings.TrimSpace(common.AsString(args["message"])) == "" {
			return agentruntime.ToolResult{Kind: "business", Content: mustSessionJSON(map[string]any{
				"ok":        false,
				"error":     "EMPTY_MESSAGE",
				"message":   "send_message message is empty",
				"ephemeral": true,
			})}, nil
		}
		if target, ok := t.Session.currentChatTarget(); ok {
			if common.AsString(args["targetType"]) == "" {
				args["targetType"] = target.Type
			}
			if common.AsString(args["targetId"]) == "" {
				args["targetId"] = target.ID
			}
		}
	}
	return t.Tools.Execute(ctx, agentruntime.ToolCall{ID: call.ID + ":invoke", Name: name, Arguments: args})
}
