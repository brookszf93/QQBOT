package agent

import (
	roottools "QqBot/internal/agent/root"
	"QqBot/internal/agentruntime"
	"QqBot/internal/capabilities/contextsummary"
	"QqBot/internal/capabilities/messaging"
	"QqBot/internal/capabilities/personalapp"
	"QqBot/internal/capabilities/terminal"
	"QqBot/internal/common"
	"QqBot/internal/config"
	"QqBot/internal/db"
	"QqBot/internal/llm"
	"QqBot/internal/prompts"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// EventQueue 是可执行服务使用的根包事件队列。
//
// internal/agentruntime 中的队列是可复用版本；这里保留该队列是为了
// 兼容当前根运行时组装方式和仪表盘结构。
type EventQueue struct {
	mu     sync.Mutex
	queue  []AgentEvent
	wakeup chan struct{}
}

// AgentEvent 是根 Agent 消费的标准化外部事件。
type AgentEvent struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
	At   time.Time      `json:"at"`
}

func NewEventQueue() *EventQueue {
	return &EventQueue{wakeup: make(chan struct{}, 1)}
}

func (q *EventQueue) Enqueue(event AgentEvent) {
	if event.At.IsZero() {
		event.At = time.Now()
	}
	q.mu.Lock()
	q.queue = append(q.queue, event)
	q.mu.Unlock()
	select {
	case q.wakeup <- struct{}{}:
	default:
	}
}

func (q *EventQueue) DequeueAll() []AgentEvent {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.queue
	q.queue = nil
	for {
		select {
		case <-q.wakeup:
		default:
			return out
		}
	}
}

func (q *EventQueue) Count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.queue)
}

func (q *EventQueue) WaitSignal(ctx context.Context) bool {
	if q.Count() > 0 {
		return true
	}
	select {
	case <-q.wakeup:
		return true
	case <-ctx.Done():
		return false
	}
}

func wakeTriggersRootRound(reason string) bool {
	return reason == "continue_after_tool" || reason == "self_continuation"
}

// AgentRuntime 是当前可执行服务中的根/Story 运行时门面。
//
// 它维护仪表盘状态，并从进入的消息中创建轻量 Story 记录；
// 更完整的 internal/agent 运行时可用于后续更深层的接线。
type AgentRuntime struct {
	cfg                 *config.Config
	store               *db.Store
	events              *EventQueue
	llm                 *llm.LLMClient
	rootKernel          agentruntime.ReActKernel
	storyKernel         agentruntime.ReActKernel
	summarizer          contextsummary.Operation
	rootTools           *agentruntime.ToolCatalog
	storyTools          *agentruntime.ToolCatalog
	session             *roottools.Session
	rootMessages        []agentruntime.Message
	storyMessages       []agentruntime.Message
	mu                  sync.Mutex
	initialized         bool
	loopState           string
	lastError           *RuntimeError
	lastActivity        *time.Time
	contextItems        []DashboardContextItem
	lastToolCall        *DashboardToolCall
	lastToolResult      *string
	lastLlmCall         *DashboardLlmCall
	storyLastSeq        int
	terminal            *terminal.Service
	personal            *personalapp.Service
	lastCompaction      *time.Time
	lastWakeReminderAt  *time.Time
	lastStoryRecallAt   int
	lastStoryRecallKey  string
	injectedStoryIDs    map[string]bool
	lastReadOnlyToolSig string
	storyBatchRunning   bool
	storyIdleTimer      *time.Timer
	storyRecallRunning  bool
	autonomousRounds    int
	autonomousPending   bool
	autonomousReminder  string
	autonomousTimer     *time.Timer
	autonomousUntil     *time.Time
	lastRhythmSignal    *DashboardRhythmSignal
	lastToolGuardEvent  *DashboardToolGuardEvent
	lastCreativeRhythm  *time.Time
	lastReviewRhythm    *time.Time
	lastNewsRhythm      *time.Time
}

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
	AssistantContentPreview string   `json:"assistantContentPreview"`
	ToolCallNames           []string `json:"toolCallNames"`
	TotalTokens             *int     `json:"totalTokens"`
	UpdatedAt               string   `json:"updatedAt"`
}

type DashboardRhythmSignal struct {
	Kind             string   `json:"kind"`
	Reason           string   `json:"reason"`
	SuggestedActions []string `json:"suggestedActions"`
	IdleForMs        int64    `json:"idleForMs"`
	CreatedAt        string   `json:"createdAt"`
}

type DashboardToolGuardEvent struct {
	Rule      string `json:"rule"`
	Tool      string `json:"tool"`
	Signature string `json:"signature,omitempty"`
	Message   string `json:"message"`
	UpdatedAt string `json:"updatedAt"`
}

func NewAgentRuntime(cfg *config.Config, store *db.Store, events *EventQueue, llmClient *llm.LLMClient, sender messaging.Sender) *AgentRuntime {
	rootModel := llmModelAdapter{client: llmClient, usage: "agent"}
	browserModel := llmModelAdapter{client: llmClient, usage: "browserAgent"}
	storyModel := llmModelAdapter{client: llmClient, usage: "storyAgent"}
	summarizerModel := llmModelAdapter{client: llmClient, usage: "contextSummarizer"}
	terminalService, _ := terminal.NewService(terminal.Config{
		InitialCwd:        cfg.Server.Agent.Terminal.InitialCWD,
		CommandTimeout:    time.Duration(cfg.Server.Agent.Terminal.CommandTimeoutMs) * time.Millisecond,
		PreviewBytes:      cfg.Server.Agent.Terminal.PreviewBytes,
		MaxOutputBytes:    cfg.Server.Agent.Terminal.MaxOutputBytes,
		MaxCommandLength:  cfg.Server.Agent.Terminal.MaxCommandLength,
		ReadOutputMaxSize: cfg.Server.Agent.Terminal.ReadOutputMaxSize,
		Shell:             cfg.Server.Agent.Terminal.Shell,
	}, store)
	personalService := personalapp.NewService(personalAppsRoot(cfg))
	session := roottools.NewSession(cfg.Server.Napcat.ListenGroupIDs)
	runtime := &AgentRuntime{
		cfg:              cfg,
		store:            store,
		events:           events,
		llm:              llmClient,
		rootKernel:       agentruntime.ReActKernel{Model: rootModel},
		storyKernel:      agentruntime.ReActKernel{Model: storyModel},
		summarizer:       contextsummary.Operation{Model: summarizerModel},
		rootTools:        buildBusinessTools(cfg, store, sender, terminalService, llmClient, personalService),
		storyTools:       buildStoryTools(cfg, store),
		session:          session,
		terminal:         terminalService,
		personal:         personalService,
		loopState:        "starting",
		injectedStoryIDs: map[string]bool{},
	}
	if snapshot, ok := store.AgentRuntimeSnapshot(); ok {
		runtime.rootMessages = snapshot.RootMessages
		runtime.storyMessages = snapshot.StoryMessages
		runtime.storyLastSeq = snapshot.StoryLastSeq
		runtime.session.Restore(snapshot.Session)
	}
	if tool, ok := runtime.rootTools.Get("search_web"); ok {
		if searchTool, ok := tool.(*WebSearchTaskAgentTool); ok {
			searchTool.SetModel(rootModel)
			searchTool.SetTaskContext(
				func() string { return createSystemPrompt(cfg) },
				func() []agentruntime.Message {
					runtime.mu.Lock()
					defer runtime.mu.Unlock()
					return append([]agentruntime.Message(nil), runtime.rootMessages...)
				},
				func() *agentruntime.ToolCatalog {
					return rootTools(cfg, runtime.rootTools, runtime.session, runtime.events)
				},
			)
		}
	}
	if tool, ok := runtime.rootTools.Get("browser"); ok {
		if browserTool, ok := tool.(*BrowserTaskAgentTool); ok {
			browserTool.SetModel(browserModel)
			browserTool.SetTaskContext(
				func() string { return createSystemPrompt(cfg) },
				func() []agentruntime.Message {
					runtime.mu.Lock()
					defer runtime.mu.Unlock()
					return append([]agentruntime.Message(nil), runtime.rootMessages...)
				},
			)
		}
	}
	return runtime
}

func personalAppsRoot(_ *config.Config) string {
	return "data/personal-apps"
}

func (a *AgentRuntime) Start(ctx context.Context) {
	failedTools, uncertainTools := a.store.RecoverExpiredToolExecutions(time.Now())
	recoveredTasks := a.store.RecoverExpiredAgentTasks(time.Now())
	if failedTools > 0 || uncertainTools > 0 || recoveredTasks > 0 {
		a.store.Log("warn", "Recovered stale agent runtime work", map[string]any{
			"event":                "agent.runtime.recovered",
			"failedToolExecutions": failedTools,
			"uncertainSideEffects": uncertainTools,
			"recoveredAgentTasks":  recoveredTasks,
		})
	}
	a.mu.Lock()
	now := time.Now()
	a.initialized = true
	a.loopState = "idle"
	a.lastActivity = &now
	if len(a.rootMessages) == 0 {
		a.contextItems = append(a.contextItems, contextItem("llm_message", "system", createSystemPrompt(a.cfg)))
		for _, message := range a.focusMessagesForStateLocked("portal") {
			a.appendRootMessageLocked(message)
			a.contextItems = append(a.contextItems, contextItem("system_reminder", "state_focus", message.Content))
		}
	} else {
		for _, message := range a.rootMessages {
			a.contextItems = append(a.contextItems, contextItem("llm_message", message.Role, message.Content))
		}
	}
	a.mu.Unlock()

	go a.runAgentTaskWorker(ctx)
	a.scheduleStoryBatch()
	go a.runAutonomousIdleWatcher(ctx)
	go func() {
		continueRound := false
		for {
			if ctx.Err() != nil {
				return
			}
			shouldRunRoot := a.consumePendingEvents()
			if !shouldRunRoot && !continueRound {
				a.markRootLoopIdle()
				if !a.events.WaitSignal(ctx) {
					return
				}
				continue
			}
			continueRound = a.runRootRound()
			a.markRootLoopIdle()
		}
	}()
}

func (a *AgentRuntime) runAutonomousIdleWatcher(ctx context.Context) {
	cfg := a.cfg.Server.Agent.Autonomous
	if !cfg.Enabled {
		return
	}
	idleDelay := autonomousIdleDelay(cfg)
	tick := autonomousIdleWatchInterval(idleDelay)
	timer := time.NewTimer(tick)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			now := time.Now()
			if a.shouldQueueAutonomousIdleWake(now, idleDelay) {
				signal := a.nextRhythmSignal(now, idleDelay)
				a.events.Enqueue(AgentEvent{
					Type: "wake",
					Data: map[string]any{
						"reason":  "self_continuation",
						"source":  "rhythm_scheduler",
						"rhythm":  signal.Kind,
						"message": signal.Reason,
						"actions": signal.SuggestedActions,
					},
				})
				a.store.Log("info", "Agent rhythm wake queued", map[string]any{
					"event":      "agent.root.rhythm.queued",
					"rhythm":     signal.Kind,
					"idleForMs":  signal.IdleForMs,
					"idleDelay":  int(idleDelay.Milliseconds()),
					"suggestion": signal.SuggestedActions,
				})
			}
			timer.Reset(tick)
		}
	}
}

