package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"qqbot-ai/internal/agentruntime"
	"qqbot-ai/internal/capabilities/messaging"
	"qqbot-ai/internal/capabilities/terminal"
	"qqbot-ai/internal/capabilities/websearch"
	"qqbot-ai/internal/common"
	"qqbot-ai/internal/config"
	"qqbot-ai/internal/db"
	"qqbot-ai/internal/llm"
	"qqbot-ai/internal/prompts"
	"strings"
	"sync"
	"time"
)

// AgentRuntime 是当前可执行服务中的根/Story 运行时门面。
//
// 它维护仪表盘状态，并从进入的消息中创建轻量 Story 记录；
// 更完整的 internal/agent 运行时可用于后续更深层的接线。
type AgentRuntime struct {
	cfg                              *config.Config
	store                            *db.Store
	events                           *EventQueue
	llm                              *llm.LLMClient
	sender                           messaging.Sender
	rootKernel                       agentruntime.ReActKernel
	storyKernel                      agentruntime.ReActKernel
	hooks                            RuntimeHookSet
	rootTools                        *agentruntime.ToolCatalog
	storyTools                       *agentruntime.ToolCatalog
	session                          *rootSession
	terminal                         *terminal.Service
	storyQueue                       chan agentruntime.Message
	replyTarget                      *chatReplyTarget
	rootContext                      *RootContextManager
	rootMessages                     []agentruntime.Message
	storyMessages                    []agentruntime.Message
	mu                               sync.Mutex
	initialized                      bool
	loopState                        string
	lastError                        *RuntimeError
	lastActivity                     *time.Time
	contextItems                     []DashboardContextItem
	lastToolCall                     *DashboardToolCall
	lastToolResult                   *string
	lastLlmCall                      *DashboardLlmCall
	lastTotalTokens                  int
	storyLastSeq                     int
	lastRecallCount                  int
	recalledStoryIDs                 map[string]bool
	lastWakeReminderAt               *time.Time
	lastSentAtByTarget               map[string]time.Time
	lastPersistedSnapshotFingerprint string
	ctx                              context.Context
}

func NewAgentRuntime(cfg *config.Config, store *db.Store, events *EventQueue, llmClient *llm.LLMClient, sender messaging.Sender) *AgentRuntime {
	rootModel := llmModelAdapter{client: llmClient, usage: "agent"}
	storyModel := llmModelAdapter{client: llmClient, usage: "storyAgent"}
	webSearchModel := llmModelAdapter{client: llmClient, usage: "webSearchAgent"}
	webSearchService := websearch.MemoryService{}
	if strings.TrimSpace(cfg.Server.Tavily.APIKey) != "" {
		webSearchService = websearch.MemoryService{}
	}
	var searchService websearch.Service = webSearchService
	if strings.TrimSpace(cfg.Server.Tavily.APIKey) != "" {
		searchService = websearch.TavilyService{APIKey: cfg.Server.Tavily.APIKey}
	}
	terminalService, err := terminal.NewService(terminal.Config{
		InitialCwd:        cfg.Server.Agent.Terminal.InitialCWD,
		CommandTimeout:    time.Duration(cfg.Server.Agent.Terminal.CommandTimeoutMs) * time.Millisecond,
		PreviewBytes:      cfg.Server.Agent.Terminal.PreviewBytes,
		MaxOutputBytes:    cfg.Server.Agent.Terminal.MaxOutputBytes,
		MaxCommandLength:  cfg.Server.Agent.Terminal.MaxCommandLength,
		ReadOutputMaxSize: cfg.Server.Agent.Terminal.ReadOutputMaxSize,
		Shell:             cfg.Server.Agent.Terminal.Shell,
	})
	if err != nil {
		store.Log("warn", "Terminal service init failed", map[string]any{"event": "agent.terminal.init_failed", "error": err.Error()})
	}
	var recentProvider recentMessageProvider
	if provider, ok := sender.(recentMessageProvider); ok {
		recentProvider = provider
	}
	session := newRootSession(cfg, store, terminalService != nil, recentProvider)
	return &AgentRuntime{
		cfg:                cfg,
		store:              store,
		events:             events,
		llm:                llmClient,
		sender:             sender,
		rootKernel:         agentruntime.ReActKernel{Model: rootModel},
		storyKernel:        agentruntime.ReActKernel{Model: storyModel},
		hooks:              RuntimeHookSet{},
		rootTools:          buildBusinessTools(cfg, store, sender, searchService, webSearchModel, terminalService),
		storyTools:         buildStoryTools(cfg, store),
		session:            session,
		terminal:           terminalService,
		storyQueue:         make(chan agentruntime.Message, cfg.Server.Agent.Story.BatchSize*2),
		recalledStoryIDs:   map[string]bool{},
		lastSentAtByTarget: map[string]time.Time{},
		loopState:          "starting",
	}
}

