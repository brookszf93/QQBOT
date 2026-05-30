package agent

import (
	"qqbot-ai/internal/agentruntime"
	"strings"
	"testing"
)

func TestRootContextManagerSanitizeDropsProtocolAndNoisyToolResults(t *testing.T) {
	manager := NewRootContextManager([]agentruntime.Message{
		{Role: "tool", Content: `{"ok":true}`},
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "call_wait", Name: "wait"}}},
		{Role: "user", Content: `<system_reminder>工具 wait 执行结果：{"ok":true}</system_reminder>`},
		{Role: "user", Content: `<system_reminder>{"availableChildren":[]}</system_reminder>`},
		{Role: "user", Content: `<qq_message time="2026-05-12 17:15:47 +08:00">可见消息</qq_message>`},
	})

	manager.Sanitize()
	messages := manager.Messages()

	if len(messages) != 1 {
		t.Fatalf("expected only visible message, got %d: %#v", len(messages), messages)
	}
	if !strings.Contains(messages[0].Content, "可见消息") {
		t.Fatalf("visible message was not preserved: %#v", messages[0])
	}
}

func TestRootContextManagerCompactIfNeeded(t *testing.T) {
	messages := make([]agentruntime.Message, 0, 45)
	for i := 0; i < 45; i++ {
		messages = append(messages, agentruntime.Message{Role: "user", Content: strings.Repeat("上下文", 700)})
	}
	manager := NewRootContextManager(messages)

	summary, compacted := manager.CompactIfNeeded(0, 1, func([]agentruntime.Message) string {
		return "压缩摘要"
	})

	if !compacted {
		t.Fatal("expected compaction")
	}
	if summary != "压缩摘要" {
		t.Fatalf("unexpected summary: %q", summary)
	}
	got := manager.Messages()
	if len(got) != 31 {
		t.Fatalf("expected summary plus 30 recent messages, got %d", len(got))
	}
	if !strings.Contains(got[0].Content, "压缩摘要") {
		t.Fatalf("summary message missing: %#v", got[0])
	}
}

func TestRootContextManagerCompactionKeepsAssistantToolBoundaryTogether(t *testing.T) {
	messages := make([]agentruntime.Message, 0, 45)
	for i := 0; i < 14; i++ {
		messages = append(messages, agentruntime.Message{Role: "user", Content: strings.Repeat("旧上下文", 800)})
	}
	messages = append(messages,
		agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "call_wait", Name: "wait"}}},
		agentruntime.Message{Role: "tool", ToolCallID: "call_wait", Content: waitResumeContent("event")},
	)
	for i := 0; i < 29; i++ {
		messages = append(messages, agentruntime.Message{Role: "user", Content: strings.Repeat("新上下文", 800)})
	}
	manager := NewRootContextManager(messages)

	_, compacted := manager.CompactIfNeeded(0, 1, func(messages []agentruntime.Message) string {
		for _, msg := range messages {
			if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
				return "包含 wait assistant 和 tool result"
			}
		}
		return "missing"
	})

	if !compacted {
		t.Fatal("expected compaction")
	}
	got := manager.Messages()
	for _, msg := range got[1:] {
		if msg.Role == "tool" && msg.ToolCallID == "call_wait" {
			t.Fatalf("tool result was kept without its assistant boundary: %#v", got)
		}
	}
	if !strings.Contains(got[0].Content, "包含 wait assistant 和 tool result") {
		t.Fatalf("summary did not receive complete tool boundary: %#v", got[0])
	}
}

func TestRootContextManagerClassifiesLayers(t *testing.T) {
	manager := NewRootContextManager([]agentruntime.Message{
		{Role: "user", Content: "<qq_message>新消息</qq_message>"},
		{Role: "system", Content: "<system_reminder>当前状态：qq_group:1</system_reminder>"},
		{Role: "user", Content: "<system_reminder>工具 enter 执行结果：...</system_reminder>"},
		{Role: "user", Content: "<story_recall>旧事</story_recall>"},
	})

	if got := manager.EntriesByLayer(RootContextLayerEvent); len(got) != 1 {
		t.Fatalf("event layer count mismatch: %d", len(got))
	}
	if got := manager.EntriesByLayer(RootContextLayerWorkingSet); len(got) != 1 {
		t.Fatalf("working layer count mismatch: %d", len(got))
	}
	if got := manager.EntriesByLayer(RootContextLayerToolEffect); len(got) != 1 {
		t.Fatalf("tool layer count mismatch: %d", len(got))
	}
	if got := manager.EntriesByLayer(RootContextLayerRecall); len(got) != 1 {
		t.Fatalf("recall layer count mismatch: %d", len(got))
	}
}

func TestRootContextManagerKeepsLayerBudgets(t *testing.T) {
	manager := NewRootContextManager(nil)
	for i := 0; i < 100; i++ {
		manager.Append(RootContextLayerEvent, agentruntime.Message{Role: "user", Content: "event"})
	}
	for i := 0; i < 20; i++ {
		manager.Append(RootContextLayerToolEffect, agentruntime.Message{Role: "user", Content: "tool"})
	}
	for i := 0; i < 20; i++ {
		manager.Append(RootContextLayerWorkingSet, agentruntime.Message{Role: "system", Content: "working"})
	}

	if got := len(manager.EntriesByLayer(RootContextLayerEvent)); got != 80 {
		t.Fatalf("event budget mismatch: %d", got)
	}
	if got := len(manager.EntriesByLayer(RootContextLayerToolEffect)); got != 8 {
		t.Fatalf("tool budget mismatch: %d", got)
	}
	if got := len(manager.EntriesByLayer(RootContextLayerWorkingSet)); got != 6 {
		t.Fatalf("working budget mismatch: %d", got)
	}
}

func TestRootContextManagerAssemblesStablePromptOrder(t *testing.T) {
	manager := NewRootContextManager(nil)
	manager.Append(RootContextLayerEvent, agentruntime.Message{Role: "user", Content: "event"})
	manager.Append(RootContextLayerSystem, agentruntime.Message{Role: "system", Content: "system"})
	manager.Append(RootContextLayerRecall, agentruntime.Message{Role: "user", Content: "recall"})
	manager.Append(RootContextLayerToolEffect, agentruntime.Message{Role: "user", Content: "tool"})

	got := manager.Messages()
	if len(got) != 4 {
		t.Fatalf("message count mismatch: %d", len(got))
	}
	if got[0].Content != "system" || got[1].Content != "recall" || got[2].Content != "event" || got[3].Content != "tool" {
		t.Fatalf("unexpected assembled order: %#v", got)
	}
}

func TestRootContextManagerClassifiesSummary(t *testing.T) {
	manager := NewRootContextManager([]agentruntime.Message{
		{Role: "system", Content: "以下是已压缩的历史上下文摘要：\n摘要"},
	})

	if got := len(manager.EntriesByLayer(RootContextLayerSummary)); got != 1 {
		t.Fatalf("summary layer count mismatch: %d", got)
	}
}

func TestRootContextManagerClassifiesEventLayer(t *testing.T) {
	manager := NewRootContextManager([]agentruntime.Message{
		{Role: "user", Content: "<qq_message>新消息</qq_message>"},
	})

	if got := manager.EntriesByLayer(RootContextLayerEvent)[0].Layer; got != RootContextLayerEvent {
		t.Fatalf("event layer mismatch: %s", got)
	}
}