func (a *AgentRuntime) nextRhythmSignal(now time.Time, idleDelay time.Duration) DashboardRhythmSignal {
	idleFor := idleDelay
	a.mu.Lock()
	if a.lastActivity != nil {
		idleFor = now.Sub(*a.lastActivity)
	}
	a.mu.Unlock()
	overview := personalapp.WorkspaceOverview{}
	if a.personal != nil {
		if loaded, err := a.personal.WorkspaceOverview(); err == nil {
			overview = loaded
		}
	}
	kind := "quiet"
	reason := "外界暂时安静，可以选择做一小步自己的事，也可以 wait。"
	actions := []string{"没有明确想做的事就 wait", "有灵感就写一小段随笔", "可以整理项目或待办"}
	if overview.CurrentActivity != nil {
		kind = "continue"
		reason = "你有一个未完成的活动：" + overview.CurrentActivity.Title + "。可以继续一个明确步骤，或者 finish 活动。"
		actions = []string{"继续当前活动的一小步", "完成活动并记录结果", "如果不想继续就 wait"}
	} else if a.rhythmDue(now, "creative") {
		kind = "creative"
		reason = "到了适合写一点自己的东西的安静时刻。"
		actions = []string{"用 novel_app 写随笔/灵感", "用 activity_app start 记录写作活动", "没有想法就 wait"}
	} else if a.rhythmDue(now, "news") && a.unreadNewsCount() > 0 {
		kind = "news"
		reason = "有未读新闻，且到了适合阅读摘记的节奏。"
		actions = []string{"用 open_ithome_article 看一篇新闻", "用 news_app 保存 takeaway", "不想读就 wait"}
	} else if a.rhythmDue(now, "review") {
		kind = "review"
		reason = "到了适合整理自己工作台的节奏。"
		actions = []string{"用 workspace_app 看文件工作台总览", "整理 todo/project/music/news 中的一小项", "没有要整理的就 wait"}
	}
	return DashboardRhythmSignal{
		Kind:             kind,
		Reason:           reason,
		SuggestedActions: actions,
		IdleForMs:        idleFor.Milliseconds(),
		CreatedAt:        common.ISO(now),
	}
}