func (a *AgentRuntime) Start(ctx context.Context) {
	a.mu.Lock()
	now := time.Now()
	a.initialized = true
	a.ctx = ctx
	a.loopState = "idle"
	a.lastActivity = &now
	a.lastWakeReminderAt = &now
	if snapshot, ok := a.loadPersistedSnapshot(); ok {
		a.replaceRootMessages(snapshot.RootMessages)
		a.storyMessages = snapshot.StoryMessages
		a.storyLastSeq = snapshot.StoryLastSeq
		a.lastRecallCount = snapshot.LastRecallCount
		if snapshot.RecalledStoryIDs != nil {
			a.recalledStoryIDs = cloneBoolMap(snapshot.RecalledStoryIDs)
		}
		a.session.restore(snapshot.Session)
	}
	a.contextItems = append(a.contextItems,
		contextItem("llm_message", "system", createSystemPrompt(a.cfg)),
		contextItem("system_reminder", "wake", prompts.WakeReminder(now)),
		contextItem("system_reminder", "portal", a.session.portalReminder()),
	)
	if len(a.rootMessages) == 0 {
		a.replaceRootMessages([]agentruntime.Message{
			{Role: "system", Content: prompts.WakeReminder(now)},
			{Role: "system", Content: a.session.portalReminder()},
		})
	}
	a.mu.Unlock()
	a.sanitizeRootContext()
	a.persistSnapshot()
	a.hooks.OnStart(ctx, a)

	go a.storyLoop(ctx)
	go a.rootLoop(ctx)
}

func rootAssistantToPersist(message agentruntime.Message, tools *agentruntime.ToolCatalog) agentruntime.Message {
	out := message
	out.ToolCalls = nil
	for _, call := range message.ToolCalls {
		if shouldPersistRootToolCall(call.Name, tools) {
			out.ToolCalls = append(out.ToolCalls, call)
		}
	}
	return out
}

func shouldPersistRootAssistant(message agentruntime.Message) bool {
	return strings.TrimSpace(message.Content) != "" || len(message.ToolCalls) > 0
}

func shouldPersistRootToolResult(toolName string, result agentruntime.ToolResult, tools *agentruntime.ToolCatalog) bool {
	if strings.TrimSpace(result.Content) == "" {
		return false
	}
	return shouldPersistRootToolCall(toolName, tools)
}

func shouldPersistRootToolCall(toolName string, tools *agentruntime.ToolCatalog) bool {
	if tools == nil {
		return toolName == "wait"
	}
	tool, ok := tools.Get(toolName)
	if !ok {
		return false
	}
	return tool.Kind() != "control" || toolName == "wait"
}

func sentMessageContextMessage(execution agentruntime.ToolExecution) agentruntime.Message {
	if execution.Call.Name != "invoke" || invokeToolName(execution.Call) != "send_message" {
		return agentruntime.Message{}
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(execution.Result.Content), &payload); err != nil {
		return agentruntime.Message{}
	}
	if ephemeral, _ := payload["ephemeral"].(bool); ephemeral {
		return agentruntime.Message{}
	}
	if errText := common.AsString(payload["error"]); strings.TrimSpace(errText) != "" {
		return agentruntime.Message{}
	}
	message := strings.TrimSpace(common.AsString(payload["message"]))
	if message == "" {
		message = strings.TrimSpace(common.AsString(invokeArguments(execution.Call)["message"]))
	}
	if message == "" {
		return agentruntime.Message{}
	}
	return agentruntime.Message{Role: "assistant", Content: message}
}

func (a *AgentRuntime) recordSentMessage(execution agentruntime.ToolExecution) {
	if execution.Call.Name != "invoke" || invokeToolName(execution.Call) != "send_message" {
		return
	}
	target := sentMessageTarget(execution)
	if target.Type == "" || strings.TrimSpace(target.ID) == "" {
		return
	}
	a.mu.Lock()
	if a.lastSentAtByTarget == nil {
		a.lastSentAtByTarget = map[string]time.Time{}
	}
	a.lastSentAtByTarget[targetKey(target)] = time.Now()
	a.mu.Unlock()
}

