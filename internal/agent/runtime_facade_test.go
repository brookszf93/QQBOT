package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	roottools "QqBot/internal/agent/root"
	"QqBot/internal/agentruntime"
	"QqBot/internal/config"
	"QqBot/internal/db"
)

func TestStoryBatchScheduleDecision(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.Local)
	idleFlush := 2 * time.Minute

	shouldRun, retryAfter := storyBatchScheduleDecision(1, 24, now.Add(-30*time.Second), true, now, idleFlush)
	if shouldRun || retryAfter != 90*time.Second {
		t.Fatalf("single recent message should wait for idle flush: run=%v retry=%v", shouldRun, retryAfter)
	}

	shouldRun, retryAfter = storyBatchScheduleDecision(24, 24, now, true, now, idleFlush)
	if !shouldRun || retryAfter != 0 {
		t.Fatalf("full batch should run immediately: run=%v retry=%v", shouldRun, retryAfter)
	}

	shouldRun, retryAfter = storyBatchScheduleDecision(3, 24, now.Add(-idleFlush), true, now, idleFlush)
	if !shouldRun || retryAfter != 0 {
		t.Fatalf("idle partial batch should run: run=%v retry=%v", shouldRun, retryAfter)
	}

	shouldRun, retryAfter = storyBatchScheduleDecision(0, 24, time.Time{}, false, now, idleFlush)
	if shouldRun || retryAfter != 0 {
		t.Fatalf("empty ledger should remain idle: run=%v retry=%v", shouldRun, retryAfter)
	}
}

func TestRenderStoryLedgerBatchIncludesCompleteLinearContext(t *testing.T) {
	rendered := renderStoryLedgerBatch([]db.StoryLedgerItem{
		{Seq: 10, Role: "user", Content: "<qq_message>绗竴鏉?/qq_message>"},
		{Seq: 11, Role: "user", Content: "<qq_message>绗簩鏉?/qq_message>"},
	})

	for _, expected := range []string{
		"<ledger_batch>",
		"[10] user\n<qq_message>绗竴鏉?/qq_message>",
		"[11] user\n<qq_message>绗簩鏉?/qq_message>",
		"</ledger_batch>",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered batch missing %q:\n%s", expected, rendered)
		}
	}
}

func TestWaitTimeoutWakeTriggersRootRound(t *testing.T) {
	if wakeTriggersRootRound("wait_timeout") {
		t.Fatal("wait timeout must not trigger an LLM round unless cache keepalive is explicitly enabled")
	}
	if !wakeTriggersRootRound("continue_after_tool") {
		t.Fatal("tool continuation wake should trigger an LLM round")
	}
	if !wakeTriggersRootRound("self_continuation") {
		t.Fatal("self continuation wake should trigger an LLM round")
	}
	if wakeTriggersRootRound("") {
		t.Fatal("unclassified wake should remain silent")
	}
}

func TestWaitExecutionIsNotPersistedInModelContext(t *testing.T) {
	message := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "wait-1", Name: "wait"}}}
	executions := []agentruntime.ToolExecution{{
		Call:   message.ToolCalls[0],
		Result: agentruntime.ToolResult{Kind: "control", Content: "wait completed"},
	}}
	if shouldPersistAssistant(message, executions) {
		t.Fatal("wait assistant call must not enter persistent model context")
	}
	if shouldPersistToolResult(executions[0]) {
		t.Fatal("wait tool output must not enter persistent model context")
	}
}

func TestUnavailableToolExecutionIsNotPersistedInModelContext(t *testing.T) {
	execution := agentruntime.ToolExecution{
		Call:   agentruntime.ToolCall{ID: "call-1", Name: "act", Arguments: map[string]any{"action": "novel_app"}},
		Result: agentruntime.ToolResult{Kind: "business", Content: `{"ok":false,"error":"INVOKE_TOOL_NOT_AVAILABLE"}`},
	}
	if shouldPersistToolResult(execution) {
		t.Fatal("unavailable tool errors should not teach the model a stale capability map")
	}
}

func TestSuccessfulActEnterExecutionIsPersistedInModelContext(t *testing.T) {
	execution := agentruntime.ToolExecution{
		Call:   agentruntime.ToolCall{ID: "call-1", Name: "act", Arguments: map[string]any{"action": "enter", "query": "novel"}},
		Result: agentruntime.ToolResult{Kind: "control", Content: `{"ok":true,"enteredApp":"novel","screenResult":"{\"ok\":true}"}`},
	}
	if !shouldPersistToolResult(execution) {
		t.Fatal("successful enter should be visible to the next model round")
	}
}

