package agent

import (
	"qqbot-ai/internal/agentruntime"
	"sort"
	"strings"
	"time"
)

type RootContextLayer string

const (
	RootContextLayerSystem     RootContextLayer = "system"
	RootContextLayerEvent      RootContextLayer = "event"
	RootContextLayerWorkingSet RootContextLayer = "working_set"
	RootContextLayerToolEffect RootContextLayer = "tool_effect"
	RootContextLayerRecall     RootContextLayer = "recall"
	RootContextLayerAssistant  RootContextLayer = "assistant"
	RootContextLayerSummary    RootContextLayer = "summary"
)

type RootContextEntry struct {
	Layer     RootContextLayer
	Message   agentruntime.Message
	CreatedAt time.Time
	Seq       int64
}

// RootContextManager 管理根 Agent 的工作上下文。
//
// 它把事件、当前可见状态、工具结果、长期召回和助手发言先标记成不同层，
// 再统一输出为 LLM 可消费的消息列表。这样运行时不需要到处直接 append
// rootMessages，后续继续做分层预算、过期策略和更强去重时也有固定入口。
type RootContextManager struct {
	nextSeq    int64
	system     []RootContextEntry
	summaries  []RootContextEntry
	recalls    []RootContextEntry
	events     []RootContextEntry
	workingSet []RootContextEntry
	toolEffect []RootContextEntry
	assistant  []RootContextEntry
}

func NewRootContextManager(messages []agentruntime.Message) *RootContextManager {
	manager := &RootContextManager{}
	manager.ReplaceMessages(messages)
	return manager
}

func (m *RootContextManager) ReplaceMessages(messages []agentruntime.Message) {
	m.clear()
	for _, msg := range messages {
		m.Append(classifyRootContextLayer(msg), msg)
	}
}

func (m *RootContextManager) Append(layer RootContextLayer, msg agentruntime.Message) {
	m.nextSeq++
	entry := RootContextEntry{
		Layer:     layer,
		Message:   msg,
		CreatedAt: time.Now(),
		Seq:       m.nextSeq,
	}
	switch layer {
	case RootContextLayerSummary:
		m.summaries = appendAndKeepRecent(m.summaries, entry, 2)
	case RootContextLayerSystem:
		m.system = appendAndKeepRecent(m.system, entry, 8)
	case RootContextLayerRecall:
		m.recalls = appendAndKeepRecent(m.recalls, entry, 6)
	case RootContextLayerWorkingSet:
		m.workingSet = appendAndKeepRecent(m.workingSet, entry, 6)
	case RootContextLayerToolEffect:
		m.toolEffect = appendAndKeepRecent(m.toolEffect, entry, 8)
	case RootContextLayerAssistant:
		m.assistant = appendAndKeepRecent(m.assistant, entry, 20)
	default:
		m.events = appendAndKeepRecent(m.events, entry, 80)
	}
}

func (m *RootContextManager) Len() int {
	return len(m.allEntries())
}

func (m *RootContextManager) Messages() []agentruntime.Message {
	entries := m.assembleEntries()
	messages := make([]agentruntime.Message, 0, len(entries))
	for _, entry := range entries {
		messages = append(messages, entry.Message)
	}
	return messages
}

func (m *RootContextManager) Sanitize() {
	m.ReplaceMessages(sanitizeRootMessages(m.Messages()))
}

func (m *RootContextManager) TotalChars() int {
	total := 0
	for _, entry := range m.assembleEntries() {
		total += len([]rune(entry.Message.Content))
	}
	return total
}

func (m *RootContextManager) RecentQuery(limit int) string {
	return recentContextQuery(m.Messages(), limit)
}

func (m *RootContextManager) CompactIfNeeded(totalTokens, threshold int, summarize func([]agentruntime.Message) string) (string, bool) {
	totalChars := m.TotalChars()
	if totalTokens <= 0 {
		totalTokens = totalChars / 3
	}
	hitTokenThreshold := threshold > 0 && totalTokens >= threshold
	currentLen := m.Len()
	hitHardThreshold := totalChars >= 60000 || currentLen >= 120
	if (!hitTokenThreshold && !hitHardThreshold) || currentLen < 40 {
		return "", false
	}
	keep := calculateCompactionKeepCount(currentLen)
	if currentLen <= keep {
		return "", false
	}
	messages := m.Messages()
	cutIndex := extendCompactionCutIndexForToolBoundary(messages, len(messages)-keep)
	if cutIndex <= 0 || cutIndex >= len(messages) {
		return "", false
	}
	summary := summarize(messages[:cutIndex])
	kept := append([]agentruntime.Message{{Role: "system", Content: "以下是已压缩的历史上下文摘要：\n" + summary}}, messages[cutIndex:]...)
	m.ReplaceMessages(kept)
	return summary, true
}