func (a *AgentRuntime) rhythmDue(now time.Time, kind string) bool {
	var last **time.Time
	var interval time.Duration
	cfg := a.cfg.Server.Agent.Autonomous.Rhythm
	switch kind {
	case "creative":
		last = &a.lastCreativeRhythm
		interval = time.Duration(cfg.CreativeEveryMs) * time.Millisecond
		if interval <= 0 {
			interval = 45 * time.Minute
		}
	case "review":
		last = &a.lastReviewRhythm
		interval = time.Duration(cfg.ReviewEveryMs) * time.Millisecond
		if interval <= 0 {
			interval = 90 * time.Minute
		}
	case "news":
		last = &a.lastNewsRhythm
		interval = time.Duration(cfg.NewsEveryMs) * time.Millisecond
		if interval <= 0 {
			interval = time.Hour
		}
	default:
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if *last == nil || now.Sub(**last) >= interval {
		copied := now
		*last = &copied
		return true
	}
	return false
}

func (a *AgentRuntime) unreadNewsCount() int {
	if cursor, ok := a.store.NewsFeedCursor("ithome"); ok {
		return a.store.CountNewsArticlesNewerThanCursor("ithome", cursor)
	}
	return a.store.CountNewsArticlesNewerThanCursor("ithome", db.NewsFeedCursor{})
}

func autonomousIdleDelay(cfg config.AutonomousConfig) time.Duration {
	delay := time.Duration(cfg.IdleDelayMs) * time.Millisecond
	if delay <= 0 {
		return 10 * time.Minute
	}
	return delay
}

func autonomousIdleWatchInterval(idleDelay time.Duration) time.Duration {
	if idleDelay <= 0 {
		return time.Minute
	}
	interval := idleDelay / 4
	if interval < 5*time.Second {
		return 5 * time.Second
	}
	if interval > time.Minute {
		return time.Minute
	}
	return interval
}

func (a *AgentRuntime) shouldQueueAutonomousIdleWake(now time.Time, idleDelay time.Duration) bool {
	if !a.cfg.Server.Agent.Autonomous.Enabled || idleDelay <= 0 || a.events.Count() > 0 {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.initialized || a.loopState != "idle" || a.autonomousPending || a.autonomousUntil != nil {
		return false
	}
	if a.lastActivity == nil {
		return true
	}
	return now.Sub(*a.lastActivity) >= idleDelay
}

func (a *AgentRuntime) runRootLoopOnce() {
	a.consumePendingEvents()
	a.runRootRound()
	a.markRootLoopIdle()
}

func (a *AgentRuntime) markRootLoopIdle() {
	a.mu.Lock()
	now := time.Now()
	a.loopState = "idle"
	a.lastActivity = &now
	a.mu.Unlock()
	a.persistSnapshot()
}

func (a *AgentRuntime) consumePendingEvents() bool {
	a.mu.Lock()
	a.loopState = "consuming_events"
	a.mu.Unlock()
	events := a.events.DequeueAll()
	a.store.Log("info", "Agent consuming events", map[string]any{"event": "agent.root.consume_events", "count": len(events)})
	shouldRunRoot := false
	hasExternalEvent := hasExternalAgentEvent(events)
	if hasExternalEvent {
		a.resetAutonomousRounds()
	}
	for _, event := range events {
		if event.Type == "wake" {
			reason := common.AsString(event.Data["reason"])
			switch reason {
			case "continue_after_tool":
				a.store.Log("info", "Agent internal continuation wake", map[string]any{"event": "agent.root.continue_after_tool"})
				shouldRunRoot = wakeTriggersRootRound(reason)
				continue
			case "wait_timeout":
				a.store.Log("info", "Agent cache keepalive wake", map[string]any{
					"event":      "agent.root.cache_keepalive",
					"intervalMs": a.cfg.Server.Agent.WaitToolMaxWaitMs,
				})
				shouldRunRoot = a.cfg.Server.Agent.CacheKeepaliveEnabled
				continue
			case "self_continuation":
				if hasExternalEvent {
					continue
				}
				afterCooldown, _ := event.Data["afterCooldown"].(bool)
				signal := rhythmSignalFromEvent(event)
				if a.prepareAutonomousRound(afterCooldown, signal) {
					a.store.Log("info", "Agent autonomous continuation wake", map[string]any{
						"event":            "agent.root.self_continuation",
						"rhythm":           signal.Kind,
						"consecutiveRound": a.autonomousRoundCount(),
						"maxConsecutive":   a.cfg.Server.Agent.Autonomous.MaxConsecutiveRounds,
						"afterCooldown":    afterCooldown,
					})
					shouldRunRoot = true
				}
				continue
			}
		}
		rendered := a.renderEventContext(event)
		a.store.AppendAgentStackItem(db.AgentStackItem{
			RuntimeKey: "root",
			Kind:       "runtime_input",
			Role:       eventMessageRole(event.Type),
			Content:    rendered,
			Metadata: map[string]any{
				"eventType": event.Type,
				"eventAt":   common.ISO(event.At),
			},
		})
		focused := true
		switch event.Type {
		case "napcat_group_message":
			focused = a.session.OnGroupMessage(
				common.AsString(event.Data["groupId"]),
				common.AsString(event.Data["userId"]),
				common.AsString(event.Data["nickname"]),
				common.AsString(event.Data["rawMessage"]),
				intValue(event.Data["messageSeq"]),
				intValue(event.Data["messageId"]),
				event.At,
			)
		case "napcat_private_message":
			focused = a.session.OnPrivateMessage(
				common.AsString(event.Data["userId"]),
				common.AsString(event.Data["nickname"]),
				common.AsString(event.Data["rawMessage"]),
				intValue(event.Data["messageSeq"]),
				intValue(event.Data["messageId"]),
				event.At,
			)
		case "news_article_ingested":
			focused = a.session.OnNewsArticle()
		case "story_recall_completed":
			focused = true
		}
		currentState := a.session.State()
		a.store.Log("info", "Agent event normalized", map[string]any{"event": "agent.root.event", "type": event.Type, "focused": focused, "state": currentState, "preview": trimPreview(rendered, 240)})
		a.appendContext(contextItem(eventContextKind(event.Type), event.Type, rendered))
		if event.Type == "wake" {
			continue
		}
		if focused {
			a.appendRootMessage(agentruntime.Message{Role: eventMessageRole(event.Type), Content: rendered})
			if event.Type == "napcat_group_message" || event.Type == "napcat_private_message" {
				a.store.AddStoryLedger("root", eventMessageRole(event.Type), rendered)
			}
			shouldRunRoot = true
		}
	}
	a.scheduleStoryBatch()
	return shouldRunRoot
}

func rhythmSignalFromEvent(event AgentEvent) DashboardRhythmSignal {
	actions := []string{}
	switch raw := event.Data["actions"].(type) {
	case []string:
		actions = append(actions, raw...)
	case []any:
		for _, item := range raw {
			if s := strings.TrimSpace(common.AsString(item)); s != "" {
				actions = append(actions, s)
			}
		}
	}
	kind := strings.TrimSpace(common.AsString(event.Data["rhythm"]))
	if kind == "" {
		kind = "quiet"
	}
	reason := strings.TrimSpace(common.AsString(event.Data["message"]))
	if reason == "" {
		reason = "外界暂时安静，可以选择做一小步自己的事，也可以 wait。"
	}
	return DashboardRhythmSignal{
		Kind:             kind,
		Reason:           reason,
		SuggestedActions: actions,
		CreatedAt:        common.ISO(event.At),
	}
}

func hasExternalAgentEvent(events []AgentEvent) bool {
	for _, event := range events {
		if event.Type != "wake" {
			return true
		}
	}
	return false
}

func (a *AgentRuntime) runRootRound() bool {
	if a.llm == nil || len(a.rootMessages) == 0 {
		a.store.Log("warn", "Root LLM round skipped", map[string]any{"event": "agent.root.llm.skipped", "hasLlm": a.llm != nil, "messageCount": len(a.rootMessages)})
		return false
	}
	a.mu.Lock()
	a.loopState = "calling_root_llm"
	a.mu.Unlock()
	a.appendWakeReminderIfNeeded()
	a.scheduleStoryRecall()
	messages, autonomous := a.rootRoundMessages()
	tools := rootTools(a.cfg, a.rootTools, a.session, a.events)
	tools.SetObserver(a)
	a.store.Log("info", "Root LLM round start", map[string]any{"event": "agent.root.llm.start", "round": 1, "messageCount": len(messages), "state": a.session.State(), "currentApp": a.session.App(), "exposedTools": toolDefinitionNames(tools.Definitions()), "availableActions": a.session.AvailableTools(), "autonomous": autonomous})
	result, err := a.rootKernel.RunRound(context.Background(), agentruntime.RoundInput{
		SystemPrompt: createSystemPrompt(a.cfg),
		Messages:     messages,
		Tools:        tools,
		ToolChoice:   "required",
	})
	if err != nil {
		a.store.Log("error", "Root LLM round failed", map[string]any{"event": "agent.root.llm.failed", "round": 1, "error": err.Error()})
		a.setRuntimeError(err)
		return false
	}
	staleRound := a.events.Count() > 0
	if staleRound {
		a.store.Log("info", "Root LLM round marked stale", map[string]any{"event": "agent.root.llm.stale", "pendingEventCount": a.events.Count()})
	}
	a.store.Log("info", "Root LLM response", map[string]any{
		"event":            "agent.root.llm.response",
		"round":            1,
		"provider":         result.Completion.Provider,
		"model":            result.Completion.Model,
		"assistant":        trimPreview(result.Assistant.Content, 500),
		"reasoningContent": trimPreview(result.Assistant.ReasoningContent, 1000),
		"toolCalls":        toolCallNames(result.Assistant.ToolCalls),
	})
	a.appendRootRoundStack(result)
	if shouldPersistAssistant(result.Assistant, result.ToolExecutions) {
		a.appendRootMessage(assistantForPersistence(result.Assistant, result.ToolExecutions))
		a.appendContext(contextItem("llm_message", "assistant", result.Assistant.Content))
	}
	for _, execution := range result.ToolExecutions {
		logMetadata := map[string]any{"event": "agent.root.tool.executed", "tool": execution.Call.Name}
		if message := toolCallMessage(execution.Call); message != "" {
			logMetadata["messagePreview"] = trimPreview(message, 240)
		}
		logMetadata["arguments"] = execution.Call.Arguments
		logMetadata["result"] = trimPreview(execution.Result.Content, 500)
		a.store.Log("info", "Root tool executed", logMetadata)
		if shouldPersistToolResult(execution) {
			a.appendRootMessage(agentruntime.Message{Role: "tool", ToolCallID: execution.Call.ID, Content: execution.Result.Content})
		}
		a.recordToolExecution(execution)
		for _, message := range a.postToolFocusMessages(execution) {
			a.appendRootMessage(message)
			a.appendContext(contextItem("system_instruction", "state_focus", message.Content))
		}
		if reminder := sentMessageReminder(execution); reminder != "" {
			a.appendRootMessage(agentruntime.Message{Role: "user", Content: reminder})
			a.appendContext(contextItem("system_reminder", "message_sent", reminder))
		}
	}
	a.recordLLMCall(result.Completion)
	a.maybeCompactRoot(result.Completion)
	if a.shouldContinueAfterTool(result.ToolExecutions) {
		return true
	}
	if len(result.ToolExecutions) == 0 && strings.TrimSpace(result.Assistant.Content) != "" {
		reason := "disabled"
		if staleRound {
			reason = "disabled_stale_round_pending_events"
		}
		a.store.Log("info", "Assistant content fallback skipped", map[string]any{"event": "agent.root.fallback_send.skipped", "reason": reason, "message": trimPreview(result.Assistant.Content, 300)})
		if !staleRound {
			a.appendRootMessage(agentruntime.Message{Role: "user", Content: prompts.AssistantActionRequiredReminder(result.Assistant.Content)})
			a.appendContext(contextItem("system_reminder", "assistant_action_required", prompts.AssistantActionRequiredReminder(result.Assistant.Content)))
			return true
		}
	}
	return false
}

func (a *AgentRuntime) BeforeTool(_ context.Context, call agentruntime.ToolCall, definition agentruntime.ToolDefinition, kind string) (*agentruntime.ToolResult, error) {
	sideEffect := toolCallHasSideEffect(call)
	execution, acquired, err := a.store.BeginToolExecution(db.ToolExecutionItem{
		ExecutionKey: call.ID,
		RuntimeKey:   "root",
		ToolCallID:   call.ID,
		ToolName:     resolvedToolCallName(call),
		Arguments:    call.Arguments,
		Status:       "processing",
		SideEffect:   sideEffect,
		LeaseOwner:   "root",
	}, 2*time.Minute)
	if err != nil {
		return nil, err
	}
	if acquired {
		return nil, nil
	}
	if execution.Status == "completed" {
		return &agentruntime.ToolResult{Kind: kind, Content: execution.Result}, nil
	}
	payload := map[string]any{
		"ok":         false,
		"error":      "TOOL_EXECUTION_BLOCKED",
		"tool":       definition.Name,
		"status":     execution.Status,
		"message":    execution.ErrorMessage,
		"sideEffect": execution.SideEffect,
	}
	data, _ := json.Marshal(payload)
	return &agentruntime.ToolResult{Kind: kind, Content: string(data)}, nil
}

func (a *AgentRuntime) AfterTool(_ context.Context, call agentruntime.ToolCall, _ agentruntime.ToolDefinition, result agentruntime.ToolResult, err error) {
	a.store.CompleteToolExecution(call.ID, result.Content, err)
}

func toolCallHasSideEffect(call agentruntime.ToolCall) bool {
	name := resolvedToolCallName(call)
	switch name {
	case "send_message", "bash", "browser", "searchMagnetFromWeb":
		return true
	case "todo_app", "novel_app", "project_app", "music_app", "news_app", "activity_app", "workspace_app":
		return personalAppActionHasSideEffect(name, invocationArguments(call.Arguments))
	default:
		return false
	}
}

func personalAppActionHasSideEffect(tool string, args map[string]any) bool {
	action := strings.TrimSpace(common.AsString(args["action_text"]))
	if action == "" {
		action = strings.TrimSpace(common.AsString(args["subaction"]))
	}
	if action == "" {
		action = strings.TrimSpace(common.AsString(args["operation"]))
	}
	if action == "" {
		action = strings.TrimSpace(common.AsString(args["op"]))
	}
	if action == "" {
		action = strings.TrimSpace(common.AsString(args["action"]))
	}
	switch tool {
	case "todo_app":
		switch action {
		case "add", "update", "complete", "remove":
			return true
		}
	case "novel_app":
		switch action {
		case "create_project", "append_draft", "append_note", "update_outline", "add_todo", "complete_todo":
			return true
		}
	case "project_app":
		switch action {
		case "create", "append_note", "append_journal":
			return true
		}
	case "music_app":
		switch action {
		case "add", "set_current", "save_impression", "finish", "drop":
			return true
		}
	case "news_app":
		return action == "save_takeaway"
	case "activity_app":
		return action == "start" || action == "finish"
	case "workspace_app":
		return action == "write"
	}
	return false
}

func toolCallMessage(call agentruntime.ToolCall) string {
	if resolvedToolCallName(call) != "send_message" {
		return ""
	}
	args := call.Arguments
	if call.Name == "invoke" {
		args = invocationArguments(call.Arguments)
	}
	return strings.TrimSpace(common.AsString(args["message"]))
}

func resolvedToolCallName(call agentruntime.ToolCall) string {
	if call.Name == "act" {
		if action := strings.TrimSpace(common.AsString(call.Arguments["action"])); action != "" {
			switch action {
			case "send", "sendMessage", "send_group_message", "send_private_message":
				return "send_message"
			case "searchMagnet", "search_magnet", "magnet_search":
				return "searchMagnetFromWeb"
			case "open_ithome", "ithome", "open_article":
				return "open_ithome_article"
			case "ai_tone", "detectAI":
				return "detect_ai_tone"
			default:
				return action
			}
		}
		if strings.TrimSpace(common.AsString(call.Arguments["message"])) != "" {
			return "send_message"
		}
		return call.Name
	}
	if call.Name != "invoke" {
		return call.Name
	}
	if name := invocationToolName(call.Arguments); name != "" {
		return name
	}
	return call.Name
}

func (a *AgentRuntime) appendRootRoundStack(result agentruntime.RoundResult) {
	if strings.TrimSpace(result.Assistant.Content) != "" || strings.TrimSpace(result.Assistant.ReasoningContent) != "" {
		a.store.AppendAgentStackItem(db.AgentStackItem{
			RuntimeKey: "root",
			Kind:       "assistant_output",
			Role:       "assistant",
			Content: map[string]any{
				"content":          result.Assistant.Content,
				"reasoningContent": result.Assistant.ReasoningContent,
			},
			Metadata: map[string]any{"provider": result.Completion.Provider, "model": result.Completion.Model},
		})
	}
	for _, call := range result.Assistant.ToolCalls {
		a.store.AppendAgentStackItem(db.AgentStackItem{
			RuntimeKey: "root",
			Kind:       "function_call",
			Role:       "assistant",
			ToolCallID: call.ID,
			ToolName:   resolvedToolCallName(call),
			Content:    call.Arguments,
		})
	}
	for _, execution := range result.ToolExecutions {
		a.store.AppendAgentStackItem(db.AgentStackItem{
			RuntimeKey: "root",
			Kind:       "function_call_output",
			Role:       "tool",
			ToolCallID: execution.Call.ID,
			ToolName:   resolvedToolCallName(execution.Call),
			Content:    execution.Result.Content,
		})
	}
}

func (a *AgentRuntime) rootRoundMessages() ([]agentruntime.Message, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	messages := append([]agentruntime.Message(nil), a.rootMessages...)
	if !a.autonomousPending {
		return messages, false
	}
	reminder := strings.TrimSpace(a.autonomousReminder)
	if reminder == "" {
		reminder = prompts.SelfContinuationReminder()
	}
	messages = append(messages, agentruntime.Message{Role: "user", Content: reminder})
	a.autonomousPending = false
	a.autonomousReminder = ""
	return messages, true
}

func (a *AgentRuntime) prepareAutonomousRound(afterCooldown bool, signal DashboardRhythmSignal) bool {
	if !a.cfg.Server.Agent.Autonomous.Enabled {
		return false
	}
	maxRounds := a.cfg.Server.Agent.Autonomous.MaxConsecutiveRounds
	if maxRounds <= 0 {
		maxRounds = 4
	}
	a.mu.Lock()
	if afterCooldown {
		a.autonomousTimer = nil
		a.autonomousUntil = nil
	}
	allowed, nextRounds := autonomousRoundDecision(a.autonomousRounds, maxRounds, afterCooldown)
	if !allowed {
		a.mu.Unlock()
		a.scheduleAutonomousCooldown()
		return false
	}
	a.autonomousRounds = nextRounds
	a.autonomousPending = true
	reminder := prompts.RhythmContinuationReminder(signal.Kind, signal.Reason, signal.SuggestedActions)
	a.autonomousReminder = reminder
	a.lastRhythmSignal = &signal
	a.mu.Unlock()
	a.appendContext(contextItem("rhythm_signal", signal.Kind, reminder))
	return true
}

func autonomousRoundDecision(current, max int, afterCooldown bool) (bool, int) {
	if max <= 0 {
		max = 1
	}
	if afterCooldown {
		current = 0
	}
	if current >= max {
		return false, current
	}
	return true, current + 1
}

func (a *AgentRuntime) autonomousRoundCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.autonomousRounds
}

func (a *AgentRuntime) scheduleAutonomousCooldown() {
	cooldown := time.Duration(a.cfg.Server.Agent.Autonomous.CooldownMs) * time.Millisecond
	if cooldown <= 0 {
		cooldown = 5 * time.Minute
	}
	a.mu.Lock()
	if a.autonomousTimer != nil {
		a.mu.Unlock()
		return
	}
	until := time.Now().Add(cooldown)
	a.autonomousUntil = &until
	a.autonomousTimer = time.AfterFunc(cooldown, func() {
		a.events.Enqueue(AgentEvent{
			Type: "wake",
			Data: map[string]any{"reason": "self_continuation", "afterCooldown": true},
		})
	})
	a.mu.Unlock()
	a.store.Log("info", "Agent autonomous continuation cooling down", map[string]any{
		"event":      "agent.root.self_continuation.cooldown",
		"cooldownMs": int(cooldown.Milliseconds()),
		"until":      common.ISO(until),
	})
}

func (a *AgentRuntime) resetAutonomousRounds() {
	a.mu.Lock()
	if a.autonomousTimer != nil {
		a.autonomousTimer.Stop()
		a.autonomousTimer = nil
	}
	a.autonomousRounds = 0
	a.autonomousPending = false
	a.autonomousReminder = ""
	a.autonomousUntil = nil
	a.mu.Unlock()
}

func hasToolExecution(executions []agentruntime.ToolExecution, name string) bool {
	for _, execution := range executions {
		if resolvedToolCallName(execution.Call) == name {
			return true
		}
	}
	return false
}

func shouldContinueAfterTool(executions []agentruntime.ToolExecution) bool {
	if len(executions) == 0 || hasToolExecution(executions, "wait") {
		return false
	}
	allSuccessfulSends := true
	for _, execution := range executions {
		if isPersonalAppTool(resolvedToolCallName(execution.Call)) && toolResultHasError(execution.Result.Content) {
			return false
		}
		if personalAppActionHasSideEffect(resolvedToolCallName(execution.Call), invocationArguments(execution.Call.Arguments)) && !toolResultHasError(execution.Result.Content) {
			return false
		}
		if resolvedToolCallName(execution.Call) != "send_message" || toolResultHasError(execution.Result.Content) {
			allSuccessfulSends = false
		}
		var payload map[string]any
		if json.Unmarshal([]byte(execution.Result.Content), &payload) != nil {
			continue
		}
		switch common.AsString(payload["error"]) {
		case "AI_TONE_TOO_HIGH", "UNKNOWN_TOOL", "INVOKE_TOOL_NOT_FOUND":
			return false
		}
	}
	if allSuccessfulSends {
		return false
	}
	return true
}

func (a *AgentRuntime) shouldContinueAfterTool(executions []agentruntime.ToolExecution) bool {
	if !shouldContinueAfterTool(executions) {
		a.setLastReadOnlyToolSignature("")
		a.recordToolGuardEvent(toolGuardEventForStop(executions))
		return false
	}
	sig, ok := readOnlyPersonalAppSignature(executions)
	if !ok {
		a.setLastReadOnlyToolSignature("")
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lastReadOnlyToolSig == sig {
		a.lastToolGuardEvent = &DashboardToolGuardEvent{
			Rule:      "duplicate_read_only_personal_tool",
			Tool:      resolvedToolCallName(executions[0].Call),
			Signature: sig,
			Message:   "连续重复读取同一个个人工作台状态，已停止续轮，避免 screen/list 空转。",
			UpdatedAt: common.ISO(time.Now()),
		}
		return false
	}
	a.lastReadOnlyToolSig = sig
	return true
}

func (a *AgentRuntime) recordToolGuardEvent(event *DashboardToolGuardEvent) {
	if event == nil {
		return
	}
	a.mu.Lock()
	a.lastToolGuardEvent = event
	a.mu.Unlock()
}

func toolGuardEventForStop(executions []agentruntime.ToolExecution) *DashboardToolGuardEvent {
	if len(executions) == 0 {
		return nil
	}
	now := common.ISO(time.Now())
	for _, execution := range executions {
		name := resolvedToolCallName(execution.Call)
		if toolResultHasError(execution.Result.Content) {
			return &DashboardToolGuardEvent{Rule: "tool_error_stops_retry", Tool: name, Message: "工具返回错误，停止自动续轮，避免原样重试。", UpdatedAt: now}
		}
		if personalAppActionHasSideEffect(name, invocationArguments(execution.Call.Arguments)) {
			return &DashboardToolGuardEvent{Rule: "personal_write_stops_round", Tool: name, Message: "个人工作台写入已完成，本轮停止，避免每一步都汇报。", UpdatedAt: now}
		}
	}
	return nil
}

func (a *AgentRuntime) setLastReadOnlyToolSignature(sig string) {
	a.mu.Lock()
	a.lastReadOnlyToolSig = sig
	a.mu.Unlock()
}

func readOnlyPersonalAppSignature(executions []agentruntime.ToolExecution) (string, bool) {
	if len(executions) != 1 {
		return "", false
	}
	execution := executions[0]
	name := resolvedToolCallName(execution.Call)
	if !isPersonalAppTool(name) || toolResultHasError(execution.Result.Content) {
		return "", false
	}
	args := invocationArguments(execution.Call.Arguments)
	action := strings.TrimSpace(common.AsString(args["action_text"]))
	if action == "" {
		action = strings.TrimSpace(common.AsString(args["subaction"]))
	}
	if action == "" {
		action = strings.TrimSpace(common.AsString(args["op"]))
	}
	if action == "" {
		action = strings.TrimSpace(common.AsString(args["action"]))
	}
	if action == "" && name == "personal_screen" {
		action = "screen"
	}
	if action == "" {
		return "", false
	}
	switch action {
	case "screen", "list", "list_projects", "open_project", "overview":
		payload, _ := json.Marshal(map[string]any{
			"name":      name,
			"action":    action,
			"app":       common.AsString(args["app"]),
			"projectId": common.AsString(args["projectId"]),
		})
		return string(payload), true
	default:
		return "", false
	}
}

func isPersonalAppTool(name string) bool {
	switch name {
	case "personal_screen", "todo_app", "novel_app", "project_app", "music_app", "news_app", "activity_app", "workspace_app":
		return true
	default:
		return false
	}
}

func toolResultHasError(content string) bool {
	var payload map[string]any
	return json.Unmarshal([]byte(content), &payload) == nil && strings.TrimSpace(common.AsString(payload["error"])) != ""
}

func shouldPersistAssistant(message agentruntime.Message, executions []agentruntime.ToolExecution) bool {
	if len(message.ToolCalls) == 0 && len(executions) == 0 {
		return false
	}
	persisted := assistantForPersistence(message, executions)
	if isPlainWaitContent(persisted.Content) && len(persisted.ToolCalls) == 0 {
		return false
	}
	return strings.TrimSpace(persisted.Content) != "" || len(persisted.ToolCalls) > 0
}

func isPlainWaitContent(content string) bool {
	content = strings.TrimSpace(strings.ToLower(content))
	content = strings.Trim(content, "。.!！ \t\r\n")
	return content == "wait"
}

func assistantForPersistence(message agentruntime.Message, executions []agentruntime.ToolExecution) agentruntime.Message {
	message.ReasoningContent = ""
	if len(message.ToolCalls) > 0 {
		message.Content = ""
	}
	drop := map[string]bool{}
	for _, execution := range executions {
		if !shouldPersistToolResult(execution) {
			drop[execution.Call.ID] = true
		}
	}
	if len(drop) == 0 {
		return message
	}
	out := message
	out.ToolCalls = nil
	for _, call := range message.ToolCalls {
		if !drop[call.ID] {
			out.ToolCalls = append(out.ToolCalls, call)
		}
	}
	return out
}

func shouldPersistToolResult(execution agentruntime.ToolExecution) bool {
	switch toolResultError(execution.Result.Content) {
	case "AI_TONE_TOO_HIGH", "UNKNOWN_TOOL", "INVOKE_TOOL_NOT_FOUND", "INVOKE_TOOL_NOT_AVAILABLE":
		return false
	}
	if resolvedToolCallName(execution.Call) == "enter" && toolResultOK(execution.Result.Content) {
		return true
	}
	return execution.Result.Kind != "control"
}

func toolResultError(content string) string {
	var payload map[string]any
	if json.Unmarshal([]byte(content), &payload) != nil {
		return ""
	}
	return strings.TrimSpace(common.AsString(payload["error"]))
}

func (a *AgentRuntime) appendWakeReminderIfNeeded() {
	now := time.Now()
	a.mu.Lock()
	if a.lastWakeReminderAt != nil && sameMinute(*a.lastWakeReminderAt, now) {
		a.mu.Unlock()
		return
	}
	a.lastWakeReminderAt = &now
	a.mu.Unlock()
	a.appendRootMessage(agentruntime.Message{Role: "user", Content: prompts.WakeReminder(now)})
	a.appendContext(contextItem("system_reminder", "wake", prompts.WakeReminder(now)))
}

func sameMinute(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day() && a.Hour() == b.Hour() && a.Minute() == b.Minute()
}

func (a *AgentRuntime) scheduleStoryRecall() {
	a.mu.Lock()
	if a.storyRecallRunning {
		a.mu.Unlock()
		return
	}
	a.storyRecallRunning = true
	a.mu.Unlock()
	go func() {
		defer func() {
			a.mu.Lock()
			a.storyRecallRunning = false
			a.mu.Unlock()
		}()
		a.triggerStoryRecallIfNeeded()
	}()
}

func (a *AgentRuntime) triggerStoryRecallIfNeeded() {
	a.mu.Lock()
	messageCount := len(a.rootMessages)
	if messageCount == 0 || a.lastStoryRecallAt == messageCount {
		a.mu.Unlock()
		return
	}
	messages := append([]agentruntime.Message(nil), a.rootMessages...)
	a.lastStoryRecallAt = messageCount
	query := latestStoryRecallQuery(messages)
	if query == "" || query == a.lastStoryRecallKey {
		a.mu.Unlock()
		return
	}
	a.lastStoryRecallKey = query
	a.mu.Unlock()
	searchTool, ok := a.rootTools.Get("search_memory")
	if !ok {
		return
	}
	a.mu.Lock()
	stale := latestStoryRecallQuery(a.rootMessages) != query
	a.mu.Unlock()
	if stale {
		a.store.Log("info", "Story recall skipped", map[string]any{"event": "agent.story_recall.skipped", "reason": "stale_query"})
		return
	}
	topK := a.cfg.Server.Agent.Story.Recall.TopK
	if topK <= 0 {
		topK = 2
	}
	recallArgs := map[string]any{"query": query, "limit": topK}
	result, err := searchTool.Execute(context.Background(), agentruntime.ToolCall{ID: common.NewID() + ":story-recall", Name: "search_memory", Arguments: recallArgs})
	if err != nil {
		a.store.Log("warn", "Story recall search failed", map[string]any{"event": "agent.story_recall.search_failed", "error": err.Error()})
		return
	}
	content := strings.TrimSpace(result.Content)
	if content == "" || content == "[]" || content == "null" {
		return
	}
	filtered := a.filterNewStoryRecallContent(content)
	if filtered == "" {
		return
	}
	a.events.Enqueue(AgentEvent{Type: "story_recall_completed", Data: map[string]any{"content": filtered}})
	a.store.Log("info", "Story recall enqueued", map[string]any{"event": "agent.story_recall.enqueued", "query": trimPreview(query, 160), "preview": trimPreview(filtered, 300)})
}

func latestStoryRecallQuery(messages []agentruntime.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		content := common.AsString(messages[i].Content)
		start := strings.LastIndex(content, "<qq_message")
		if start < 0 {
			continue
		}
		openEnd := strings.Index(content[start:], ">")
		if openEnd < 0 {
			continue
		}
		bodyStart := start + openEnd + 1
		closeOffset := strings.Index(content[bodyStart:], "</qq_message>")
		if closeOffset < 0 {
			continue
		}
		query := strings.Join(strings.Fields(content[bodyStart:bodyStart+closeOffset]), " ")
		runes := []rune(query)
		if len(runes) > 500 {
			query = string(runes[len(runes)-500:])
		}
		return query
	}
	return ""
}

func (a *AgentRuntime) filterNewStoryRecallContent(content string) string {
	var items []map[string]any
	if err := json.Unmarshal([]byte(content), &items); err != nil {
		return content
	}
	fresh := []map[string]any{}
	threshold := a.cfg.Server.Agent.Story.Recall.ScoreThreshold
	a.mu.Lock()
	if a.injectedStoryIDs == nil {
		a.injectedStoryIDs = map[string]bool{}
	}
	for _, item := range items {
		if threshold > 0 {
			score, ok := floatValue(item["score"])
			if !ok || score < threshold {
				continue
			}
		}
		id := common.AsString(item["id"])
		if id != "" && a.injectedStoryIDs[id] {
			continue
		}
		if id != "" {
			a.injectedStoryIDs[id] = true
		}
		fresh = append(fresh, item)
	}
	a.mu.Unlock()
	if len(fresh) == 0 {
		return ""
	}
	data, _ := json.Marshal(fresh)
	return string(data)
}

func (a *AgentRuntime) postToolFocusMessages(execution agentruntime.ToolExecution) []agentruntime.Message {
	switch resolvedToolCallName(execution.Call) {
	case "enter":
		if !toolResultOK(execution.Result.Content) {
			return nil
		}
		return a.focusMessagesForState(enteredStateID(invocationArguments(execution.Call.Arguments)))
	case "back":
		if !toolResultOK(execution.Result.Content) {
			return nil
		}
		return a.focusMessagesForState(a.session.State())
	case "invoke":
		toolName := invocationToolName(execution.Call.Arguments)
		if toolName == "open_ithome_article" && toolResultOK(execution.Result.Content) {
			if message, ok := a.ithomeArticleDetailMessage(invocationArguments(execution.Call.Arguments)); ok {
				return []agentruntime.Message{message}
			}
		}
	case "open_ithome_article":
		if toolResultOK(execution.Result.Content) {
			if message, ok := a.ithomeArticleDetailMessage(execution.Call.Arguments); ok {
				return []agentruntime.Message{message}
			}
		}
	}
	return nil
}

func (a *AgentRuntime) ithomeFocusMessages() []agentruntime.Message {
	limit := a.cfg.Server.News.Ithome.RecentArticleLimit
	if limit <= 0 {
		limit = 10
	}
	cursor, hasCursor := a.store.NewsFeedCursor("ithome")
	isNewMode := false
	hiddenNewCount := 0
	var articles []db.NewsArticle
	if hasCursor {
		totalNew := a.store.CountNewsArticlesNewerThanCursor("ithome", cursor)
		if totalNew > 0 {
			isNewMode = true
			articles = a.store.ListNewsArticlesNewerThanCursor("ithome", cursor, limit)
			hiddenNewCount = totalNew - len(articles)
			if hiddenNewCount < 0 {
				hiddenNewCount = 0
			}
		}
	}
	if len(articles) == 0 {
		articles = a.store.ListNewsArticlesLatest("ithome", limit)
	}
	if len(articles) > 0 {
		a.store.UpsertNewsFeedCursor("ithome", articles[0].ID, articles[0].PublishedAt)
	}
	summaries := make([]prompts.ArticleSummary, 0, len(articles))
	for _, article := range articles {
		summaries = append(summaries, prompts.ArticleSummary{
			ID:              article.ID,
			Title:           article.Title,
			PublishedAtText: formatTime(article.PublishedAt),
			URL:             article.URL,
			RSSSummary:      article.RSSSummary,
		})
	}
	content := prompts.ITHomeArticleListInstruction("IT 之家", isNewMode, hiddenNewCount, summaries)
	return []agentruntime.Message{{Role: "user", Content: content}}
}

func (a *AgentRuntime) focusMessagesForState(stateID string) []agentruntime.Message {
	messages := []agentruntime.Message{a.stateReminderMessage(stateID)}
	messages = append(messages, a.stateOnFocusMessages(stateID)...)
	return messages
}

func (a *AgentRuntime) focusMessagesForStateLocked(stateID string) []agentruntime.Message {
	if stateID == "main" {
		return nil
	}
	return []agentruntime.Message{a.stateReminderMessage(stateID)}
}

func (a *AgentRuntime) stateOnFocusMessages(stateID string) []agentruntime.Message {
	switch {
	case isPersonalAppState(stateID):
		return a.personalAppFocusMessages(stateID)
	case stateID == "portal":
		return nil
	case stateID == "ithome":
		return a.ithomeFocusMessages()
	case strings.HasPrefix(stateID, "qq_group:"), strings.HasPrefix(stateID, "qq_private:"):
		return a.chatFocusMessages(stateID)
	default:
		return nil
	}
}

func isPersonalAppState(stateID string) bool {
	switch stateID {
	case "todo", "novel", "projects", "browser", "music", "news":
		return true
	default:
		return false
	}
}

func (a *AgentRuntime) personalAppFocusMessages(appID string) []agentruntime.Message {
	if a.personal == nil {
		return nil
	}
	screen, err := a.personal.Screen(appID)
	if err != nil {
		return []agentruntime.Message{{Role: "user", Content: "<app_screen app=\"" + appID + "\">\n{\"ok\":false,\"error\":\"" + err.Error() + "\"}\n</app_screen>"}}
	}
	data, _ := json.MarshalIndent(screen, "", "  ")
	return []agentruntime.Message{{Role: "user", Content: "<app_screen app=\"" + appID + "\">\n" + string(data) + "\n</app_screen>"}}
}

func (a *AgentRuntime) stateReminderMessage(stateID string) agentruntime.Message {
	snapshot := a.session.Snapshot()
	displayName := common.AsString(snapshot["focusedStateDisplayName"])
	if stateID != a.session.State() {
		displayName = stateDisplayNameFromSessionSnapshot(snapshot, stateID)
	}
	if displayName == "" {
		displayName = stateID
	}
	children := []prompts.StateReminderChild{}
	if stateID == a.session.State() {
		if items, ok := snapshot["children"].([]roottools.ChildState); ok {
			for _, child := range items {
				children = append(children, prompts.StateReminderChild{
					ID:          child.ID,
					DisplayName: child.DisplayName,
					Description: child.Description,
				})
			}
		}
	}
	apps := []prompts.StateReminderApp(nil)
	if stateID == "portal" {
		apps = []prompts.StateReminderApp{
			{ID: "calc", DisplayName: "计算器"},
			{ID: "terminal", DisplayName: "终端"},
		}
	}
	return agentruntime.Message{Role: "user", Content: prompts.StateSystemReminder(displayName, children, apps)}
}

func stateDisplayNameFromSessionSnapshot(snapshot map[string]any, stateID string) string {
	if stack, ok := snapshot["stateStack"].([]map[string]string); ok {
		for _, item := range stack {
			if item["id"] == stateID {
				return item["displayName"]
			}
		}
	}
	if children, ok := snapshot["children"].([]roottools.ChildState); ok {
		for _, child := range children {
			if child.ID == stateID {
				return child.DisplayName
			}
		}
	}
	return ""
}

func (a *AgentRuntime) chatFocusMessages(stateID string) []agentruntime.Message {
	if groupID := strings.TrimPrefix(stateID, "qq_group:"); groupID != stateID {
		if messages := a.session.ConsumeGroupFocusMessages(groupID); len(messages) > 0 {
			return groupUnreadFocusMessages(messages)
		}
	}
	if userID := strings.TrimPrefix(stateID, "qq_private:"); userID != stateID {
		if messages, useRecent := a.session.ConsumePrivateFocusMessages(userID); !useRecent {
			return privateUnreadFocusMessages(messages)
		}
	}
	limit := a.cfg.Server.Napcat.StartupContextRecentMessageCount
	if limit <= 0 {
		limit = 20
	}
	data := a.store.Snapshot()
	selected := make([]db.NapcatMessageItem, 0, limit)
	for _, item := range data.NapcatMessages {
		if !messageBelongsToState(item, stateID) {
			continue
		}
		selected = append(selected, item)
		if len(selected) > limit {
			selected = selected[1:]
		}
	}
	if len(selected) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("<system_reminder>\nRecent chat context after entering this chat:\n")
	for i, item := range selected {
		if i > 0 {
			b.WriteString("\n")
		}
		nickname := common.AsString(item.Nickname)
		if nickname == "" {
			nickname = stringPtrValue(item.Nickname)
		}
		if nickname == "" {
			nickname = "unknown"
		}
		raw := strings.TrimSpace(common.AsString(item.RawMessage))
		fmt.Fprintf(&b, "%s (%s):\n%s\n", nickname, stringPtrValue(item.UserID), raw)
	}
	b.WriteString("</system_reminder>")
	return []agentruntime.Message{{Role: "user", Content: b.String()}}
}

func groupUnreadFocusMessages(messages []roottools.GroupUnreadMessage) []agentruntime.Message {
	if len(messages) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("<system_reminder>\nUnread group messages:\n")
	for i, item := range messages {
		if i > 0 {
			b.WriteString("\n")
		}
		nickname := strings.TrimSpace(item.Nickname)
		if nickname == "" {
			nickname = "unknown"
		}
		fmt.Fprintf(&b, "%s (%s):\n%s\n", nickname, item.UserID, strings.TrimSpace(item.RawMessage))
	}
	b.WriteString("</system_reminder>")
	return []agentruntime.Message{{Role: "user", Content: b.String()}}
}

func privateUnreadFocusMessages(messages []roottools.PrivateUnreadMessage) []agentruntime.Message {
	if len(messages) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("<system_reminder>\nUnread private messages:\n")
	for i, item := range messages {
		if i > 0 {
			b.WriteString("\n")
		}
		nickname := strings.TrimSpace(item.Nickname)
		if nickname == "" {
			nickname = "unknown"
		}
		fmt.Fprintf(&b, "%s (%s):\n%s\n", nickname, item.UserID, strings.TrimSpace(item.RawMessage))
	}
	b.WriteString("</system_reminder>")
	return []agentruntime.Message{{Role: "user", Content: b.String()}}
}
func (a *AgentRuntime) ithomeArticleDetailMessage(args map[string]any) (agentruntime.Message, bool) {
	articleID := intValue(args["articleId"])
	if articleID == 0 {
		return agentruntime.Message{}, false
	}
	for _, article := range a.store.Snapshot().NewsArticles {
		if article.ID != articleID {
			continue
		}
		content := strings.TrimSpace(article.Content)
		fallbackToSummary := false
		if content == "" {
			content = strings.TrimSpace(article.RSSSummary)
			fallbackToSummary = true
		}
		maxChars := a.cfg.Server.News.Ithome.ArticleMaxChars
		if maxChars <= 0 {
			maxChars = 8000
		}
		truncated := false
		if len([]rune(content)) > maxChars {
			runes := []rune(content)
			content = string(runes[:maxChars])
			truncated = true
		}
		return agentruntime.Message{Role: "user", Content: prompts.ITHomeArticleDetail(article.Title, formatTime(article.PublishedAt), article.URL, content, fallbackToSummary, truncated, maxChars)}, true
	}
	return agentruntime.Message{}, false
}

func enteredStateID(args map[string]any) string {
	kind := common.AsString(args["kind"])
	id := common.AsString(args["id"])
	if id == "" {
		id = common.AsString(args["stateId"])
	}
	if id == "" {
		id = common.AsString(args["app"])
	}
	if id == "" {
		id = common.AsString(args["appId"])
	}
	if id == "" {
		id = common.AsString(args["target"])
	}
	if id == "" {
		id = common.AsString(args["query"])
	}
	if id == "" {
		id = common.AsString(args["message"])
	}
	switch kind {
	case "qq_group":
		if id == "" || strings.HasPrefix(id, "qq_group:") {
			return id
		}
		return "qq_group:" + id
	case "qq_private":
		if id == "" || strings.HasPrefix(id, "qq_private:") {
			return id
		}
		return "qq_private:" + id
	case "ithome":
		return kind
	default:
		return id
	}
}

func messageBelongsToState(item db.NapcatMessageItem, stateID string) bool {
	if groupID := strings.TrimPrefix(stateID, "qq_group:"); groupID != stateID {
		return item.MessageType == "group" && stringPtrValue(item.GroupID) == groupID
	}
	if userID := strings.TrimPrefix(stateID, "qq_private:"); userID != stateID {
		return item.MessageType == "private" && stringPtrValue(item.UserID) == userID
	}
	return false
}

func invocationToolName(args map[string]any) string {
	toolName := common.AsString(args["tool"])
	if toolName == "" {
		toolName = common.AsString(args["toolName"])
	}
	return toolName
}

func invocationArguments(args map[string]any) map[string]any {
	nested, _ := args["arguments"].(map[string]any)
	if nested != nil {
		return nested
	}
	if raw := common.AsString(args["arguments"]); strings.TrimSpace(raw) != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil && parsed != nil {
			return parsed
		}
	}
	out := map[string]any{}
	for key, value := range args {
		if key == "tool" || key == "toolName" || key == "arguments" {
			continue
		}
		out[key] = value
	}
	return out
}