func TestRootToolsExposeConcreteRootTools(t *testing.T) {
	tools := rootTools(&config.Config{}, agentruntime.NewToolCatalog(sendMessageTool{}), roottools.NewSession(nil), NewEventQueue())
	definitions := tools.Definitions()
	names := definitionNamesForTest(definitions)
	want := []string{"enter", "back_to_portal", "help", "wait", "send_message"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("root tools should expose concrete tools, got %#v", names)
	}
}

func TestAutonomousRoundDecisionLimitsBurstAndRestartsAfterCooldown(t *testing.T) {
	rounds := 0
	for expected := 1; expected <= 4; expected++ {
		allowed, next := autonomousRoundDecision(rounds, 4, false)
		if !allowed || next != expected {
			t.Fatalf("round %d should be allowed, got allowed=%v next=%d", expected, allowed, next)
		}
		rounds = next
	}
	if allowed, next := autonomousRoundDecision(rounds, 4, false); allowed || next != 4 {
		t.Fatalf("fifth consecutive round should enter cooldown: allowed=%v next=%d", allowed, next)
	}
	if allowed, next := autonomousRoundDecision(rounds, 4, true); !allowed || next != 1 {
		t.Fatalf("round after cooldown should restart burst: allowed=%v next=%d", allowed, next)
	}
}

func TestExternalEventSuppressesSameBatchSelfContinuation(t *testing.T) {
	events := []AgentEvent{
		{Type: "napcat_group_message"},
		{Type: "wake", Data: map[string]any{"reason": "self_continuation"}},
	}
	if !hasExternalAgentEvent(events) {
		t.Fatal("real QQ event must take priority over self continuation")
	}
	if hasExternalAgentEvent([]AgentEvent{{Type: "wake", Data: map[string]any{"reason": "self_continuation"}}}) {
		t.Fatal("wake-only batch should remain autonomous")
	}
}

func TestSelfContinuationReminderIsEphemeral(t *testing.T) {
	runtime := &AgentRuntime{
		rootMessages:      []agentruntime.Message{{Role: "user", Content: "real context"}},
		autonomousPending: true,
	}
	messages, autonomous := runtime.rootRoundMessages()
	if !autonomous || len(messages) != 2 || !strings.Contains(messages[1].Content, "rhythm_signal") {
		t.Fatalf("expected one ephemeral reminder: %#v", messages)
	}
	if len(runtime.rootMessages) != 1 {
		t.Fatalf("self continuation must not enter persisted root history: %#v", runtime.rootMessages)
	}
	next, autonomous := runtime.rootRoundMessages()
	if autonomous || len(next) != 1 {
		t.Fatalf("ephemeral reminder should be consumed once: %#v", next)
	}
}

func TestAutonomousIdleWakeRequiresRealIdleRuntime(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.Local)
	lastActivity := now.Add(-11 * time.Minute)
	runtime := &AgentRuntime{
		cfg: &config.Config{Server: config.ServerConfig{Agent: config.AgentConfig{
			Autonomous: config.AutonomousConfig{Enabled: true, IdleDelayMs: int((10 * time.Minute).Milliseconds())},
		}}},
		events:       NewEventQueue(),
		initialized:  true,
		loopState:    "idle",
		lastActivity: &lastActivity,
	}
	if !runtime.shouldQueueAutonomousIdleWake(now, 10*time.Minute) {
		t.Fatal("idle initialized runtime should queue autonomous wake")
	}

	runtime.events.Enqueue(AgentEvent{Type: "napcat_private_message"})
	if runtime.shouldQueueAutonomousIdleWake(now, 10*time.Minute) {
		t.Fatal("pending external events must suppress autonomous wake")
	}
	runtime.events.DequeueAll()

	runtime.autonomousPending = true
	if runtime.shouldQueueAutonomousIdleWake(now, 10*time.Minute) {
		t.Fatal("existing autonomous pending wake must not be duplicated")
	}
	runtime.autonomousPending = false

	runtime.loopState = "calling_root_llm"
	if runtime.shouldQueueAutonomousIdleWake(now, 10*time.Minute) {
		t.Fatal("busy runtime must not queue autonomous wake")
	}
}

func TestAutonomousIdleWatchIntervalIsBounded(t *testing.T) {
	if got := autonomousIdleWatchInterval(time.Second); got != 5*time.Second {
		t.Fatalf("short idle delay should use minimum interval, got %s", got)
	}
	if got := autonomousIdleWatchInterval(time.Hour); got != time.Minute {
		t.Fatalf("long idle delay should use maximum interval, got %s", got)
	}
	if got := autonomousIdleWatchInterval(40 * time.Second); got != 10*time.Second {
		t.Fatalf("normal idle delay should use quarter interval, got %s", got)
	}
}

