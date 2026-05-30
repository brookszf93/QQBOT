package agent

import (
	"context"
	"fmt"
	"log"
	"qqbot-ai/internal/agentruntime"
	storycap "qqbot-ai/internal/capabilities/story"
	"qqbot-ai/internal/common"
	"qqbot-ai/internal/prompts"
	"strings"
	"time"
)

func (a *AgentRuntime) runStoryBatch(messages []agentruntime.Message) {
	a.runStoryBatchWithRange(context.Background(), messages, a.storyLastSeq+1, a.storyLastSeq+len(messages))
}

func (a *AgentRuntime) runStoryBatchWithRange(ctx context.Context, messages []agentruntime.Message, startSeq, endSeq int) {
	if a.llm == nil || len(messages) == 0 {
		return
	}
	a.hooks.OnBeforeStoryBatch(ctx, a, messages, startSeq, endSeq)
	var batchErr error
	defer func() {
		a.hooks.OnAfterStoryBatch(ctx, a, messages, startSeq, endSeq, batchErr)
	}()
	tools := buildStoryToolsForRange(a.cfg, a.store, startSeq, endSeq)
	a.storyMessages = append(a.storyMessages, messages...)
	for i := 0; i < 6; i++ {
		log.Printf("[AGENT] story round=%d messages=%d", i+1, len(a.storyMessages))
		result, err := a.storyKernel.RunRound(ctx, agentruntime.RoundInput{
			SystemPrompt: prompts.StoryAgentSystemPrompt(),
			Messages:     append([]agentruntime.Message(nil), a.storyMessages...),
			Tools:        tools,
			ToolChoice:   "required",
		})
		if err != nil {
			batchErr = err
			log.Printf("[AGENT] story round=%d error=%v", i+1, err)
			a.setRuntimeError(err)
			return
		}
		log.Printf("[AGENT] story round=%d assistant=%q toolCalls=%s", i+1, trimPreview(result.Assistant.Content, 300), runtimeToolCallNames(result.Assistant.ToolCalls))
		a.storyMessages = append(a.storyMessages, result.Assistant)
		finished := false
		for _, execution := range result.ToolExecutions {
			log.Printf("[AGENT] story tool name=%s args=%s result=%q", execution.Call.Name, mustCompactJSON(execution.Call.Arguments), trimPreview(execution.Result.Content, 300))
			a.storyMessages = append(a.storyMessages, agentruntime.Message{Role: "tool", ToolCallID: execution.Call.ID, Content: execution.Result.Content})
			a.recordToolExecution(execution)
			if execution.Call.Name == "finish_story_batch" {
				a.storyLastSeq = endSeq
				finished = true
			}
		}
		if finished || len(result.ToolExecutions) == 0 {
			break
		}
	}
	a.persistSnapshot()
}

func (a *AgentRuntime) loadPendingStoryBatch() ([]agentruntime.Message, int, int) {
	if a.store == nil {
		return nil, 0, 0
	}
	limit := a.cfg.Server.Agent.Story.BatchSize
	if limit <= 0 {
		limit = 24
	}
	items := a.store.ListStoryLedgerAfter("root", a.storyLastSeq, limit)
	if len(items) == 0 {
		return nil, 0, 0
	}
	out := make([]agentruntime.Message, 0, len(items))
	for _, item := range items {
		out = append(out, agentruntime.Message{Role: item.Role, Content: item.Content})
	}
	return out, items[0].Seq, items[len(items)-1].Seq
}

