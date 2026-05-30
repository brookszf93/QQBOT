package agent

import (
	"context"
	"log"
	"qqbot-ai/internal/agentruntime"
	"qqbot-ai/internal/common"
	"qqbot-ai/internal/prompts"
	"strings"
	"time"
)

func (a *AgentRuntime) rootLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		shouldRun := a.consumeEvents()
		a.appendWakeReminderIfNeeded()
		if !shouldRun {
			a.waitForNextEvent(ctx)
			continue
		}
		if a.runRootRound(ctx) {
			a.waitForNextEvent(ctx)
		}
	}
}

func (a *AgentRuntime) consumeEvents() bool {
	a.mu.Lock()
	a.loopState = "consuming_events"
	a.mu.Unlock()
	events := a.events.DequeueAll()
	if len(events) > 0 {
		log.Printf("[AGENT] consume events count=%d", len(events))
	}
	shouldRunRoot := false
	var storyBatch []agentruntime.Message
	var replyTarget *chatReplyTarget
	for _, event := range events {
		eventContext := a.renderEventContext(event)
		rendered, visible, shouldTrigger := a.session.consume(event, eventContext)
		log.Printf("[AGENT] event type=%s target=%+v preview=%q", event.Type, targetFromEvent(event), trimPreview(rendered, 300))
		if strings.TrimSpace(rendered) != "" {
			a.appendContext(contextItem(eventContextKind(event.Type), event.Type, rendered))
		}
		if visible {
			a.appendRootContext(RootContextLayerEvent, agentruntime.Message{Role: eventMessageRole(event.Type), Content: rendered})
		}
		if shouldTrigger && a.eventShouldTriggerRoot(event) {
			shouldRunRoot = true
		}
		if (event.Type == "napcat_group_message" || event.Type == "napcat_private_message") && !boolValue(event.Data["startup"]) {
			storyBatch = append(storyBatch, agentruntime.Message{Role: "user", Content: eventContext})
			a.store.AddStoryLedger("root", "user", eventContext)
			if target := targetFromEvent(event); target.ID != "" {
				replyTarget = &target
			}
		}
	}
	if notification := a.session.flushNotificationsIfReady(a.notificationBatchWindow()); notification != "" {
		a.appendRootContext(RootContextLayerEvent, agentruntime.Message{Role: "user", Content: notification})
		a.appendContext(contextItem("system_reminder", "cross_state_notification", notification))
		shouldRunRoot = true
	}
	a.mu.Lock()
	a.replyTarget = replyTarget
	a.mu.Unlock()
	for _, msg := range storyBatch {
		select {
		case a.storyQueue <- msg:
		default:
			a.store.Log("warn", "Story queue full; message dropped", map[string]any{"event": "agent.story.queue_full"})
		}
	}
	a.mu.Lock()
	a.replyTarget = nil
	a.loopState = "idle"
	a.lastActivity = new(time.Now())
	a.mu.Unlock()
	a.persistSnapshot()
	return shouldRunRoot
}

func (a *AgentRuntime) appendWakeReminderIfNeeded() {
	now := time.Now()
	a.mu.Lock()
	if a.lastWakeReminderAt != nil && sameBeijingMinute(*a.lastWakeReminderAt, now) {
		a.mu.Unlock()
		return
	}
	a.lastWakeReminderAt = &now
	a.mu.Unlock()
	reminder := prompts.WakeReminder(now)
	a.appendRootContext(RootContextLayerSystem, agentruntime.Message{Role: "user", Content: reminder})
	a.appendContext(contextItem("system_reminder", "wake", reminder))
}

func (a *AgentRuntime) notificationBatchWindow() time.Duration {
	ms := 30000
	if a.cfg != nil && a.cfg.Server.Agent.NotificationBatchWindowMs > 0 {
		ms = a.cfg.Server.Agent.NotificationBatchWindowMs
	}
	return time.Duration(ms) * time.Millisecond
}

func (a *AgentRuntime) eventShouldTriggerRoot(event AgentEvent) bool {
	switch event.Type {
	case "wake":
		switch common.AsString(event.Data["reason"]) {
		case "private_unread", "group_unread", "portal_unread":
			return true
		default:
			return false
		}
	case "napcat_group_message":
		if a.shouldThrottleGroupMessage(event) {
			return false
		}
	}
	return true
}

func (a *AgentRuntime) shouldThrottleGroupMessage(event AgentEvent) bool {
	if event.Type != "napcat_group_message" || a.isPriorityMessage(event) {
		return false
	}
	target := targetFromEvent(event)
	if target.Type != "group" || strings.TrimSpace(target.ID) == "" {
		return false
	}
	cooldown := a.groupReplyCooldown()
	if cooldown <= 0 {
		return false
	}
	a.mu.Lock()
	last := a.lastSentAtByTarget[targetKey(target)]
	a.mu.Unlock()
	if last.IsZero() || time.Since(last) >= cooldown {
		return false
	}
	log.Printf("[AGENT] throttle group root trigger target=%s cooldown=%s remaining=%s", target.ID, cooldown, cooldown-time.Since(last))
	return true
}