func TestRootToolSchemaStaysStableAcrossAppTransitions(t *testing.T) {
	cfg := &config.Config{}
	business := agentruntime.NewToolCatalog(
		sendMessageTool{},
		calculateTool{},
	)
	session := roottools.NewSession([]string{"1001"})
	events := NewEventQueue()

	before, err := json.Marshal(rootTools(cfg, business, session, events).Definitions())
	if err != nil {
		t.Fatal(err)
	}
	if result := session.EnterApp("calc"); result["ok"] != true {
		t.Fatalf("failed to enter calc: %#v", result)
	}
	after, err := json.Marshal(rootTools(cfg, business, session, events).Definitions())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("root tool schema changed across state transition:\nbefore=%s\nafter=%s", before, after)
	}
}

type runtimeFacadeTestTool struct {
	name string
}

func (t runtimeFacadeTestTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: t.name, Description: "test tool", Parameters: agentruntime.ObjectSchema(map[string]any{})}
}

func (runtimeFacadeTestTool) Kind() string { return "business" }

func (runtimeFacadeTestTool) Execute(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	return agentruntime.ToolResult{Kind: "business", Content: `{"ok":true}`}, nil
}

func TestDetectAIToneIsAlwaysAvailable(t *testing.T) {
	cfg := &config.Config{}
	business := agentruntime.NewToolCatalog(runtimeFacadeTestTool{name: "detect_ai_tone"})
	session := roottools.NewSession([]string{"1001"})
	events := NewEventQueue()

	result, err := rootTools(cfg, business, session, events).Execute(context.Background(), agentruntime.ToolCall{
		ID:        "detect-1",
		Name:      "detect_ai_tone",
		Arguments: map[string]any{"text": "娴嬭瘯"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, "INVOKE_TOOL_NOT_AVAILABLE") {
		t.Fatalf("detect_ai_tone should be available in main state: %s", result.Content)
	}
	if !strings.Contains(result.Content, `"ok":true`) {
		t.Fatalf("unexpected result: %s", result.Content)
	}
}

func TestToolSideEffectClassification(t *testing.T) {
	if !toolCallHasSideEffect(agentruntime.ToolCall{Name: "invoke", Arguments: map[string]any{"tool": "send_message"}}) {
		t.Fatal("send_message must be protected as side-effecting")
	}
	if !toolCallHasSideEffect(agentruntime.ToolCall{Name: "bash"}) {
		t.Fatal("bash must be protected as side-effecting")
	}
	if !toolCallHasSideEffect(agentruntime.ToolCall{Name: "act", Arguments: map[string]any{"action": "novel_app", "action_text": "create_project"}}) {
		t.Fatal("novel project creation must be protected as side-effecting")
	}
	if toolCallHasSideEffect(agentruntime.ToolCall{Name: "act", Arguments: map[string]any{"action": "novel_app", "action_text": "screen"}}) {
		t.Fatal("novel screen should remain read-only")
	}
	if toolCallHasSideEffect(agentruntime.ToolCall{Name: "search_web"}) {
		t.Fatal("read-only web search should be replayable")
	}
}

func TestShouldStopAfterSuccessfulSendMessage(t *testing.T) {
	executions := []agentruntime.ToolExecution{{
		Call: agentruntime.ToolCall{
			Name: "invoke",
			Arguments: map[string]any{
				"tool":      "send_message",
				"arguments": map[string]any{"message": "next"},
			},
		},
		Result: agentruntime.ToolResult{Kind: "business", Content: `{"messageId":1}`},
	}}
	if shouldContinueAfterTool(executions) {
		t.Fatal("successful send_message must end the round until a new external event arrives")
	}
}

func TestShouldStopAfterPersonalAppWrite(t *testing.T) {
	executions := []agentruntime.ToolExecution{{
		Call: agentruntime.ToolCall{
			Name:      "novel_app",
			Arguments: map[string]any{"action_text": "create_project", "title": "闅忕瑪"},
		},
		Result: agentruntime.ToolResult{Kind: "business", Content: `{"ok":true,"project":{"id":"novel-1"}}`},
	}}
	if shouldContinueAfterTool(executions) {
		t.Fatal("successful personal app write must end the round until a new external event arrives")
	}
}

func TestShouldContinueAfterFailedSendMessage(t *testing.T) {
	executions := []agentruntime.ToolExecution{{
		Call:   agentruntime.ToolCall{Name: "send_message"},
		Result: agentruntime.ToolResult{Kind: "business", Content: `{"ok":false,"error":"NapCat disconnected"}`},
	}}
	if !shouldContinueAfterTool(executions) {
		t.Fatal("failed send_message should continue so the model can recover")
	}
}

func TestShouldStopAfterAIToneBlockedSendMessage(t *testing.T) {
	executions := []agentruntime.ToolExecution{{
		Call:   agentruntime.ToolCall{Name: "send_message"},
		Result: agentruntime.ToolResult{Kind: "business", Content: `{"ok":false,"error":"AI_TONE_TOO_HIGH","prob":0.8}`},
	}}
	if shouldContinueAfterTool(executions) {
		t.Fatal("AI tone blocked send_message must stop instead of asking the model to report/retry")
	}
	if shouldPersistToolResult(executions[0]) {
		t.Fatal("AI tone blocked result should stay out of chat context")
	}
	assistant := assistantForPersistence(agentruntime.Message{
		Role:      "assistant",
		ToolCalls: []agentruntime.ToolCall{{ID: executions[0].Call.ID, Name: "send_message"}},
	}, executions)
	if len(assistant.ToolCalls) != 0 {
		t.Fatalf("AI tone blocked assistant tool call should stay out of chat context: %#v", assistant)
	}
}

func TestShouldStopAfterWait(t *testing.T) {
	executions := []agentruntime.ToolExecution{{
		Call:   agentruntime.ToolCall{Name: "wait"},
		Result: agentruntime.ToolResult{Kind: "control", Content: "wait completed"},
	}}
	if shouldContinueAfterTool(executions) {
		t.Fatal("wait must suspend the autonomous loop")
	}
}

func TestShouldStopAfterActWait(t *testing.T) {
	executions := []agentruntime.ToolExecution{{
		Call:   agentruntime.ToolCall{Name: "act", Arguments: map[string]any{"action": "wait"}},
		Result: agentruntime.ToolResult{Kind: "control", Content: "wait completed"},
	}}
	if shouldContinueAfterTool(executions) {
		t.Fatal("act(action=wait) must suspend the autonomous loop")
	}
}

func TestPlainWaitAssistantIsNotPersisted(t *testing.T) {
	if !isPlainWaitContent("wait") || !isPlainWaitContent(" wait.") {
		t.Fatal("plain wait text should be treated as an idle action")
	}
	if shouldPersistAssistant(agentruntime.Message{Role: "assistant", Content: "wait"}, nil) {
		t.Fatal("plain wait assistant content should not pollute chat history")
	}
	if shouldPersistAssistant(agentruntime.Message{Role: "assistant", Content: "I will wait a bit"}, nil) {
		t.Fatal("plain assistant content without a tool call was not sent and must not pollute chat history")
	}
}

func TestShouldContinueAfterToolFailure(t *testing.T) {
	executions := []agentruntime.ToolExecution{{
		Call:   agentruntime.ToolCall{Name: "search_web"},
		Result: agentruntime.ToolResult{Kind: "business", Content: `{"ok":false,"error":"temporary failure"}`},
	}}
	if !shouldContinueAfterTool(executions) {
		t.Fatal("a non-wait tool failure must continue so the model can inspect the error and recover")
	}
}

func TestShouldStopAfterUnknownToolFailure(t *testing.T) {
	executions := []agentruntime.ToolExecution{{
		Call:   agentruntime.ToolCall{Name: "send_message"},
		Result: agentruntime.ToolResult{Kind: "control", Content: `{"ok":false,"error":"UNKNOWN_TOOL"}`},
	}}
	if shouldContinueAfterTool(executions) {
		t.Fatal("permanent unknown-tool failures must not create an autonomous retry loop")
	}
}

func definitionNamesForTest(definitions []agentruntime.ToolDefinition) []string {
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	return names
}

func TestLatestStoryRecallQueryUsesNewestQQMessage(t *testing.T) {
	messages := []agentruntime.Message{
		{Role: "user", Content: `<qq_message target_type="group" target_id="1001">alice (1): old topic</qq_message>`},
		{Role: "user", Content: "<system_reminder>褰撳墠鏃堕棿</system_reminder>"},
		{Role: "user", Content: `<qq_message target_type="private" target_id="2">
bob (2):
new topic
</qq_message>`},
	}
	if query := latestStoryRecallQuery(messages); query != "bob (2): new topic" {
		t.Fatalf("unexpected recall query: %q", query)
	}
}
