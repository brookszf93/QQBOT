package agent

import (
	"context"
	"encoding/json"
	"qqbot-ai/internal/agentruntime"
	"qqbot-ai/internal/capabilities/messaging"
	"qqbot-ai/internal/config"
	"testing"
	"time"
)

type recordingSender struct {
	groupID string
	userID  string
	message string
}

func (s *recordingSender) SendGroupMessage(groupID, message string) (int, error) {
	s.groupID = groupID
	s.message = message
	return 42, nil
}

func (s *recordingSender) SendPrivateMessage(userID, message string) (int, error) {
	s.userID = userID
	s.message = message
	return 43, nil
}

func TestFacadeWaitToolBlocksUntilEventWithoutConsumingIt(t *testing.T) {
	queue := NewEventQueue()
	tool := facadeWaitTool{Queue: queue, MaxWait: time.Second}

	done := make(chan agentruntime.ToolResult, 1)
	go func() {
		result, _ := tool.Execute(context.Background(), agentruntime.ToolCall{Name: "wait"})
		done <- result
	}()

	select {
	case <-done:
		t.Fatal("wait returned before an event arrived")
	case <-time.After(20 * time.Millisecond):
	}

	event := AgentEvent{Type: "napcat_group_message", Data: map[string]any{"groupId": "1"}}
	queue.Enqueue(event)

	select {
	case result := <-done:
		if result.Content != waitResumeContent("event") {
			t.Fatalf("unexpected wait result: %q", result.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not return after event arrived")
	}

	events := queue.DequeueAll()
	if len(events) != 1 || events[0].Type != event.Type {
		t.Fatalf("wait should not consume the event, got %#v", events)
	}
}

func TestFacadeWaitToolTimeoutEnqueuesWake(t *testing.T) {
	queue := NewEventQueue()
	tool := facadeWaitTool{Queue: queue, MaxWait: 15 * time.Millisecond}

	result, err := tool.Execute(context.Background(), agentruntime.ToolCall{Name: "wait"})
	if err != nil {
		t.Fatalf("wait returned error: %v", err)
	}
	if result.Content != waitResumeContent("timeout") {
		t.Fatalf("unexpected wait result: %q", result.Content)
	}

	events := queue.DequeueAll()
	if len(events) != 1 || events[0].Type != "wake" {
		t.Fatalf("expected timeout wake event, got %#v", events)
	}
}

func TestFacadeInvokeSendMessageKeepsToolResultMinimal(t *testing.T) {
	sender := &recordingSender{}
	tool := facadeInvokeTool{
		Tools: agentruntime.NewToolCatalog(messaging.SendMessageTool{Sender: sender}),
	}

	result, err := tool.Execute(context.Background(), agentruntime.ToolCall{
		ID:   "call_send",
		Name: "invoke",
		Arguments: map[string]any{
			"tool": "send_message",
			"arguments": map[string]any{
				"targetType": "group",
				"targetId":   "253631878",
				"message":    "刚刚点评过了",
			},
		},
	})
	if err != nil {
		t.Fatalf("send_message returned error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("invalid result json: %v", err)
	}
	if payload["messageId"] == nil {
		t.Fatalf("expected messageId result, got %s", result.Content)
	}
	if _, ok := payload["message"]; ok {
		t.Fatalf("send_message result should not echo message text: %#v", payload)
	}
	if _, ok := payload["targetId"]; ok {
		t.Fatalf("send_message result should not echo target metadata: %#v", payload)
	}
}

func TestSentMessageContextMessageUsesAssistantRoleWithOwnUtterance(t *testing.T) {
	message := sentMessageContextMessage(agentruntime.ToolExecution{
		Call: agentruntime.ToolCall{
			Name: "invoke",
			Arguments: map[string]any{
				"tool": "send_message",
				"arguments": map[string]any{
					"targetType": "group",
					"targetId":   "253631878",
					"message":    "上一句自己的话",
				},
			},
		},
		Result: agentruntime.ToolResult{Content: `{"ok":true,"tool":"send_message","messageId":42,"message":"上一句自己的话","targetType":"group","targetId":"253631878"}`},
	})
	if message.Role != "assistant" || message.Content != "上一句自己的话" {
		t.Fatalf("own utterance should be stored as assistant message, got %#v", message)
	}
}

func TestGroupMessageThrottledAfterRecentSend(t *testing.T) {
	runtime := &AgentRuntime{
		cfg: &config.Config{},
		lastSentAtByTarget: map[string]time.Time{
			"group:253631878": time.Now(),
		},
	}
	runtime.cfg.Server.Bot.QQ = "180920020"
	runtime.cfg.Server.Bot.Creator.QQ = "461105039"

	event := AgentEvent{
		Type: "napcat_group_message",
		Data: map[string]any{
			"groupId":    "253631878",
			"userId":     "1655827800",
			"rawMessage": "普通接梗消息",
		},
	}
	if runtime.eventShouldTriggerRoot(event) {
		t.Fatal("ordinary group message should not trigger root immediately after a recent group send")
	}
}

func TestGroupMentionBypassesReplyCooldown(t *testing.T) {
	runtime := &AgentRuntime{
		cfg: &config.Config{},
		lastSentAtByTarget: map[string]time.Time{
			"group:253631878": time.Now(),
		},
	}
	runtime.cfg.Server.Bot.QQ = "180920020"
	runtime.cfg.Server.Bot.Creator.QQ = "461105039"

	event := AgentEvent{
		Type: "napcat_group_message",
		Data: map[string]any{
			"groupId":    "253631878",
			"userId":     "1655827800",
			"rawMessage": "帕秋莉你怎么看",
		},
	}
	if !runtime.eventShouldTriggerRoot(event) {
		t.Fatal("directly mentioned group message should bypass reply cooldown")
	}
}

func TestCreatorMessageBypassesReplyCooldown(t *testing.T) {
	runtime := &AgentRuntime{
		cfg: &config.Config{},
		lastSentAtByTarget: map[string]time.Time{
			"group:253631878": time.Now(),
		},
	}
	runtime.cfg.Server.Bot.Creator.QQ = "461105039"

	event := AgentEvent{
		Type: "napcat_group_message",
		Data: map[string]any{
			"groupId":    "253631878",
			"userId":     "461105039",
			"rawMessage": "回复一下",
		},
	}
	if !runtime.eventShouldTriggerRoot(event) {
		t.Fatal("creator message should bypass reply cooldown")
	}
}

func TestWaitTimeoutWakeDoesNotTriggerRoot(t *testing.T) {
	runtime := &AgentRuntime{}
	event := AgentEvent{Type: "wake", Data: map[string]any{"reason": "wait_timeout"}}
	if runtime.eventShouldTriggerRoot(event) {
		t.Fatal("plain wait timeout wake should not trigger a root round by itself")
	}
}

func TestEventQueueWaitIgnoresStaleWakeupSignal(t *testing.T) {
	queue := NewEventQueue()
	queue.Enqueue(AgentEvent{Type: "old_event"})
	if events := queue.DequeueAll(); len(events) != 1 {
		t.Fatalf("expected setup event to be drained, got %#v", events)
	}

	done := make(chan []AgentEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		events, ok := queue.Wait(ctx)
		if ok {
			done <- events
		}
	}()

	select {
	case events := <-done:
		t.Fatalf("wait returned on stale wakeup signal: %#v", events)
	case <-time.After(20 * time.Millisecond):
	}

	queue.Enqueue(AgentEvent{Type: "new_event"})
	select {
	case events := <-done:
		if len(events) != 1 || events[0].Type != "new_event" {
			t.Fatalf("unexpected events after real wakeup: %#v", events)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not return after a real event arrived")
	}
}

func TestEventQueueWaitForEventIgnoresStaleWakeupSignal(t *testing.T) {
	queue := NewEventQueue()
	queue.Enqueue(AgentEvent{Type: "old_event"})
	if events := queue.DequeueAll(); len(events) != 1 {
		t.Fatalf("expected setup event to be drained, got %#v", events)
	}

	done := make(chan bool, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		done <- queue.WaitForEvent(ctx)
	}()

	select {
	case ok := <-done:
		t.Fatalf("waitForEvent returned on stale wakeup signal: %v", ok)
	case <-time.After(20 * time.Millisecond):
	}

	queue.Enqueue(AgentEvent{Type: "new_event"})
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("waitForEvent returned false after a real event arrived")
		}
	case <-time.After(time.Second):
		t.Fatal("waitForEvent did not return after a real event arrived")
	}
}

func TestSanitizeKeepsOnlyRecentWaitToolTurns(t *testing.T) {
	messages := []agentruntime.Message{}
	for i := 0; i < 5; i++ {
		messages = append(messages,
			agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "call_wait", Name: "wait"}}},
			agentruntime.Message{Role: "tool", ToolCallID: "call_wait", Content: waitResumeContent("event")},
		)
	}
	messages = append(messages, agentruntime.Message{Role: "user", Content: "visible"})

	got := sanitizeRootMessages(messages)
	waitAssistants := 0
	waitResults := 0
	for _, msg := range got {
		if isWaitAssistantMessage(msg) {
			waitAssistants++
		}
		if msg.Role == "tool" && isWaitResumeContent(msg.Content) {
			waitResults++
		}
	}
	if waitAssistants != 3 || waitResults != 3 {
		t.Fatalf("expected 3 wait turns, got assistant=%d tool=%d messages=%#v", waitAssistants, waitResults, got)
	}
	if got[len(got)-1].Content != "visible" {
		t.Fatalf("visible message was not preserved: %#v", got)
	}
}

