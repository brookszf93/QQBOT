package terminal

import (
	"context"
	"encoding/json"
	"fmt"

	"qqbot-ai/internal/agentruntime"
)

// BashTool 将终端命令执行暴露为 Agent 工具。
type BashTool struct{ Service *Service }

func (t BashTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "bash", Description: "执行一条 shell 命令", Parameters: agentruntime.ObjectSchema(map[string]any{"command": map[string]any{"type": "string"}})}
}
func (t BashTool) Kind() string { return "business" }
func (t BashTool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	if t.Service == nil {
		return agentruntime.ToolResult{}, fmt.Errorf("terminal service is nil")
	}
	command, _ := call.Arguments["command"].(string)
	out, err := t.Service.Run(ctx, command)
	data, _ := json.Marshal(out)
	return agentruntime.ToolResult{Kind: "business", Content: string(data)}, err
}

// ReadBashOutputTool 按输出 ID 读取已保存的命令输出。
type ReadBashOutputTool struct{ Service *Service }

func (t ReadBashOutputTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "read_bash_output", Description: "读取 bash 输出", Parameters: agentruntime.ObjectSchema(map[string]any{"outputId": map[string]any{"type": "string"}})}
}
func (t ReadBashOutputTool) Kind() string { return "business" }
func (t ReadBashOutputTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	outputID, _ := call.Arguments["outputId"].(string)
	out, ok := t.Service.Read(outputID)
	if !ok {
		return agentruntime.ToolResult{Kind: "business", Content: `{"ok":false,"error":"OUTPUT_NOT_FOUND"}`}, nil
	}
	data, _ := json.Marshal(out)
	return agentruntime.ToolResult{Kind: "business", Content: string(data)}, nil
}
