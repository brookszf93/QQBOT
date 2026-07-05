package agent

import (
	"QqBot/internal/agentruntime"
	"strings"
	"testing"
)

func TestStateEffectFromEnterSuccessUsesVisibleContext(t *testing.T) {
	effect := stateEffectFromToolResult("enter", `{"ok":true,"focusedStateId":"qq_group:1","allowedTools":["send_message"],"context":"<system_reminder>当前状态：qq_group:1</system_reminder>"}`)

	if effect.FocusedStateID != "qq_group:1" {
		t.Fatalf("focused state mismatch: %q", effect.FocusedStateID)
	}
	if len(effect.AllowedTools) != 1 || effect.AllowedTools[0] != "send_message" {
		t.Fatalf("allowed tools mismatch: %#v", effect.AllowedTools)
	}
	if !strings.Contains(effect.VisibleContext, "当前状态：qq_group:1") {
		t.Fatalf("visible context missing: %q", effect.VisibleContext)
	}
}

func TestStateEffectFromWaitIsEphemeral(t *testing.T) {
	effect := stateEffectFromToolResult("wait", `{"ok":true,"hasEvent":true,"requeuedEventCount":3}`)

	if !effect.Ephemeral {
		t.Fatal("wait effect should be ephemeral")
	}
	if !effect.HasEvent {
		t.Fatal("wait effect should expose has event")
	}
	if effect.VisibleContext != "" {
		t.Fatalf("wait should not add visible context: %q", effect.VisibleContext)
	}
}

func TestStateEffectFromInvokeErrorSanitizesRuntimeJSON(t *testing.T) {
	effect := stateEffectFromToolResult("invoke", `{"ok":false,"error":"INVOKE_TOOL_NOT_AVAILABLE","focusedStateId":"portal","availableTools":[]}`)

	if effect.Error != "INVOKE_TOOL_NOT_AVAILABLE" {
		t.Fatalf("error mismatch: %q", effect.Error)
	}
	if !strings.Contains(effect.VisibleContext, "INVOKE_TOOL_NOT_AVAILABLE") {
		t.Fatalf("visible error missing: %q", effect.VisibleContext)
	}
	if strings.Contains(effect.VisibleContext, "availableChildren") {
		t.Fatalf("internal state leaked: %q", effect.VisibleContext)
	}
}

func TestStateEffectFromEphemeralInvokeErrorDoesNotPolluteContext(t *testing.T) {
	effect := stateEffectFromToolResult("invoke", `{"ok":false,"error":"EMPTY_MESSAGE","message":"send_message message is empty","ephemeral":true}`)

	if !effect.Ephemeral {
		t.Fatal("empty message invoke should be ephemeral")
	}
	if effect.VisibleContext != "" {
		t.Fatalf("ephemeral invoke should not add visible context: %q", effect.VisibleContext)
	}
}

func TestIsTerminalConversationAction(t *testing.T) {
	if !isTerminalConversationAction(agentruntime.ToolCall{Name: "invoke", Arguments: map[string]any{"tool": "send_message"}}) {
		t.Fatal("expected invoke send_message to be detected")
	}
	if !isTerminalConversationAction(agentruntime.ToolCall{Name: "invoke", Arguments: map[string]any{"tool": "zone_out"}}) {
		t.Fatal("expected invoke zone_out to be terminal")
	}
	if !isTerminalConversationAction(agentruntime.ToolCall{Name: "zone_out", Arguments: map[string]any{}}) {
		t.Fatal("expected direct zone_out to be terminal")
	}
	if isTerminalConversationAction(agentruntime.ToolCall{Name: "invoke", Arguments: map[string]any{"tool": "search_web"}}) {
		t.Fatal("search_web should not be treated as send_message")
	}
	if isTerminalConversationAction(agentruntime.ToolCall{Name: "wait", Arguments: map[string]any{}}) {
		t.Fatal("wait should not be treated as send_message")
	}
}
