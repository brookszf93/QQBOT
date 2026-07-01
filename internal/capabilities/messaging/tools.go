package messaging

import (
	"context"
	"encoding/json"
	"fmt"

	"QqBot/internal/agentruntime"
)

// Sender 是 SendMessageTool 所需的 NapCat 消息发送能力子集。
type Sender interface {
	SendGroupMessage(groupID, message string) (int, error)
	SendPrivateMessage(userID, message string) (int, error)
}

// SendMessageTool 向群聊或私聊发送文本。
type SendMessageTool struct{ Sender Sender }

func (t SendMessageTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "send_message", Description: "向指定群聊或私聊发送消息", Parameters: agentruntime.ObjectSchema(map[string]any{
		"targetType": map[string]any{"type": "string", "enum": []string{"group", "private"}},
		"targetId":   map[string]any{"type": "string"},
		"message":    map[string]any{"type": "string"},
	})}
}
func (t SendMessageTool) Kind() string { return "business" }
func (t SendMessageTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	if t.Sender == nil {
		return agentruntime.ToolResult{}, fmt.Errorf("消息发送器不可用")
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
