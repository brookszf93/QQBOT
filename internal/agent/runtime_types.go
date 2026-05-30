package agent

import (
	"qqbot-ai/internal/agentruntime"
	"time"
)

type RuntimeError struct {
	Name      string `json:"name"`
	Message   string `json:"message"`
	UpdatedAt string `json:"updatedAt"`
}

type DashboardContextItem struct {
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Preview   string `json:"preview"`
	Truncated bool   `json:"truncated"`
}

type DashboardToolCall struct {
	Name             string `json:"name"`
	ArgumentsPreview string `json:"argumentsPreview"`
	UpdatedAt        string `json:"updatedAt"`
}

type DashboardLlmCall struct {
	Provider                string   `json:"provider"`
	Model                   string   `json:"model"`
	OSPreview               string   `json:"osPreview,omitempty"`
	AssistantContentPreview string   `json:"assistantContentPreview"`
	ToolCallNames           []string `json:"toolCallNames"`
	TotalTokens             *int     `json:"totalTokens"`
	UpdatedAt               string   `json:"updatedAt"`
}

type AgentDashboardSnapshot struct {
	GeneratedAt string                `json:"generatedAt"`
	Agents      []AgentDashboardAgent `json:"agents"`
}

type AgentDashboardAgent struct {
	ID        string                   `json:"id"`
	Kind      string                   `json:"kind"`
	Label     string                   `json:"label"`
	Runtime   DashboardRuntimeSummary  `json:"runtime"`
	Context   DashboardContextSummary  `json:"context"`
	Activity  DashboardActivitySummary `json:"activity"`
	Session   *DashboardSessionSummary `json:"session,omitempty"`
	Queue     *DashboardQueueSummary   `json:"queue,omitempty"`
	Providers any                      `json:"providers,omitempty"`
	Story     *DashboardStorySummary   `json:"story,omitempty"`
}

type DashboardRuntimeSummary struct {
	Initialized          bool          `json:"initialized"`
	LoopState            string        `json:"loopState"`
	LastError            *RuntimeError `json:"lastError"`
	LastActivityAt       any           `json:"lastActivityAt"`
	LastRoundCompletedAt any           `json:"lastRoundCompletedAt"`
	LastCompactionAt     any           `json:"lastCompactionAt"`
}

type DashboardContextSummary struct {
	MessageCount                  int                    `json:"messageCount"`
	CompactionTotalTokenThreshold int                    `json:"compactionTotalTokenThreshold"`
	RecentItems                   []DashboardContextItem `json:"recentItems"`
	RecentItemsTruncated          bool                   `json:"recentItemsTruncated"`
}

type DashboardActivitySummary struct {
	LastToolCall          *DashboardToolCall `json:"lastToolCall"`
	LastToolResultPreview *string            `json:"lastToolResultPreview"`
	LastLlmCall           *DashboardLlmCall  `json:"lastLlmCall"`
}

type DashboardSessionSummary struct {
	FocusedStateID          string   `json:"focusedStateId"`
	FocusedStateDisplayName string   `json:"focusedStateDisplayName"`
	FocusedStateDescription string   `json:"focusedStateDescription"`
	StateStack              []any    `json:"stateStack"`
	Children                []any    `json:"children"`
	AvailableInvokeTools    []string `json:"availableInvokeTools"`
}

type DashboardQueueSummary struct {
	PendingEventCount int `json:"pendingEventCount"`
}

type DashboardStorySummary struct {
	LastProcessedMessageSeq int `json:"lastProcessedMessageSeq"`
	PendingMessageCount     int `json:"pendingMessageCount"`
	PendingBatch            any `json:"pendingBatch"`
	BatchSize               int `json:"batchSize"`
	IdleFlushMs             int `json:"idleFlushMs"`
}

type chatReplyTarget struct {
	Type string
	ID   string
}

type agentRuntimeSnapshot struct {
	RootMessages     []agentruntime.Message `json:"rootMessages"`
	StoryMessages    []agentruntime.Message `json:"storyMessages"`
	StoryLastSeq     int                    `json:"storyLastSeq"`
	LastRecallCount  int                    `json:"lastRecallCount"`
	RecalledStoryIDs map[string]bool        `json:"recalledStoryIds"`
	Session          rootSessionSnapshot    `json:"session"`
	UpdatedAt        time.Time              `json:"updatedAt"`
}