func (a *AgentRuntime) injectStoryRecallIfNeeded() {
	if a.store == nil {
		return
	}
	a.mu.Lock()
	messageCount := a.rootContextLen()
	if messageCount == a.lastRecallCount {
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()

	query := a.generateStoryRecallQuery()
	if strings.TrimSpace(query) == "" {
		a.mu.Lock()
		a.lastRecallCount = messageCount
		a.mu.Unlock()
		return
	}
	topK := a.cfg.Server.Agent.Story.Recall.TopK
	if topK <= 0 {
		topK = 2
	}
	threshold := a.cfg.Server.Agent.Story.Recall.ScoreThreshold
	storyService := storycap.Service{
		Repo:   storeStoryRepository{store: a.store, indexer: nil},
		Recall: storycap.NewVectorRecall(a.cfg, a.store),
	}
	items, err := storyService.Search(context.Background(), query, topK)
	if err != nil {
		a.mu.Lock()
		a.lastRecallCount = messageCount
		a.mu.Unlock()
		return
	}
	parts := []string{}
	a.mu.Lock()
	for _, item := range items {
		if a.recalledStoryIDs == nil {
			a.recalledStoryIDs = map[string]bool{}
		}
		if a.recalledStoryIDs[item.ID] {
			continue
		}
		if item.Score != nil && threshold > 0 && *item.Score < threshold {
			continue
		}
		a.recalledStoryIDs[item.ID] = true
		parts = append(parts, "你想起了一件事情：\n\n"+item.Markdown)
	}
	a.lastRecallCount = messageCount
	a.mu.Unlock()
	if len(parts) == 0 {
		return
	}
	content := "<story_recall>\n" + strings.Join(parts, "\n\n") + "\n</story_recall>"
	a.appendRootContext(RootContextLayerRecall, agentruntime.Message{Role: "user", Content: content})
	a.appendContext(contextItem("story_recall", "story_recall", content))
}

func (a *AgentRuntime) generateStoryRecallQuery() string {
	if a.llm == nil {
		return ""
	}
	contextText := a.ensureRootContext().RecentQuery(10)
	if strings.TrimSpace(contextText) == "" {
		return ""
	}
	model := llmModelAdapter{client: a.llm, usage: "memoryQuery"}
	tool := agentruntime.ToolDefinition{
		Name:        "search_memory",
		Description: "根据当前对话生成一句适合检索长期记忆的查询。",
		Parameters: agentruntime.ObjectSchema(map[string]any{
			"query": map[string]any{"type": "string"},
			"limit": map[string]any{"type": "integer"},
		}),
	}
	completion, err := model.Chat(context.Background(),
		"请根据当前对话上下文，调用 search_memory 工具搜索可能相关的历史记忆。只需要给出最适合检索的一句 query。",
		[]agentruntime.Message{{Role: "user", Content: contextText}},
		[]agentruntime.ToolDefinition{tool},
		map[string]any{"tool_name": "search_memory"},
	)
	if err != nil || len(completion.Message.ToolCalls) == 0 {
		return ""
	}
	call := completion.Message.ToolCalls[0]
	if call.Name != "search_memory" {
		return ""
	}
	return strings.TrimSpace(common.AsString(call.Arguments["query"]))
}

func summarizeMessages(messages []agentruntime.Message) string {
	items := []string{}
	for _, msg := range messages {
		text := strings.TrimSpace(msg.Content)
		if text == "" {
			continue
		}
		items = append(items, fmt.Sprintf("- %s: %s", msg.Role, trimPreview(text, 160)))
	}
	if len(items) > 80 {
		items = items[len(items)-80:]
	}
	return strings.Join(items, "\n")
}

func recentContextQuery(messages []agentruntime.Message, limit int) string {
	if limit <= 0 {
		limit = 8
	}
	parts := []string{}
	for i := len(messages) - 1; i >= 0 && len(parts) < limit; i-- {
		text := strings.TrimSpace(messages[i].Content)
		if text == "" {
			continue
		}
		parts = append(parts, trimPreview(text, 240))
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, "\n")
}

func (a *AgentRuntime) storyLoop(ctx context.Context) {
	batch := []agentruntime.Message{}
	idle := time.Duration(a.cfg.Server.Agent.Story.IdleFlushMs) * time.Millisecond
	if idle <= 0 {
		idle = 2 * time.Minute
	}
	timer := time.NewTimer(idle)
	defer timer.Stop()
	flush := func() {
		if len(batch) == 0 && a.store.CountStoryLedgerAfter("root", a.storyLastSeq) == 0 {
			return
		}
		messages, startSeq, endSeq := a.loadPendingStoryBatch()
		if len(messages) == 0 {
			messages = append([]agentruntime.Message(nil), batch...)
			startSeq = a.storyLastSeq + 1
			endSeq = a.storyLastSeq + len(messages)
		}
		batch = batch[:0]
		a.runStoryBatchWithRange(ctx, messages, startSeq, endSeq)
	}
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case msg := <-a.storyQueue:
			batch = append(batch, msg)
			if len(batch) >= a.cfg.Server.Agent.Story.BatchSize {
				flush()
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)
		case <-timer.C:
			flush()
			timer.Reset(idle)
		}
	}
}