func TestSanitizeDropsOrphanToolResults(t *testing.T) {
	got := sanitizeRootMessages([]agentruntime.Message{
		{Role: "tool", ToolCallID: "missing", Content: "orphan"},
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "call_ok", Name: "wait"}}},
		{Role: "tool", ToolCallID: "call_ok", Content: waitResumeContent("event")},
	})
	if len(got) != 2 {
		t.Fatalf("expected orphan tool result to be dropped, got %#v", got)
	}
	if got[1].Role != "tool" || got[1].ToolCallID != "call_ok" {
		t.Fatalf("paired tool result was not preserved: %#v", got)
	}
}

func TestSanitizeDropsAssistantToolCallsWithoutOutputs(t *testing.T) {
	got := sanitizeRootMessages([]agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "call_missing", Name: "wait"}}},
		{Role: "user", Content: "next message"},
	})
	if len(got) != 1 {
		t.Fatalf("expected assistant with missing tool output to be dropped, got %#v", got)
	}
	if got[0].Role != "user" || got[0].Content != "next message" {
		t.Fatalf("unexpected remaining messages: %#v", got)
	}
}

func TestSanitizeFiltersOnlyMissingAssistantToolCalls(t *testing.T) {
	got := sanitizeRootMessages([]agentruntime.Message{
		{Role: "assistant", Content: "kept", ToolCalls: []agentruntime.ToolCall{
			{ID: "call_missing", Name: "wait"},
			{ID: "call_ok", Name: "wait"},
		}},
		{Role: "tool", ToolCallID: "call_ok", Content: waitResumeContent("event")},
	})
	if len(got) != 2 {
		t.Fatalf("expected paired assistant/tool messages, got %#v", got)
	}
	if len(got[0].ToolCalls) != 1 || got[0].ToolCalls[0].ID != "call_ok" {
		t.Fatalf("assistant tool calls were not filtered: %#v", got[0].ToolCalls)
	}
}

func TestTerminalConversationActionWaitsForNextEvent(t *testing.T) {
	runtime := &AgentRuntime{events: NewEventQueue()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runtime.waitForNextEvent(ctx)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("waitForNextEvent returned before an event arrived")
	case <-time.After(20 * time.Millisecond):
	}
	runtime.events.Enqueue(AgentEvent{Type: "napcat_group_message", Data: map[string]any{"groupId": "1"}})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("waitForNextEvent did not return after event arrived")
	}
	events := runtime.events.DequeueAll()
	if len(events) != 1 || events[0].Type != "napcat_group_message" {
		t.Fatalf("waitForNextEvent should not consume events, got %#v", events)
	}
}
