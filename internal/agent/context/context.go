package contextstore

import (
	"sync"

	"qqbot-ai/internal/agentruntime"
)

// Context 保存根 Agent 的系统提示词、聊天消息和原始事件。
//
// 该结构刻意保持轻量并保证线程安全，方便运行循环和工具追加
// 观测结果，同时不把存储细节泄漏到能力包中。
type Context struct {
	mu           sync.Mutex
	SystemPrompt string
	Messages     []agentruntime.Message
	Events       []any
}

// 使用给定系统提示词创建 Agent 上下文。
func New(systemPrompt string) *Context {
	return &Context{SystemPrompt: systemPrompt}
}

// AppendMessages 将 LLM 可见的消息追加到会话中。
func (c *Context) AppendMessages(messages ...agentruntime.Message) {
	c.mu.Lock()
	c.Messages = append(c.Messages, messages...)
	c.mu.Unlock()
}

// AppendEvent 记录非消息事件，供仪表盘展示和后续转换使用。
func (c *Context) AppendEvent(event any) {
	c.mu.Lock()
	c.Events = append(c.Events, event)
	c.mu.Unlock()
}

// Snapshot 返回提示词、消息和事件的副本。
func (c *Context) Snapshot() (string, []agentruntime.Message, []any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.SystemPrompt, append([]agentruntime.Message(nil), c.Messages...), append([]any(nil), c.Events...)
}

// ReplaceMessages 在压缩或恢复后覆盖当前会话消息。
func (c *Context) ReplaceMessages(messages []agentruntime.Message) {
	c.mu.Lock()
	c.Messages = append([]agentruntime.Message(nil), messages...)
	c.mu.Unlock()
}

// Compact 保留最新消息，并在前面加入摘要消息。
func Compact(messages []agentruntime.Message, keep int, summary string) []agentruntime.Message {
	if keep <= 0 || len(messages) <= keep {
		return messages
	}
	out := []agentruntime.Message{{Role: "system", Content: "Conversation summary:\n" + summary}}
	out = append(out, messages[len(messages)-keep:]...)
	return out
}