func toolResultOK(content string) bool {
	var data map[string]any
	if err := json.Unmarshal([]byte(content), &data); err != nil {
		return true
	}
	ok, exists := data["ok"]
	if !exists {
		return true
	}
	value, _ := ok.(bool)
	return value
}

func toolResultString(content, key string) string {
	var data map[string]any
	if err := json.Unmarshal([]byte(content), &data); err != nil {
		return ""
	}
	return common.AsString(data[key])
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func timePtrValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func sentMessageReminder(execution agentruntime.ToolExecution) string {
	if resolvedToolCallName(execution.Call) != "send_message" || toolResultHasError(execution.Result.Content) {
		return ""
	}
	args := invocationArguments(execution.Call.Arguments)
	message := strings.TrimSpace(common.AsString(args["message"]))
	imagePath := strings.TrimSpace(common.AsString(args["imagePath"]))
	if message == "" && imagePath != "" {
		message = "[图片]"
	}
	if message == "" {
		return ""
	}
	return fmt.Sprintf("<system_reminder>已发送消息：%s</system_reminder>", message)
}
func (a *AgentRuntime) maybeCompactRoot(completion agentruntime.Completion) {
	if completion.Usage == nil {
		return
	}
	a.mu.Lock()
	messages := append([]agentruntime.Message(nil), a.rootMessages...)
	a.mu.Unlock()
	plan, ok := createCompactionPlan(messages, completion.Usage.TotalTokens, a.cfg.Server.Agent.ContextCompactionTotalTokenThreshold)
	if !ok {
		return
	}
	summary, err := a.summarizeWithRetry(createSystemPrompt(a.cfg), plan.summarize, prompts.RootContextSummaryReminder())
	if err != nil {
		a.setRuntimeError(err)
		return
	}
	if strings.TrimSpace(summary) == "" {
		return
	}
	now := time.Now()
	a.mu.Lock()
	a.rootMessages = append([]agentruntime.Message{{Role: "user", Content: prompts.ConversationSummary(summary)}}, plan.keep...)
	a.lastCompaction = &now
	a.contextItems = append(a.contextItems, contextItem("system_reminder", "root_compaction", prompts.ConversationSummary(summary)))
	a.mu.Unlock()
	a.store.AppendAgentStackItem(db.AgentStackItem{
		RuntimeKey: "root",
		Kind:       "memory_event",
		Role:       "user",
		Content: map[string]any{
			"summary":            summary,
			"summarizedMessages": len(plan.summarize),
			"retainedMessages":   len(plan.keep),
		},
		Metadata: map[string]any{"event": "root_compaction"},
	})
}

func (a *AgentRuntime) maybeCompactStory(completion agentruntime.Completion) {
	if completion.Usage == nil {
		return
	}
	a.mu.Lock()
	messages := append([]agentruntime.Message(nil), a.storyMessages...)
	a.mu.Unlock()
	plan, ok := createCompactionPlan(messages, completion.Usage.TotalTokens, a.cfg.Server.Agent.Story.ContextCompactionTotalTokenThreshold)
	if !ok {
		return
	}
	summary, err := a.summarizeWithRetry(prompts.StoryAgentSystemPrompt(), plan.summarize, prompts.StoryContextSummaryReminder())
	if err != nil {
		a.setRuntimeError(err)
		return
	}
	if strings.TrimSpace(summary) == "" {
		return
	}
	now := time.Now()
	a.mu.Lock()
	a.storyMessages = append([]agentruntime.Message{{Role: "user", Content: prompts.ConversationSummary(summary)}}, plan.keep...)
	a.lastCompaction = &now
	a.mu.Unlock()
}

func (a *AgentRuntime) summarizeWithRetry(systemPrompt string, messages []agentruntime.Message, reminder string) (string, error) {
	backoff := time.Duration(a.cfg.Server.Agent.LLMRetryBackoffMs) * time.Millisecond
	if backoff <= 0 {
		backoff = time.Second
	}
	for {
		summary, err := a.summarizer.Summarize(context.Background(), systemPrompt, messages, reminder)
		if err != nil {
			a.store.Log("warn", "Context summary failed; scheduling retry", map[string]any{"event": "agent.context_summary.retry_scheduled", "retryBackoffMs": int(backoff / time.Millisecond), "error": err.Error()})
			time.Sleep(backoff)
			continue
		}
		return summary, nil
	}
}

type compactPlan struct {
	summarize []agentruntime.Message
	keep      []agentruntime.Message
}

func createCompactionPlan(messages []agentruntime.Message, totalTokens, totalTokenThreshold int) (compactPlan, bool) {
	if len(messages) == 0 || totalTokens <= totalTokenThreshold {
		return compactPlan{}, false
	}
	keepCount := calculateCompactionKeepCount(len(messages))
	if keepCount == 0 {
		return compactPlan{}, false
	}
	cut := len(messages) - keepCount
	cut = extendCompactionCutIndexForAssistantToolBoundary(messages, cut)
	if cut <= 0 || cut >= len(messages) {
		return compactPlan{}, false
	}
	return compactPlan{summarize: append([]agentruntime.Message(nil), messages[:cut]...), keep: append([]agentruntime.Message(nil), messages[cut:]...)}, true
}

func calculateCompactionKeepCount(totalMessageCount int) int {
	if totalMessageCount <= 1 {
		return 0
	}
	return (totalMessageCount + 9) / 10
}

func extendCompactionCutIndexForAssistantToolBoundary(messages []agentruntime.Message, cutIndex int) int {
	if cutIndex <= 0 || cutIndex >= len(messages) {
		return cutIndex
	}
	previous := messages[cutIndex-1]
	if previous.Role != "assistant" || len(previous.ToolCalls) == 0 {
		return cutIndex
	}
	ids := map[string]bool{}
	for _, call := range previous.ToolCalls {
		ids[call.ID] = true
	}
	last := -1
	for i := cutIndex; i < len(messages); i++ {
		if messages[i].Role == "tool" && ids[messages[i].ToolCallID] {
			last = i
		}
	}
	if last >= 0 {
		return last + 1
	}
	return cutIndex
}

func (a *AgentRuntime) appendRootMessage(message agentruntime.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.appendRootMessageLocked(message)
}

func (a *AgentRuntime) appendRootMessageLocked(message agentruntime.Message) {
	a.rootMessages = append(a.rootMessages, message)
}

func (a *AgentRuntime) persistSnapshot() {
	a.mu.Lock()
	snapshot := db.AgentRuntimeSnapshot{
		RootMessages:  append([]agentruntime.Message(nil), a.rootMessages...),
		StoryMessages: append([]agentruntime.Message(nil), a.storyMessages...),
		Session:       a.session.Export(),
		StoryLastSeq:  a.storyLastSeq,
	}
	data, _ := json.Marshal(snapshot)
	snapshot.Fingerprint = fmt.Sprintf("%x", sha256.Sum256(data))
	a.mu.Unlock()
	a.store.SaveAgentRuntimeSnapshot(snapshot)
}

func (a *AgentRuntime) ResetPersistedState() {
	a.mu.Lock()
	if a.storyIdleTimer != nil {
		a.storyIdleTimer.Stop()
		a.storyIdleTimer = nil
	}
	if a.autonomousTimer != nil {
		a.autonomousTimer.Stop()
		a.autonomousTimer = nil
	}
	a.rootMessages = nil
	a.storyMessages = nil
	a.storyLastSeq = 0
	a.contextItems = nil
	a.lastCompaction = nil
	a.lastToolCall = nil
	a.lastToolResult = nil
	a.lastLlmCall = nil
	a.lastStoryRecallAt = 0
	a.injectedStoryIDs = map[string]bool{}
	a.autonomousRounds = 0
	a.autonomousPending = false
	a.autonomousReminder = ""
	a.autonomousUntil = nil
	a.lastRhythmSignal = nil
	a.lastToolGuardEvent = nil
	a.lastCreativeRhythm = nil
	a.lastReviewRhythm = nil
	a.lastNewsRhythm = nil
	a.mu.Unlock()
	a.session.Portal()
	a.store.ResetAgentRuntimeState()
}

func (a *AgentRuntime) scheduleStoryBatch() {
	a.mu.Lock()
	if a.storyBatchRunning {
		a.mu.Unlock()
		return
	}
	lastSeq := a.storyLastSeq
	a.mu.Unlock()

	pendingCount := a.store.CountStoryLedgerAfter("root", lastSeq)
	latest, hasLatest := a.store.LatestStoryLedger("root")
	batchSize := a.cfg.Server.Agent.Story.BatchSize
	if batchSize <= 0 {
		batchSize = 24
	}
	idleFlush := time.Duration(a.cfg.Server.Agent.Story.IdleFlushMs) * time.Millisecond
	if idleFlush <= 0 {
		idleFlush = 2 * time.Minute
	}
	shouldRun, retryAfter := storyBatchScheduleDecision(
		pendingCount,
		batchSize,
		latest.CreatedAt,
		hasLatest,
		time.Now(),
		idleFlush,
	)

	a.mu.Lock()
	if a.storyIdleTimer != nil {
		a.storyIdleTimer.Stop()
		a.storyIdleTimer = nil
	}
	if !shouldRun {
		if retryAfter > 0 {
			a.storyIdleTimer = time.AfterFunc(retryAfter, a.scheduleStoryBatch)
		}
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()

	taskKey := fmt.Sprintf("story_batch:%d:%d", lastSeq, latest.Seq)
	_, created, err := a.store.EnqueueAgentTask(db.AgentTaskItem{
		TaskKey:     taskKey,
		TaskType:    "story_batch",
		SideEffect:  false,
		MaxAttempts: 3,
		Payload: map[string]any{
			"storyLastSeq": lastSeq,
			"latestSeq":    latest.Seq,
			"notifyAgent":  false,
		},
	})
	if err != nil {
		a.store.Log("error", "Story batch task enqueue failed", map[string]any{"event": "agent.task.enqueue.failed", "taskType": "story_batch", "error": err.Error()})
		return
	}
	if created {
		a.store.Log("info", "Story batch task enqueued", map[string]any{"event": "agent.task.enqueued", "taskType": "story_batch", "taskKey": taskKey, "pendingCount": pendingCount})
	}
}

func (a *AgentRuntime) runAgentTaskWorker(ctx context.Context) {
	workerID := "agent-runtime"
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		task, ok := a.store.ClaimNextAgentTask(workerID, 5*time.Minute)
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				continue
			}
		}
		a.executeAgentTask(ctx, task)
	}
}