func calculateCompactionKeepCount(totalMessageCount int) int {
	if totalMessageCount <= 1 {
		return 0
	}
	keep := (totalMessageCount + 9) / 10
	if keep < 1 {
		keep = 1
	}
	if keep < 30 {
		keep = 30
	}
	return keep
}

func extendCompactionCutIndexForToolBoundary(messages []agentruntime.Message, cutIndex int) int {
	if cutIndex <= 0 || cutIndex >= len(messages) {
		return cutIndex
	}
	boundary := messages[cutIndex-1]
	if boundary.Role != "assistant" || len(boundary.ToolCalls) == 0 {
		return cutIndex
	}
	toolCallIDs := map[string]bool{}
	for _, call := range boundary.ToolCalls {
		if strings.TrimSpace(call.ID) != "" {
			toolCallIDs[call.ID] = true
		}
	}
	lastMatchingToolIndex := -1
	for i := cutIndex; i < len(messages); i++ {
		msg := messages[i]
		if msg.Role != "tool" {
			continue
		}
		if len(toolCallIDs) == 0 || toolCallIDs[msg.ToolCallID] {
			lastMatchingToolIndex = i
		}
	}
	if lastMatchingToolIndex >= 0 {
		return lastMatchingToolIndex + 1
	}
	return cutIndex
}

func (m *RootContextManager) clear() {
	m.system = nil
	m.summaries = nil
	m.recalls = nil
	m.events = nil
	m.workingSet = nil
	m.toolEffect = nil
	m.assistant = nil
	m.nextSeq = 0
}

func (m *RootContextManager) allEntries() []RootContextEntry {
	out := []RootContextEntry{}
	out = append(out, m.summaries...)
	out = append(out, m.system...)
	out = append(out, m.recalls...)
	out = append(out, m.events...)
	out = append(out, m.workingSet...)
	out = append(out, m.toolEffect...)
	out = append(out, m.assistant...)
	return out
}

func (m *RootContextManager) assembleEntries() []RootContextEntry {
	out := []RootContextEntry{}
	out = append(out, m.summaries...)
	out = append(out, m.system...)
	out = append(out, m.recalls...)

	core := []RootContextEntry{}
	core = append(core, m.events...)
	core = append(core, m.workingSet...)
	core = append(core, m.toolEffect...)
	core = append(core, m.assistant...)
	sort.SliceStable(core, func(i, j int) bool { return core[i].Seq < core[j].Seq })

	out = append(out, core...)
	return out
}

func (m *RootContextManager) EntriesByLayer(layer RootContextLayer) []RootContextEntry {
	var source []RootContextEntry
	switch layer {
	case RootContextLayerSummary:
		source = m.summaries
	case RootContextLayerSystem:
		source = m.system
	case RootContextLayerRecall:
		source = m.recalls
	case RootContextLayerWorkingSet:
		source = m.workingSet
	case RootContextLayerToolEffect:
		source = m.toolEffect
	case RootContextLayerAssistant:
		source = m.assistant
	default:
		source = m.events
	}
	return append([]RootContextEntry(nil), source...)
}

func appendAndKeepRecent(entries []RootContextEntry, entry RootContextEntry, max int) []RootContextEntry {
	entries = append(entries, entry)
	if max > 0 && len(entries) > max {
		return append([]RootContextEntry(nil), entries[len(entries)-max:]...)
	}
	return entries
}

func classifyRootContextLayer(msg agentruntime.Message) RootContextLayer {
	content := msg.Content
	switch {
	case containsRootContextMarker(content, "以下是已压缩的历史上下文摘要"):
		return RootContextLayerSummary
	case containsRootContextMarker(content, "<story_recall>"):
		return RootContextLayerRecall
	case containsRootContextMarker(content, "工具 ") || containsRootContextMarker(content, "进入状态失败"):
		return RootContextLayerToolEffect
	case containsRootContextMarker(content, `kind="tool_result"`) || containsRootContextMarker(content, `kind="tool_error"`):
		return RootContextLayerToolEffect
	case containsRootContextMarker(content, "当前状态：") || containsRootContextMarker(content, "你当前处于门户状态") || containsRootContextMarker(content, `kind="state"`):
		return RootContextLayerWorkingSet
	case msg.Role == "assistant":
		return RootContextLayerAssistant
	case msg.Role == "system":
		return RootContextLayerSystem
	default:
		return RootContextLayerEvent
	}
}

func containsRootContextMarker(content, marker string) bool {
	return marker != "" && strings.Contains(content, marker)
}
