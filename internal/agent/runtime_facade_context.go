package agent

import (
	"context"
	"qqbot-ai/internal/agentruntime"
	"strings"
)

func (a *AgentRuntime) compactRootContextIfNeeded() {
	threshold := a.cfg.Server.Agent.ContextCompactionTotalTokenThreshold
	manager := a.ensureRootContext()
	summary, compacted := manager.CompactIfNeeded(a.lastTotalTokens, threshold, a.summarizeMessagesWithLLM)
	if !compacted {
		return
	}
	a.syncRootMessagesFromContext()
	a.appendContext(contextItem("system_reminder", "context_compaction", summary))
}

func (a *AgentRuntime) sanitizeRootContext() {
	manager := a.ensureRootContext()
	manager.Sanitize()
	a.syncRootMessagesFromContext()
}

func (a *AgentRuntime) ensureRootContext() *RootContextManager {
	if a.rootContext == nil {
		a.rootContext = NewRootContextManager(a.rootMessages)
	}
	return a.rootContext
}

func (a *AgentRuntime) replaceRootMessages(messages []agentruntime.Message) {
	a.rootMessages = append([]agentruntime.Message(nil), messages...)
	a.rootContext = NewRootContextManager(a.rootMessages)
}

func (a *AgentRuntime) appendRootContext(layer RootContextLayer, msg agentruntime.Message) {
	a.ensureRootContext().Append(layer, msg)
	a.syncRootMessagesFromContext()
}

func (a *AgentRuntime) syncRootMessagesFromContext() {
	if a.rootContext == nil {
		return
	}
	a.rootMessages = a.rootContext.Messages()
}

func (a *AgentRuntime) rootPromptMessages() []agentruntime.Message {
	return a.ensureRootContext().Messages()
}

func (a *AgentRuntime) rootContextLen() int {
	return a.ensureRootContext().Len()
}

func (a *AgentRuntime) summarizeMessagesWithLLM(messages []agentruntime.Message) string {
	fallback := summarizeMessages(messages)
	if a.llm == nil || len(messages) == 0 {
		return fallback
	}
	body := []string{}
	for _, msg := range messages {
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}
		body = append(body, msg.Role+": "+trimPreview(msg.Content, 1000))
	}
	if len(body) == 0 {
		return fallback
	}
	model := llmModelAdapter{client: a.llm, usage: "contextSummarizer"}
	completion, err := model.Chat(context.Background(),
		"你是上下文压缩器。请把输入压缩成供同一个 QQ 群聊 Agent 稍后继续使用的中文工作记忆，保留长期背景、当前话题、已执行动作和后续可接的点。只输出摘要。",
		[]agentruntime.Message{{Role: "user", Content: strings.Join(body, "\n\n")}},
		nil,
		"none",
	)
	if err != nil || strings.TrimSpace(completion.Message.Content) == "" {
		return fallback
	}
	return strings.TrimSpace(completion.Message.Content)
}