func (a *AgentRuntime) executeAgentTask(ctx context.Context, task db.AgentTaskItem) {
	a.store.Log("info", "Agent task started", map[string]any{"event": "agent.task.started", "taskId": task.ID, "taskType": task.TaskType, "attempt": task.Attempt})
	var result map[string]any
	var err error
	switch task.TaskType {
	case "story_batch":
		a.mu.Lock()
		a.storyBatchRunning = true
		a.mu.Unlock()
		err = a.runStoryBatch()
		a.mu.Lock()
		a.storyBatchRunning = false
		processedThrough := a.storyLastSeq
		a.mu.Unlock()
		result = map[string]any{"processedThroughSeq": processedThrough}
	default:
		err = fmt.Errorf("unsupported agent task type: %s", task.TaskType)
	}
	finished := a.store.FinishAgentTask(task.ID, result, err, time.Minute)
	if err != nil {
		a.store.Log("error", "Agent task failed", map[string]any{"event": "agent.task.failed", "taskId": task.ID, "taskType": task.TaskType, "status": finished.Status, "error": err.Error()})
	} else {
		a.store.Log("info", "Agent task completed", map[string]any{"event": "agent.task.completed", "taskId": task.ID, "taskType": task.TaskType, "result": result})
		a.store.AppendAgentStackItem(db.AgentStackItem{
			RuntimeKey: "root",
			Kind:       "task_completed",
			Content: map[string]any{
				"taskId":   task.ID,
				"taskType": task.TaskType,
				"result":   result,
			},
			Metadata: map[string]any{"taskKey": task.TaskKey},
		})
		if notify, _ := task.Payload["notifyAgent"].(bool); notify {
			a.events.Enqueue(AgentEvent{Type: "agent_task_completed", Data: map[string]any{"taskId": task.ID, "taskType": task.TaskType, "result": result}})
		}
	}
	a.persistSnapshot()
	a.scheduleStoryBatch()
	_ = ctx
}

