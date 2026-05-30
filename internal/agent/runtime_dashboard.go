package agent

import (
	"encoding/json"
	"qqbot-ai/internal/agentruntime"
	"qqbot-ai/internal/common"
	"qqbot-ai/internal/llm"
	"strings"
	"time"
)

func (a *AgentRuntime) appendContext(item DashboardContextItem) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.contextItems = append(a.contextItems, item)
	if len(a.contextItems) > 400 {
		a.contextItems = a.contextItems[len(a.contextItems)-400:]
	}
}

func (a *AgentRuntime) Snapshot(llm *llm.LLMClient) AgentDashboardSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := common.ISO(time.Now())
	lastActivity := ""
	if a.lastActivity != nil {
		lastActivity = common.ISO(*a.lastActivity)
	}
	recent := append([]DashboardContextItem(nil), a.contextItems...)
	if len(recent) > 40 {
		recent = recent[len(recent)-40:]
	}
	runtime := DashboardRuntimeSummary{
		Initialized:          a.initialized,
		LoopState:            a.loopState,
		LastError:            a.lastError,
		LastActivityAt:       nullableString(lastActivity),
		LastRoundCompletedAt: nil,
		LastCompactionAt:     nil,
	}
	contextSummary := DashboardContextSummary{
		MessageCount:                  len(a.contextItems),
		CompactionTotalTokenThreshold: a.cfg.Server.Agent.ContextCompactionTotalTokenThreshold,
		RecentItems:                   recent,
		RecentItemsTruncated:          len(a.contextItems) > len(recent),
	}
	activity := DashboardActivitySummary{
		LastToolCall:          a.lastToolCall,
		LastToolResultPreview: a.lastToolResult,
		LastLlmCall:           a.lastLlmCall,
	}
	pendingStoryCount := len(a.storyQueue)
	if a.store != nil {
		pendingStoryCount = a.store.CountStoryLedgerAfter("root", a.storyLastSeq)
	}
	focused := a.session.focused()
	return AgentDashboardSnapshot{
		GeneratedAt: now,
		Agents: []AgentDashboardAgent{
			{
				ID:       "root",
				Kind:     "root",
				Label:    "Root Agent",
				Runtime:  runtime,
				Context:  contextSummary,
				Activity: activity,
				Session: &DashboardSessionSummary{
					FocusedStateID:          focused,
					FocusedStateDisplayName: a.session.displayName(focused),
					FocusedStateDescription: a.session.description(focused),
					StateStack:              a.session.stackSnapshot(),
					Children:                a.session.childrenSnapshot(),
					AvailableInvokeTools:    a.session.availableInvokeTools(),
				},
				Queue:     &DashboardQueueSummary{PendingEventCount: a.events.Count()},
				Providers: llm.ListProviders("agent")["providers"],
			},
			{
				ID:       "story",
				Kind:     "story",
				Label:    "Story Agent",
				Runtime:  runtime,
				Context:  contextSummary,
				Activity: activity,
				Story: &DashboardStorySummary{
					LastProcessedMessageSeq: a.storyLastSeq,
					PendingMessageCount:     pendingStoryCount,
					PendingBatch:            nil,
					BatchSize:               a.cfg.Server.Agent.Story.BatchSize,
					IdleFlushMs:             a.cfg.Server.Agent.Story.IdleFlushMs,
				},
			},
		},
	}
}

func contextItem(kind, label, text string) DashboardContextItem {
	return DashboardContextItem{Kind: kind, Label: label, Preview: trimPreview(text, 2000), Truncated: len([]rune(strings.TrimSpace(text))) > 2000}
}

func eventContextKind(eventType string) string {
	switch eventType {
	case "napcat_group_message", "napcat_private_message":
		return "qq_message"
	case "wake", "news_article_ingested", "news_articles_ingested":
		return "system_reminder"
	default:
		return "event"
	}
}

func eventMessageRole(eventType string) string {
	switch eventType {
	case "wake", "news_article_ingested", "news_articles_ingested":
		return "system"
	default:
		return "user"
	}
}

func (a *AgentRuntime) setRuntimeError(err error) {
	now := time.Now()
	a.mu.Lock()
	a.lastError = &RuntimeError{Name: "AgentRuntimeError", Message: err.Error(), UpdatedAt: common.ISO(now)}
	a.loopState = "idle"
	a.lastActivity = &now
	a.mu.Unlock()
	if a.ctx != nil {
		a.hooks.OnRuntimeError(a.ctx, a, err)
	}
}

func (a *AgentRuntime) recordToolExecution(execution agentruntime.ToolExecution) {
	args, _ := json.Marshal(execution.Call.Arguments)
	a.mu.Lock()
	a.lastToolCall = &DashboardToolCall{Name: execution.Call.Name, ArgumentsPreview: trimPreview(string(args), 300), UpdatedAt: common.ISO(time.Now())}
	a.lastToolResult = new(execution.Result.Content)
	a.mu.Unlock()
	a.appendContext(contextItem("tool_result", execution.Call.Name, execution.Result.Content))
}

func (a *AgentRuntime) recordLLMCall(completion agentruntime.Completion) {
	var total *int
	if completion.Usage != nil && completion.Usage.TotalTokens > 0 {
		total = new(completion.Usage.TotalTokens)
	}
	names := make([]string, 0, len(completion.Message.ToolCalls))
	for _, call := range completion.Message.ToolCalls {
		names = append(names, call.Name)
	}
	a.mu.Lock()
	if total != nil {
		a.lastTotalTokens = *total
	}
	a.lastLlmCall = &DashboardLlmCall{
		Provider:                completion.Provider,
		Model:                   completion.Model,
		OSPreview:               trimPreview(completion.OS, 500),
		AssistantContentPreview: trimPreview(completion.Message.Content, 300),
		ToolCallNames:           names,
		TotalTokens:             total,
		UpdatedAt:               common.ISO(time.Now()),
	}
	a.mu.Unlock()
}