func sentMessageTarget(execution agentruntime.ToolExecution) chatReplyTarget {
	args := invokeArguments(execution.Call)
	return chatReplyTarget{
		Type: strings.TrimSpace(common.AsString(args["targetType"])),
		ID:   strings.TrimSpace(common.AsString(args["targetId"])),
	}
}

func targetKey(target chatReplyTarget) string {
	return strings.TrimSpace(target.Type) + ":" + strings.TrimSpace(target.ID)
}

func invokeArguments(call agentruntime.ToolCall) map[string]any {
	if args, ok := call.Arguments["arguments"].(map[string]any); ok && args != nil {
		return args
	}
	return call.Arguments
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
		Impact:                "由消息事件自动沉淀",
		SourceMessageSeqStart: seq,
		SourceMessageSeqEnd:   seq,
		CreatedAt:             now,
		UpdatedAt:             now,
		MatchedKinds:          []string{"overview"},
	}
	a.store.AddStory(story)
}

func createSystemPrompt(cfg *config.Config) string {
	return prompts.MainEngineSystemPrompt(cfg, invokeToolGuide())
}

func invokeToolGuide() string {
	return strings.Join([]string{
		"- 控制工具：enter 只能从 portal 进入子状态；back 返回上一级；wait 等待新事件；invoke 调用当前状态允许的业务工具。",
		"- portal：没有可直接 invoke 的业务工具，请先 enter 到 QQ 群、私聊、IT之家、终端或神游。",
		"- QQ 群/私聊状态：可 invoke send_message、search_web、search_memory。send_message 的 arguments 必须包含非空 message；可省略 targetType/targetId，系统会使用当前会话。没有要发的内容就调用 wait。",
		"- 需要补充外部事实时：在 QQ 群/私聊状态下 invoke search_web，arguments 参数：query。",
		"- 需要主动查找长期叙事记忆时：在 QQ 群/私聊状态下 invoke search_memory，arguments 参数：query。",
		"- IT之家状态：仅可 invoke open_ithome_article，arguments 参数：articleId。看完想分享时先 back 回 portal，再 enter 对应群聊。",
		"- 终端状态：仅可 invoke bash、read_bash_output。",
		"- 神游状态：仅可 invoke zone_out，arguments 参数：thought。",
	}, "\n")
}

func (a *AgentRuntime) sendAssistantReply(content string) bool {
	message := strings.TrimSpace(content)
	if message == "" || a.sender == nil {
		log.Printf("[AGENT] skip assistant text send empty=%t senderNil=%t", message == "", a.sender == nil)
		return false
	}
	a.mu.Lock()
	var target chatReplyTarget
	if a.replyTarget != nil {
		target = *a.replyTarget
	}
	a.mu.Unlock()
	if target.ID == "" {
		log.Printf("[AGENT] skip assistant text send: no reply target")
		return false
	}
	log.Printf("[AGENT] send assistant text targetType=%s targetId=%s message=%q", target.Type, target.ID, trimPreview(message, 500))
	var (
		messageID int
		err       error
	)
	if target.Type == "private" {
		messageID, err = a.sender.SendPrivateMessage(target.ID, message)
	} else {
		messageID, err = a.sender.SendGroupMessage(target.ID, message)
	}
	if err != nil {
		a.setRuntimeError(err)
		a.store.Log("warn", "Agent assistant text send failed", map[string]any{"event": "agent.reply.send_failed", "targetType": target.Type, "targetId": target.ID, "error": err.Error()})
		log.Printf("[AGENT] send assistant text failed targetType=%s targetId=%s error=%v", target.Type, target.ID, err)
		return false
	}
	result := fmt.Sprintf(`{"messageId":%d,"fallback":"assistant_content"}`, messageID)
	a.mu.Lock()
	a.lastToolResult = &result
	a.mu.Unlock()
	a.appendContext(contextItem("tool_result", "send_message", result))
	a.store.Log("info", "Agent assistant text sent", map[string]any{"event": "agent.reply.sent", "targetType": target.Type, "targetId": target.ID, "messageId": messageID})
	log.Printf("[AGENT] send assistant text ok targetType=%s targetId=%s messageId=%d", target.Type, target.ID, messageID)
	return true
}