func storyBatchScheduleDecision(
	pendingCount int,
	batchSize int,
	latestCreatedAt time.Time,
	hasLatest bool,
	now time.Time,
	idleFlush time.Duration,
) (bool, time.Duration) {
	if pendingCount <= 0 {
		return false, 0
	}
	if pendingCount >= batchSize {
		return true, 0
	}
	if !hasLatest || latestCreatedAt.IsZero() {
		return true, 0
	}
	idleFor := now.Sub(latestCreatedAt)
	if idleFor >= idleFlush {
		return true, 0
	}
	return false, idleFlush - idleFor
}

func (a *AgentRuntime) runStoryBatch() error {
	if a.llm == nil {
		return nil
	}
	limit := a.cfg.Server.Agent.Story.BatchSize
	if limit <= 0 {
		limit = 24
	}
	entries := a.store.ListStoryLedgerAfter("root", a.storyLastSeq, limit)
	maxSeq := a.storyLastSeq
	for _, entry := range entries {
		if entry.Seq > maxSeq {
			maxSeq = entry.Seq
		}
	}
	if len(entries) == 0 {
		return nil
	}
	a.storyMessages = append(a.storyMessages, agentruntime.Message{
		Role:    "user",
		Content: renderStoryLedgerBatch(entries),
	})
	for i := 0; i < 6; i++ {
		result, err := a.storyKernel.RunRound(context.Background(), agentruntime.RoundInput{
			SystemPrompt: prompts.StoryAgentSystemPrompt(),
			Messages:     append([]agentruntime.Message(nil), a.storyMessages...),
			Tools:        a.storyTools,
			ToolChoice:   "required",
		})
		if err != nil {
			a.setRuntimeError(err)
			return err
		}
		a.storyMessages = append(a.storyMessages, result.Assistant)
		finished := false
		for _, execution := range result.ToolExecutions {
			a.storyMessages = append(a.storyMessages, agentruntime.Message{Role: "tool", ToolCallID: execution.Call.ID, Content: execution.Result.Content})
			a.recordToolExecution(execution)
			if execution.Call.Name == "create_story" || execution.Call.Name == "rewrite_story" {
				a.storyLastSeq = maxSeq
			}
			if execution.Call.Name == "finish_story_batch" {
				finished = true
			}
		}
		a.maybeCompactStory(result.Completion)
		if finished || len(result.ToolExecutions) == 0 {
			if a.storyLastSeq < maxSeq {
				a.storyLastSeq = maxSeq
			}
			break
		}
	}
	return nil
}

