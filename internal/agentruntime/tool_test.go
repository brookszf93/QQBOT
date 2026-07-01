package agentruntime

import (
	"context"
	"testing"
)

type toolWithoutOS struct{}

type recordingToolObserver struct {
	before int
	after  int
	prior  *ToolResult
}

func (o *recordingToolObserver) BeforeTool(context.Context, ToolCall, ToolDefinition, string) (*ToolResult, error) {
	o.before++
	return o.prior, nil
}

func (o *recordingToolObserver) AfterTool(context.Context, ToolCall, ToolDefinition, ToolResult, error) {
	o.after++
}

func (toolWithoutOS) Definition() ToolDefinition {
	return ToolDefinition{
		Name:       "example",
		Parameters: ObjectSchema(map[string]any{"message": map[string]any{"type": "string"}}),
	}
}

func (toolWithoutOS) Kind() string { return "business" }

func (toolWithoutOS) Execute(context.Context, ToolCall) (ToolResult, error) {
	return ToolResult{}, nil
}

func TestToolCatalogDefinitionsDoNotInjectOSParameter(t *testing.T) {
	definitions := NewToolCatalog(toolWithoutOS{}).Definitions()
	if len(definitions) != 1 {
		t.Fatalf("got %d definitions, want 1", len(definitions))
	}
	properties, _ := definitions[0].Parameters["properties"].(map[string]any)
	if _, ok := properties["OS"]; ok {
		t.Fatal("tool schema should not inject OS")
	}
	if _, ok := properties["os"]; ok {
		t.Fatal("tool schema should not inject legacy os")
	}
	if _, ok := properties["message"]; !ok {
		t.Fatal("definitions must preserve existing parameters")
	}
}

func TestNormalizeToolCallConvertsMisroutedWait(t *testing.T) {
	call := NormalizeToolCall(ToolCall{
		ID:   "call-1",
		Name: "send_message",
		Arguments: map[string]any{
			"action": "wait",
		},
	})
	if call.Name != "wait" {
		t.Fatalf("send_message with action=wait should normalize to direct wait: %#v", call)
	}
	if _, ok := call.Arguments["action"]; ok {
		t.Fatalf("wait normalization should remove action: %#v", call)
	}
}

func TestNormalizeToolCallKeepsDirectSendMessage(t *testing.T) {
	call := NormalizeToolCall(ToolCall{
		ID:   "call-1",
		Name: "send_message",
		Arguments: map[string]any{
			"message":  "hello",
			"targetId": "1001",
		},
	})
	if call.Name != "send_message" {
		t.Fatalf("direct send_message should stay direct: %#v", call)
	}
	if call.Arguments["message"] != "hello" || call.Arguments["targetId"] != "1001" {
		t.Fatalf("normalization should preserve send args: %#v", call.Arguments)
	}
}

func TestNormalizeToolCallKeepsDirectWait(t *testing.T) {
	call := NormalizeToolCall(ToolCall{ID: "call-1", Name: "wait", Arguments: map[string]any{}})
	if call.Name != "wait" {
		t.Fatalf("direct wait should stay direct: %#v", call)
	}
}

func TestNormalizeToolCallKeepsDirectPersonalAppTool(t *testing.T) {
	call := NormalizeToolCall(ToolCall{
		ID:        "call-1",
		Name:      "personal_screen",
		Arguments: map[string]any{"app": "novel"},
	})
	if call.Name != "personal_screen" {
		t.Fatalf("direct personal_screen should stay direct: %#v", call)
	}
	if call.Arguments["app"] != "novel" {
		t.Fatalf("normalization should preserve app args: %#v", call.Arguments)
	}
}

func TestNormalizeToolCallConvertsLegacyActToDirectTool(t *testing.T) {
	call := NormalizeToolCall(ToolCall{
		ID:        "call-1",
		Name:      "act",
		Arguments: map[string]any{"action": "novel_app", "action_text": "screen"},
	})
	if call.Name != "novel_app" {
		t.Fatalf("legacy act should normalize to direct tool: %#v", call)
	}
	if _, ok := call.Arguments["action"]; ok {
		t.Fatalf("legacy act action should be removed: %#v", call)
	}
	if call.Arguments["action_text"] != "screen" {
		t.Fatalf("normalization should preserve app action_text: %#v", call.Arguments)
	}
}

func TestNormalizeToolCallRemovesOSArguments(t *testing.T) {
	call := NormalizeToolCall(ToolCall{
		ID:   "call-1",
		Name: "act",
		Arguments: map[string]any{
			"OS":      "do not keep this",
			"os":      "or this",
			"action":  "wait",
			"message": "keep ordinary args",
		},
	})
	if _, ok := call.Arguments["OS"]; ok {
		t.Fatalf("normalized call should remove OS argument: %#v", call.Arguments)
	}
	if _, ok := call.Arguments["os"]; ok {
		t.Fatalf("normalized call should remove legacy os argument: %#v", call.Arguments)
	}
	if call.Arguments["message"] != "keep ordinary args" {
		t.Fatalf("normalized call should preserve normal arguments: %#v", call.Arguments)
	}
}

func TestToolCatalogObserverCanReplayPriorResult(t *testing.T) {
	catalog := NewToolCatalog(toolWithoutOS{})
	observer := &recordingToolObserver{prior: &ToolResult{Kind: "business", Content: `{"cached":true}`}}
	catalog.SetObserver(observer)
	result, err := catalog.Execute(context.Background(), ToolCall{ID: "call-1", Name: "example", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != `{"cached":true}` || observer.before != 1 || observer.after != 0 {
		t.Fatalf("unexpected observer replay: result=%#v observer=%#v", result, observer)
	}
}
