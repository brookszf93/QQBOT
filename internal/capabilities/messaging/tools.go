package messaging

import (
	"context"
	"encoding/json"
	"fmt"

	"qqbot-ai/internal/agentruntime"
)

// Sender 是 SendMessageTool 所需的 NapCat 消息发送能力子集。
type Sender interface {
	SendGroupMessage(groupID, message string) (int, error)
	SendPrivateMessage(userID, message string) (int, error)
}

// SendMessageTool 向群聊或私聊发送文本。
type SendMessageTool struct{ Sender Sender }

func (t SendMessageTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "send_message", Description: "向当前群聊或私聊发送消息", Parameters: agentruntime.ObjectSchema(map[string]any{
		"targetType": map[string]any{"type": "string"},
		"targetId":   map[string]any{"type": "string"},
		"message":    map[string]any{"type": "string"},
		"os":         map[string]any{"type": "string", "description": "可选。公开展示用的一句话 OS/旁白，不是私密推理链；不会发送到 QQ。"},
	})}
}
func (t SendMessageTool) Kind() string { return "business" }
func (t SendMessageTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	if t.Sender == nil {
		return agentruntime.ToolResult{}, fmt.Errorf("sender is nil")
	}
	targetType, _ := call.Arguments["targetType"].(string)
	targetID, _ := call.Arguments["targetId"].(string)
	message, _ := call.Arguments["message"].(string)
	var id int
	var err error
	if targetType == "private" {
		id, err = t.Sender.SendPrivateMessage(targetID, message)
	} else {
		id, err = t.Sender.SendGroupMessage(targetID, message)
	}
	data, _ := json.Marshal(map[string]any{"messageId": id})
	return agentruntime.ToolResult{Kind: "business", Content: string(data)}, err
}