func renderStoryLedgerBatch(entries []db.StoryLedgerItem) string {
	var b strings.Builder
	b.WriteString("<ledger_batch>\n")
	for i, entry := range entries {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "[%d] %s\n%s", entry.Seq, entry.Role, entry.Content)
	}
	b.WriteString("\n</ledger_batch>")
	return b.String()
}

func (a *AgentRuntime) appendContext(item DashboardContextItem) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.contextItems = append(a.contextItems, item)
	if len(a.contextItems) > 400 {
		a.contextItems = a.contextItems[len(a.contextItems)-400:]
	}
}

func (a *AgentRuntime) maybeCreateStory(event AgentEvent) {
	raw := common.AsString(event.Data["rawMessage"])
	if strings.TrimSpace(raw) == "" {
		return
	}
	seq := 0
	if v, ok := event.Data["messageSeq"].(int); ok {
		seq = v
	}
	a.mu.Lock()
	a.storyLastSeq++
	if seq == 0 {
		seq = a.storyLastSeq
	}
	a.mu.Unlock()
	now := time.Now()
	nickname := common.AsString(event.Data["nickname"])
	scene := event.Type
	title := trimPreview(raw, 40)
	story := db.StoryItem{
		ID:                    common.NewID(),
		Markdown:              storyMarkdown(title, common.ISO(now), scene, []string{nickname}, raw),
		Title:                 title,
		Time:                  common.ISO(now),
		Scene:                 scene,
		People:                []string{nickname},
		Impact:                "由消息事件自动沉淀，供后续记忆召回。",
		SourceMessageSeqStart: seq,
		SourceMessageSeqEnd:   seq,
		CreatedAt:             now,
		UpdatedAt:             now,
		MatchedKinds:          []string{"overview"},
	}
	a.store.AddStory(story)
}

func (a *AgentRuntime) PersonalNovelEntries() ([]personalapp.NovelEntry, error) {
	if a.personal == nil {
		return nil, fmt.Errorf("个人工作台服务不可用")
	}
	return a.personal.ListNovelEntries()
}

func (a *AgentRuntime) PersonalWorkspaceOverview() (personalapp.WorkspaceOverview, error) {
	if a.personal == nil {
		return personalapp.WorkspaceOverview{}, fmt.Errorf("个人工作台服务不可用")
	}
	return a.personal.WorkspaceOverview()
}

