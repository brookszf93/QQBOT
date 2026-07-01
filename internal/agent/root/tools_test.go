package root

import (
	"context"
	"strings"
	"testing"
	"time"

	"QqBot/internal/agentruntime"
)

type recordingSendTool struct {
	called *bool
	args   *map[string]any
}

func (t recordingSendTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{
		Name:       "send_message",
		Parameters: agentruntime.ObjectSchema(map[string]any{}),
	}
}

func (recordingSendTool) Kind() string { return "business" }

func (t recordingSendTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	*t.called = true
	if t.args != nil {
		*t.args = call.Arguments
	}
	return agentruntime.ToolResult{Kind: "business", Content: `{"messageId":1}`}, nil
}

func TestCatalogSubtoolOwnerDoesNotDropSendMessage(t *testing.T) {
	called := false
	owner := CatalogSubtoolOwner{
		Tools: agentruntime.NewToolCatalog(recordingSendTool{called: &called}),
	}

	result, err := owner.ExecuteSubtool(
		context.Background(),
		"send_message",
		map[string]any{"message": "已生成的任务结果"},
		agentruntime.ToolCall{ID: "call-1", Name: "invoke"},
	)
	if err != nil {
		t.Fatalf("ExecuteSubtool returned error: %v", err)
	}
	if !called {
		t.Fatal("send_message must not be dropped when a newer event is queued")
	}
	if result.Content != `{"messageId":1}` {
		t.Fatalf("unexpected send result: %s", result.Content)
	}
}

func TestInvokeInfersSendMessageWhenToolNameMissing(t *testing.T) {
	called := false
	tool := InvokeTool{Owners: []InvokeSubtoolOwner{CatalogSubtoolOwner{
		Tools: agentruntime.NewToolCatalog(recordingSendTool{called: &called}),
	}}}
	result, err := tool.Execute(context.Background(), agentruntime.ToolCall{
		ID: "call-2", Name: "invoke", Arguments: map[string]any{"message": "直接发出"},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !called || result.Content != `{"messageId":1}` {
		t.Fatalf("missing tool name should infer send_message: called=%v result=%s", called, result.Content)
	}
}

func TestActDispatchesSendMessage(t *testing.T) {
	called := false
	var args map[string]any
	tool := ActTool{Invoke: InvokeTool{Owners: []InvokeSubtoolOwner{CatalogSubtoolOwner{
		Tools: agentruntime.NewToolCatalog(recordingSendTool{called: &called, args: &args}),
	}}}}
	result, err := tool.Execute(context.Background(), agentruntime.ToolCall{
		ID: "act-1", Name: "act", Arguments: map[string]any{
			"action":  "send_message",
			"message": "直接发出",
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !called || result.Content != `{"messageId":1}` {
		t.Fatalf("act should dispatch send_message: called=%v result=%s", called, result.Content)
	}
	if args["message"] != "直接发出" {
		t.Fatalf("act should preserve delegated args: %#v", args)
	}
}

func TestNormalizeEnterArgumentsAcceptsActAliases(t *testing.T) {
	for _, args := range []map[string]any{
		{"message": "novel"},
		{"query": "novel"},
		{"app": "novel"},
		{"target": "novel"},
	} {
		if got := normalizeEnterArguments(args); got != "novel" {
			t.Fatalf("enter aliases should resolve to novel, got %q for %#v", got, args)
		}
	}
}

func TestCatalogSubtoolOwnerInjectsLatestParallelRoute(t *testing.T) {
	called := false
	var args map[string]any
	session := NewSession([]string{"1001", "1002"})
	session.OnGroupMessage("1002", "2001", "alice", "hello", 1, 2, time.Time{})
	owner := CatalogSubtoolOwner{
		Tools:   agentruntime.NewToolCatalog(recordingSendTool{called: &called, args: &args}),
		Session: session,
	}
	_, err := owner.ExecuteSubtool(
		context.Background(),
		"send_message",
		map[string]any{"message": "reply"},
		agentruntime.ToolCall{ID: "call-3", Name: "send_message"},
	)
	if err != nil {
		t.Fatalf("ExecuteSubtool returned error: %v", err)
	}
	if !called || args["targetType"] != "group" || args["targetId"] != "1002" {
		t.Fatalf("latest route was not injected correctly: called=%v args=%#v", called, args)
	}
}

func TestDirectSubtoolChecksCurrentSessionPermission(t *testing.T) {
	called := false
	session := NewSession([]string{"1001"})
	if result := session.EnterApp("terminal"); result["ok"] != true {
		t.Fatalf("failed to enter terminal: %#v", result)
	}
	owner := CatalogSubtoolOwner{
		Tools:   agentruntime.NewToolCatalog(recordingSendTool{called: &called}),
		Session: session,
	}
	tool := DirectSubtool{
		Owner:           owner,
		DefinitionValue: recordingSendTool{}.Definition(),
		ToolKind:        "business",
		CheckPermission: true,
	}
	result, err := tool.Execute(context.Background(), agentruntime.ToolCall{
		ID: "call-4", Name: "send_message", Arguments: map[string]any{"message": "不应发送"},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if called {
		t.Fatal("unavailable direct tool must not execute")
	}
	if !strings.Contains(result.Content, "INVOKE_TOOL_NOT_AVAILABLE") {
		t.Fatalf("unexpected permission result: %s", result.Content)
	}
}

func TestOrderedParameterNamesAreStableSorted(t *testing.T) {
	names := orderedParameterNames(map[string]any{
		"message":    nil,
		"targetType": nil,
		"imagePath":  nil,
	})
	if got := strings.Join(names, ","); got != "imagePath,message,targetType" {
		t.Fatalf("parameters should be stable sorted, got %s", got)
	}
}