func (a *AgentRuntime) isPriorityMessage(event AgentEvent) bool {
	if event.Type == "napcat_private_message" {
		return true
	}
	userID := strings.TrimSpace(common.AsString(event.Data["userId"]))
	if a.cfg != nil && userID != "" && userID == strings.TrimSpace(a.cfg.Server.Bot.Creator.QQ) {
		return true
	}
	raw := common.AsString(event.Data["rawMessage"])
	if strings.Contains(raw, "帕秋莉") {
		return true
	}
	if a.cfg != nil {
		botQQ := strings.TrimSpace(a.cfg.Server.Bot.QQ)
		if botQQ != "" && (strings.Contains(raw, botQQ) || strings.Contains(raw, "@"+botQQ)) {
			return true
		}
	}
	return false
}

func (a *AgentRuntime) groupReplyCooldown() time.Duration {
	return 20 * time.Second
}

func (a *AgentRuntime) runRootRound(ctx context.Context) bool {
	if a.llm == nil || a.rootContextLen() == 0 {
		return false
	}
	a.mu.Lock()
	a.loopState = "calling_root_llm"
	a.mu.Unlock()
	a.sanitizeRootContext()
	a.compactRootContextIfNeeded()
	a.injectStoryRecallIfNeeded()
	a.hooks.OnBeforeRootRound(ctx, a)
	tools := a.rootControlTools()
	messages := a.rootPromptMessages()
	log.Printf("[AGENT] root round=1 messages=%d tools=%d", len(messages), len(tools.Definitions()))
	result, err := a.rootKernel.RunRound(ctx, agentruntime.RoundInput{
		SystemPrompt: createSystemPrompt(a.cfg),
		Messages:     messages,
		Tools:        tools,
		ToolChoice:   "required",
	})
	if err != nil {
		if retryResult, retryErr := a.retryRootRoundOnce(ctx, tools); retryErr == nil {
			result = retryResult
			err = nil
		} else {
			a.hooks.OnAfterRootRound(ctx, a, agentruntime.RoundResult{}, retryErr)
			log.Printf("[AGENT] root round=1 error=%v", retryErr)
			a.setRuntimeError(retryErr)
			return false
		}
	}
	a.hooks.OnAfterRootRound(ctx, a, result, err)
	shouldPause := false
	log.Printf("[AGENT] root round=1 assistant=%q toolCalls=%s", trimPreview(result.Assistant.Content, 500), runtimeToolCallNames(result.Assistant.ToolCalls))
	if os := strings.TrimSpace(result.Completion.OS); os != "" {
		log.Printf("[AGENT] root os=%q", trimPreview(os, 500))
	}
	if assistant := rootAssistantToPersist(result.Assistant, tools); shouldPersistRootAssistant(assistant) {
		a.appendRootContext(RootContextLayerAssistant, assistant)
		a.appendContext(contextItem("llm_message", "assistant", assistant.Content))
	} else if strings.TrimSpace(result.Assistant.Content) != "" {
		log.Printf("[AGENT] drop assistant text without persisted root turn content=%q", trimPreview(result.Assistant.Content, 300))
	}
	for _, execution := range result.ToolExecutions {
		log.Printf("[AGENT] tool name=%s args=%s result=%q", execution.Call.Name, mustCompactJSON(execution.Call.Arguments), trimPreview(execution.Result.Content, 500))
		if isTerminalConversationAction(execution.Call) {
			shouldPause = true
		}
		effect := stateEffectFromToolResult(execution.Call.Name, execution.Result.Content)
		if strings.TrimSpace(effect.VisibleContext) != "" {
			layer := RootContextLayerToolEffect
			if execution.Call.Name == "enter" || execution.Call.Name == "back" {
				layer = RootContextLayerWorkingSet
			}
			a.appendRootContext(layer, agentruntime.Message{Role: "user", Content: effect.VisibleContext})
			a.appendContext(contextItem("system_reminder", execution.Call.Name, effect.VisibleContext))
		}
		if sentMessage := sentMessageContextMessage(execution); sentMessage.Content != "" {
			a.appendRootContext(RootContextLayerAssistant, sentMessage)
			a.appendContext(contextItem("assistant_sent_message", "send_message", sentMessage.Content))
			a.recordSentMessage(execution)
		}
		if shouldPersistRootToolResult(execution.Call.Name, execution.Result, tools) {
			a.appendRootContext(RootContextLayerToolEffect, agentruntime.Message{Role: "tool", ToolCallID: execution.Call.ID, Content: execution.Result.Content})
		}
		a.recordToolExecution(execution)
	}
	a.recordLLMCall(result.Completion)
	a.persistSnapshot()
	a.hooks.OnAfterRootCommit(ctx, a, result)
	return shouldPause
}

func (a *AgentRuntime) waitForNextEvent(ctx context.Context) {
	log.Printf("[AGENT] terminal conversation action completed; waiting for next event")
	a.events.WaitForEvent(ctx)
}

func (a *AgentRuntime) retryRootRoundOnce(ctx context.Context, tools *agentruntime.ToolCatalog) (agentruntime.RoundResult, error) {
	delay := time.Duration(a.cfg.Server.Agent.LLMRetryBackoffMs) * time.Millisecond
	if delay <= 0 {
		delay = 3 * time.Second
	}
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	log.Printf("[AGENT] root llm retry scheduled delay=%s", delay)
	timer := time.NewTimer(delay)
	select {
	case <-ctx.Done():
		timer.Stop()
		return agentruntime.RoundResult{}, ctx.Err()
	case <-timer.C:
	}
	return a.rootKernel.RunRound(ctx, agentruntime.RoundInput{
		SystemPrompt: createSystemPrompt(a.cfg),
		Messages:     a.rootPromptMessages(),
		Tools:        tools,
		ToolChoice:   "required",
	})
}