func (a *AgentRuntime) Snapshot(llm *llm.LLMClient) map[string]any {
	if a.terminal != nil {
		a.session.SetTerminalCWD(a.terminal.CWD())
	}
	if cursor, ok := a.store.NewsFeedCursor("ithome"); ok {
		a.session.SetIthomeOverview(a.store.CountNewsArticlesNewerThanCursor("ithome", cursor), true)
	} else {
		a.session.SetIthomeOverview(a.store.CountNewsArticlesNewerThanCursor("ithome", db.NewsFeedCursor{}), false)
	}
	taskCounts := a.store.AgentTaskStatusCounts()
	toolExecutionCounts := a.store.ToolExecutionStatusCounts()
	var workspace any
	if a.personal != nil {
		if overview, err := a.personal.WorkspaceOverview(); err == nil {
			workspace = overview
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := common.ISO(time.Now())
	lastActivity := ""
	if a.lastActivity != nil {
		lastActivity = common.ISO(*a.lastActivity)
	}
	recent := append([]DashboardContextItem(nil), a.contextItems...)
	if len(recent) > 40 {
		recent = recent[len(recent)-40:]
	}
	runtime := map[string]any{
		"initialized":          a.initialized,
		"loopState":            a.loopState,
		"lastError":            a.lastError,
		"lastActivityAt":       nullableString(lastActivity),
		"lastRoundCompletedAt": nil,
		"lastCompactionAt":     nullableTime(a.lastCompaction),
		"autonomous": map[string]any{
			"enabled":              a.cfg.Server.Agent.Autonomous.Enabled,
			"consecutiveRounds":    a.autonomousRounds,
			"maxConsecutiveRounds": a.cfg.Server.Agent.Autonomous.MaxConsecutiveRounds,
			"cooldownUntil":        nullableTime(a.autonomousUntil),
			"lastRhythmSignal":     a.lastRhythmSignal,
		},
		"toolGuard": map[string]any{
			"lastEvent": a.lastToolGuardEvent,
		},
	}
	contextSummary := map[string]any{
		"messageCount":                  len(a.contextItems),
		"compactionTotalTokenThreshold": a.cfg.Server.Agent.ContextCompactionTotalTokenThreshold,
		"recentItems":                   recent,
		"recentItemsTruncated":          len(a.contextItems) > len(recent),
	}
	activity := map[string]any{"lastToolCall": a.lastToolCall, "lastToolResultPreview": a.lastToolResult, "lastLlmCall": a.lastLlmCall}
	return map[string]any{
		"generatedAt": now,
		"agents": []any{
			map[string]any{
				"id": "root", "kind": "root", "label": "根智能体",
				"runtime": runtime, "context": contextSummary, "activity": activity,
				"session": a.session.Snapshot(),
				"queue": map[string]any{
					"pendingEventCount": a.events.Count(),
					"agentTasks":        taskCounts,
					"toolExecutions":    toolExecutionCounts,
				},
				"providers": llm.ListProviders("agent")["providers"],
				"workspace": workspace,
			},
			map[string]any{
				"id": "story", "kind": "story", "label": "故事智能体",
				"runtime": runtime, "context": contextSummary, "activity": activity,
				"story": map[string]any{
					"lastProcessedMessageSeq": a.storyLastSeq,
					"pendingMessageCount":     a.store.CountStoryLedgerAfter("root", a.storyLastSeq),
					"pendingBatch":            nil,
					"batchSize":               a.cfg.Server.Agent.Story.BatchSize,
					"idleFlushMs":             a.cfg.Server.Agent.Story.IdleFlushMs,
				},
			},
		},
	}
}

func createSystemPrompt(cfg *config.Config) string {
	return prompts.MainEngineSystemPrompt(cfg, invokeToolGuide())
}

func invokeToolGuide() string {
	return strings.Join([]string{
		"- 主循环直接暴露具体工具；每轮只调用一个工具。",
		"- 控制工具：wait 表示沉默并等待新事件；enter/back_to_portal/help 只用于进入、退出或查看个人工作台/工具环境，不用于 QQ 聊天和新闻。",
		"- QQ 群/私聊：决定发言时直接调用 send_message。message 必须非空；回复最新 QQ 消息所在会话时可省略 targetType/targetId，跨会话或回复非最新消息时必须显式填写。",
		"- 没有要发的内容、话题已经被别人说完、只是想总结或点评时，直接调用 wait，不要把 wait 写成普通文本。",
		"- 网页事实与链接读取：需要补充外部事实、读取网页链接或搜索资料时调用 search_web，参数 query；完整 URL 会优先直接读取页面，失败后再搜索。",
		"- 真实浏览器：需要动态网页、点击、输入、翻页、登录态复用、看直播或查看媒体状态时调用 browser，参数 task/url/sessionId。",
		"- 图片理解：需要看 QQ 图片、浏览器截图或本地受控图片时调用 analyze_image；它只返回识别结果，不会自动发消息。",
		"- AI 腔调检测：草稿像总结、短评、客服解释或 AI 味明显时调用 detect_ai_tone；不要每句话都检测。send_message 返回 AI_TONE_TOO_HIGH 时表示未发送，需改短改具体或 wait。",
		"- 长期记忆：需要主动查找叙事记忆时调用 search_memory，参数 query；召回结果只作参考，不要当成刚发生的新消息复述。",
		"- IT之家：要阅读全文时调用 open_ithome_article，参数 articleId；看完想分享再调用 send_message。",
		"- 磁力搜索：只有用户明确请求磁力、种子或下载资源时才调用 searchMagnetFromWeb。",
		"- 个人工作台：personal_screen 查看状态；workspace_app 写 journal/drafts/reading/music 文件；activity_app 记录自己正在做的事；todo_app 处理待办；novel_app 续写小说/长草稿；project_app 管理项目笔记；music_app 维护歌单；news_app 保存阅读摘记。",
		"- 个人工作台不要反复 screen：看完状态后应继续写入、修改、打开具体条目、结束活动或 wait。写随笔/灵感/阅读摘记/听歌记录优先调用 workspace_app(action=\"write\", kind=\"journal|drafts|reading|music\", title=\"...\", text=\"...\")。",
		"- 做自己的事情时不必每一步都私聊汇报；只有用户正在等结果、动作完成值得说明，或确实有内容想分享时才 send_message。",
		"- 终端工具：bash/read_bash_output 只在终端能力可用且确实需要执行命令时使用。",
		"- 工具因为参数错误失败时，修正参数或调用 wait；不要原样重复同一个失败调用。",
	}, "\n")
}
func contextItem(kind, label, text string) DashboardContextItem {
	return DashboardContextItem{Kind: kind, Label: label, Preview: trimPreview(text, 2000), Truncated: len([]rune(strings.TrimSpace(text))) > 2000}
}

func eventContextKind(eventType string) string {
	switch eventType {
	case "napcat_group_message", "napcat_private_message":
		return "qq_message"
	case "wake", "news_article_ingested", "story_recall_completed":
		return "system_reminder"
	default:
		return "event"
	}
}

func eventMessageRole(eventType string) string {
	switch eventType {
	case "story_recall_completed":
		return "user"
	default:
		return "user"
	}
}

func rootControlTools(
	cfg *config.Config,
	business *agentruntime.ToolCatalog,
	session *roottools.Session,
	events *EventQueue,
) *agentruntime.ToolCatalog {
	maxWait := time.Duration(cfg.Server.Agent.WaitToolMaxWaitMs) * time.Millisecond
	if maxWait <= 0 {
		maxWait = 10 * time.Minute
	}
	if cfg.Server.Agent.Autonomous.Enabled {
		idleDelay := time.Duration(cfg.Server.Agent.Autonomous.IdleDelayMs) * time.Millisecond
		if idleDelay > 0 {
			maxWait = idleDelay
		}
	}
	alwaysAvailable := map[string]bool{
		"send_message":        true,
		"search_web":          true,
		"search_memory":       true,
		"analyze_image":       true,
		"detect_ai_tone":      true,
		"browser":             true,
		"personal_screen":     true,
		"todo_app":            true,
		"novel_app":           true,
		"project_app":         true,
		"music_app":           true,
		"news_app":            true,
		"activity_app":        true,
		"workspace_app":       true,
		"open_ithome_article": true,
	}
	owner := roottools.CatalogSubtoolOwner{
		Tools:           business,
		Session:         session,
		AlwaysAvailable: alwaysAvailable,
	}
	controlCatalog := agentruntime.NewToolCatalog(
		roottools.EnterTool{Session: session},
		roottools.AppBackToPortalTool{Session: session},
		roottools.HelpTool{Session: session},
	)
	controlOwner := roottools.CatalogSubtoolOwner{
		Tools: controlCatalog,
		AlwaysAvailable: map[string]bool{
			"enter":          true,
			"back_to_portal": true,
			"help":           true,
		},
	}
	waitTool := roottools.WaitTool{MaxWait: maxWait, WaitSignal: func(ctx context.Context) bool {
		if events.WaitSignal(ctx) {
			return true
		}
		if cfg.Server.Agent.Autonomous.Enabled {
			events.Enqueue(AgentEvent{Type: "wake", Data: map[string]any{"reason": "self_continuation"}})
		} else if cfg.Server.Agent.CacheKeepaliveEnabled {
			events.Enqueue(AgentEvent{Type: "wake", Data: map[string]any{"reason": "wait_timeout"}})
		}
		return false
	}}
	catalog := agentruntime.NewToolCatalog()
	for _, definition := range controlCatalog.Definitions() {
		catalog.Add(roottools.DirectSubtool{
			Owner:           controlOwner,
			DefinitionValue: definition,
			ToolKind:        "business",
			CheckPermission: true,
		})
	}
	catalog.Add(waitTool)
	for _, definition := range business.Definitions() {
		tool, ok := business.Get(definition.Name)
		if !ok {
			continue
		}
		catalog.Add(roottools.DirectSubtool{
			Owner:           owner,
			DefinitionValue: definition,
			ToolKind:        tool.Kind(),
			CheckPermission: true,
		})
	}
	return catalog
}

func rootTools(
	cfg *config.Config,
	business *agentruntime.ToolCatalog,
	session *roottools.Session,
	events *EventQueue,
) *agentruntime.ToolCatalog {
	return rootControlTools(cfg, business, session, events)
}

func (a *AgentRuntime) setRuntimeError(err error) {
	now := time.Now()
	a.mu.Lock()
	a.lastError = &RuntimeError{Name: "AgentRuntimeError", Message: err.Error(), UpdatedAt: common.ISO(now)}
	a.loopState = "idle"
	a.lastActivity = &now
	a.mu.Unlock()
}

func (a *AgentRuntime) recordToolExecution(execution agentruntime.ToolExecution) {
	args, _ := json.Marshal(execution.Call.Arguments)
	result := execution.Result.Content
	a.mu.Lock()
	a.lastToolCall = &DashboardToolCall{Name: execution.Call.Name, ArgumentsPreview: trimPreview(string(args), 300), UpdatedAt: common.ISO(time.Now())}
	a.lastToolResult = &result
	a.mu.Unlock()
	a.appendContext(contextItem("tool_result", execution.Call.Name, execution.Result.Content))
}

func (a *AgentRuntime) recordLLMCall(completion agentruntime.Completion) {
	var total *int
	if completion.Usage != nil && completion.Usage.TotalTokens > 0 {
		v := completion.Usage.TotalTokens
		total = &v
	}
	names := make([]string, 0, len(completion.Message.ToolCalls))
	for _, call := range completion.Message.ToolCalls {
		names = append(names, call.Name)
	}
	a.mu.Lock()
	a.lastLlmCall = &DashboardLlmCall{
		Provider:                completion.Provider,
		Model:                   completion.Model,
		AssistantContentPreview: trimPreview(completion.Message.Content, 300),
		ToolCallNames:           names,
		TotalTokens:             total,
		UpdatedAt:               common.ISO(time.Now()),
	}
	a.mu.Unlock()
}

func (a *AgentRuntime) renderEventContext(event AgentEvent) string {
	switch event.Type {
	case "wake":
		return prompts.WakeReminder(event.At)
	case "napcat_group_message":
		nickname := common.AsString(event.Data["nickname"])
		if nickname == "" {
			nickname = "unknown"
		}
		return prompts.QQMessageRoutedAt("group", common.AsString(event.Data["groupId"]), nickname, common.AsString(event.Data["userId"]), common.AsString(event.Data["rawMessage"]), event.At)
	case "napcat_private_message":
		nickname := common.AsString(event.Data["nickname"])
		if nickname == "" {
			nickname = "unknown"
		}
		return prompts.QQMessageRoutedAt("private", common.AsString(event.Data["userId"]), nickname, common.AsString(event.Data["userId"]), common.AsString(event.Data["rawMessage"]), event.At)
	case "news_article_ingested":
		if article, ok := a.findNewsArticle(event.Data["articleId"]); ok {
			return prompts.ITHomeArticleIngestedNotice(prompts.ArticleSummary{
				ID:              article.ID,
				Title:           article.Title,
				PublishedAtText: formatTime(article.PublishedAt),
				URL:             article.URL,
				RSSSummary:      article.RSSSummary,
			})
		}
	case "story_recall_completed":
		return renderStoryRecallMessage(common.AsString(event.Data["content"]))
	}
	return fmt.Sprintf("<system_reminder>event: %s\ndata: %v</system_reminder>", event.Type, event.Data)
}

func renderStoryRecallMessage(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return "<story_recall>\n</story_recall>"
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(content), &items); err == nil && len(items) > 0 {
		var b strings.Builder
		b.WriteString("<story_recall>\n")
		for _, item := range items {
			date := formatStoryRecallDate(common.AsString(item["createdAt"]))
			if date == "" {
				date = "未知日期"
			}
			fmt.Fprintf(&b, "你想起了一件发生在 %s 的事情：\n%s\n", date, common.AsString(item["markdown"]))
		}
		b.WriteString("</story_recall>")
		return b.String()
	}
	return "<story_recall>\n" + content + "\n</story_recall>"
}

func formatStoryRecallDate(value string) string {
	if value == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	if t.IsZero() {
		return "未知日期"
	}
	return t.Format("2006-01-02")
}
func (a *AgentRuntime) findNewsArticle(id any) (db.NewsArticle, bool) {
	articleID := intValue(id)
	if articleID == 0 {
		return db.NewsArticle{}, false
	}
	for _, article := range a.store.Snapshot().NewsArticles {
		if article.ID == articleID {
			return article, true
		}
	}
	return db.NewsArticle{}, false
}

func portalGroups(cfg *config.Config) []prompts.PortalTarget {
	groups := make([]prompts.PortalTarget, 0, len(cfg.Server.Napcat.ListenGroupIDs))
	for _, id := range cfg.Server.Napcat.ListenGroupIDs {
		groups = append(groups, prompts.PortalTarget{
			Label:            "QQ群 " + id,
			Kind:             "qq_group",
			HasEntered:       false,
			EnterCommandText: fmt.Sprintf(`enter(id="qq_group:%s")`, id),
		})
	}
	return groups
}

func portalFeeds() []prompts.PortalTarget {
	return []prompts.PortalTarget{{
		Label:            "IT 之家",
		Kind:             "ithome",
		HasEntered:       false,
		EnterCommandText: `enter(id="ithome")`,
	}}
}

func storyMarkdown(title, timestamp, scene string, people []string, raw string) string {
	return fmt.Sprintf(`# %s
- 时间：%s
- 场景：%s
- 人物：%s
- 影响：由消息事件自动沉淀，供后续记忆召回。

起因：聊天中出现了新消息。
经过：
1. %s
结果：该消息已保存为轻量 story。`, title, timestamp, scene, strings.Join(people, ", "), raw)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	t = t.In(time.FixedZone("Asia/Shanghai", 8*60*60))
	return fmt.Sprintf("%d/%d/%d %02d:%02d", t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute())
}

func intValue(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		i, _ := x.Int64()
		return int(i)
	default:
		return 0
	}
}

func floatValue(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func toolCallNames(calls []agentruntime.ToolCall) []string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		if call.Name != "" {
			names = append(names, call.Name)
		}
	}
	return names
}

func toolDefinitionNames(definitions []agentruntime.ToolDefinition) []string {
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Name != "" {
			names = append(names, definition.Name)
		}
	}
	return names
}

func trimPreview(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullableTime(v *time.Time) any {
	if v == nil || v.IsZero() {
		return nil
	}
	return common.ISO(*v)
}
