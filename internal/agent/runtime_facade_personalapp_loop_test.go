package agent

import (
	"testing"

	"QqBot/internal/agentruntime"
)

func TestPersonalAppReadMayContinueOnceThenStopsOnRepeat(t *testing.T) {
	executions := []agentruntime.ToolExecution{{
		Call:   agentruntime.ToolCall{Name: "novel_app", Arguments: map[string]any{"action_text": "screen"}},
		Result: agentruntime.ToolResult{Kind: "business", Content: `{"ok":true,"screen":{"app":"novel","state":{}}}`},
	}}
	runtime := &AgentRuntime{}
	if !runtime.shouldContinueAfterTool(executions) {
		t.Fatal("first personal app read should allow one follow-up action")
	}
	if runtime.shouldContinueAfterTool(executions) {
		t.Fatal("repeated identical personal app read must stop")
	}
}

func TestShouldStopAfterPersonalAppFailure(t *testing.T) {
	executions := []agentruntime.ToolExecution{{
		Call:   agentruntime.ToolCall{Name: "novel_app", Arguments: map[string]any{"action_text": "open_project", "projectId": "novel-missing"}},
		Result: agentruntime.ToolResult{Kind: "business", Content: `{"ok":false,"error":"PERSONAL_APP_FAILED","message":"project novel-missing not found"}`},
	}}
	if shouldContinueAfterTool(executions) {
		t.Fatal("personal app failures should stop instead of creating autonomous retry loops")
	}
}

func TestAssistantToolCallContentDoesNotPersist(t *testing.T) {
	assistant := assistantForPersistence(agentruntime.Message{
		Role:      "assistant",
		Content:   "I will inspect the project first.",
		ToolCalls: []agentruntime.ToolCall{{ID: "call-1", Name: "novel_app", Arguments: map[string]any{"action_text": "screen"}}},
	}, []agentruntime.ToolExecution{{
		Call:   agentruntime.ToolCall{ID: "call-1", Name: "novel_app", Arguments: map[string]any{"action_text": "screen"}},
		Result: agentruntime.ToolResult{Kind: "business", Content: `{"ok":true}`},
	}})
	if assistant.Content != "" {
		t.Fatalf("assistant narration beside tool calls should not persist: %q", assistant.Content)
	}
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("tool call should still persist: %#v", assistant.ToolCalls)
	}
}
