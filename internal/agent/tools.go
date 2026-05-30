package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"qqbot-ai/internal/agentruntime"
	"qqbot-ai/internal/capabilities/messaging"
	"qqbot-ai/internal/capabilities/news"
	storycap "qqbot-ai/internal/capabilities/story"
	"qqbot-ai/internal/capabilities/terminal"
	"qqbot-ai/internal/capabilities/websearch"
	"qqbot-ai/internal/config"
	"qqbot-ai/internal/db"
	"strings"
)

func buildBusinessTools(cfg *config.Config, store *db.Store, sender messaging.Sender, webSearch websearch.Service, webSearchModel agentruntime.Model, terminalService *terminal.Service) *agentruntime.ToolCatalog {
	indexer := storycap.NewMemoryIndexer(cfg, store)
	recall := storycap.NewVectorRecall(cfg, store)
	storyService := storycap.Service{Repo: storeStoryRepository{store: store, indexer: indexer}, Recall: recall}
	searchTool := NewWebSearchTaskAgentTool(webSearch)
	catalog := agentruntime.NewToolCatalog(
		sendMessageTool{sender: sender},
		searchTool,
		news.OpenIthomeArticleTool{Store: storeNewsStore{store: store, maxChars: cfg.Server.News.Ithome.ArticleMaxChars}},
		storycap.SearchMemoryTool{Service: storyService},
	)
	if terminalService != nil {
		catalog.Add(terminal.BashTool{Service: terminalService})
		catalog.Add(terminal.ReadBashOutputTool{Service: terminalService})
	}
	return catalog
}

type sendMessageTool struct {
	sender messaging.Sender
}

func (t sendMessageTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "send_message", Description: "向当前群聊或私聊发送消息", Parameters: agentruntime.ObjectSchema(map[string]any{
		"targetType": map[string]any{"type": "string"},
		"targetId":   map[string]any{"type": "string"},
		"message":    map[string]any{"type": "string"},
		"os":         map[string]any{"type": "string", "description": "可选。公开展示用的一句话 OS/旁白，不是私密推理链；不会发送到 QQ。"},
	})}
}

func (t sendMessageTool) Kind() string { return "business" }

func (t sendMessageTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	if t.sender == nil {
		return agentruntime.ToolResult{}, fmt.Errorf("sender is nil")
	}
	targetType, _ := call.Arguments["targetType"].(string)
	targetID, _ := call.Arguments["targetId"].(string)
	message, _ := call.Arguments["message"].(string)
	if strings.TrimSpace(message) == "" {
		return agentruntime.ToolResult{Kind: "business", Content: `{"ok":false,"error":"INVALID_ARGUMENTS","message":"message 不能为空。"}`}, nil
	}
	if targetID == "" {
		return agentruntime.ToolResult{Kind: "business", Content: `{"ok":false,"error":"CHAT_CONTEXT_UNAVAILABLE","message":"当前缺少可发消息的 QQ 会话上下文，不能发送消息。"}`}, nil
	}
	if targetType == "" {
		return agentruntime.ToolResult{Kind: "business", Content: `{"ok":false,"error":"CHAT_CONTEXT_UNAVAILABLE","message":"当前缺少可发消息的 QQ 会话类型，不能发送消息。"}`}, nil
	}
	var id int
	var err error
	if targetType == "private" {
		id, err = t.sender.SendPrivateMessage(targetID, message)
	} else {
		id, err = t.sender.SendGroupMessage(targetID, message)
	}
	data, _ := json.Marshal(map[string]any{"messageId": id})
	return agentruntime.ToolResult{Kind: "business", Content: string(data)}, err
}

func buildStoryTools(cfg *config.Config, store *db.Store) *agentruntime.ToolCatalog {
	return buildStoryToolsForRange(cfg, store, 0, 0)
}

func buildStoryToolsForRange(cfg *config.Config, store *db.Store, startSeq, endSeq int) *agentruntime.ToolCatalog {
	storyService := storycap.Service{Repo: storeStoryRepository{store: store, indexer: storycap.NewMemoryIndexer(cfg, store)}}
	return agentruntime.NewToolCatalog(
		storycap.CreateStoryTool{Service: storyService, SourceMessageSeqStart: startSeq, SourceMessageSeqEnd: endSeq},
		storycap.RewriteStoryTool{Service: storyService, SourceMessageSeqStart: startSeq, SourceMessageSeqEnd: endSeq},
		storycap.FinishStoryBatchTool{},
	)
}

type storeStoryRepository struct {
	store   *db.Store
	indexer *storycap.MemoryIndexer
}

func (r storeStoryRepository) Save(ctx context.Context, story storycap.Story) error {
	item := db.StoryItem{
		ID:                    story.ID,
		Markdown:              story.Markdown,
		Title:                 story.Title,
		Time:                  story.Time,
		Scene:                 story.Scene,
		People:                story.People,
		Impact:                story.Impact,
		SourceMessageSeqStart: story.SourceMessageSeqStart,
		SourceMessageSeqEnd:   story.SourceMessageSeqEnd,
		CreatedAt:             story.CreatedAt,
		UpdatedAt:             story.UpdatedAt,
		Score:                 story.Score,
		MatchedKinds:          story.MatchedKinds,
	}
	if item.Title == "" {
		item.Title = storycap.ExtractTitle(item.Markdown)
	}
	r.store.AddStory(item)
	if r.indexer != nil {
		_ = r.indexer.ReindexStory(ctx, item)
	}
	return nil
}

func (r storeStoryRepository) List(context.Context) ([]storycap.Story, error) {
	items := r.store.Snapshot().Stories
	out := make([]storycap.Story, 0, len(items))
	for _, item := range items {
		out = append(out, storycap.Story{
			ID:                    item.ID,
			Markdown:              item.Markdown,
			Title:                 item.Title,
			Time:                  item.Time,
			Scene:                 item.Scene,
			People:                item.People,
			Impact:                item.Impact,
			SourceMessageSeqStart: item.SourceMessageSeqStart,
			SourceMessageSeqEnd:   item.SourceMessageSeqEnd,
			CreatedAt:             item.CreatedAt,
			UpdatedAt:             item.UpdatedAt,
			Score:                 item.Score,
			MatchedKinds:          item.MatchedKinds,
		})
	}
	return out, nil
}

func (r storeStoryRepository) Delete(_ context.Context, id string) error {
	r.store.DeleteStory(id)
	return nil
}

type storeNewsStore struct {
	store    *db.Store
	maxChars int
}

func (s storeNewsStore) FindArticle(id int) (news.Article, bool) {
	for _, article := range s.store.Snapshot().NewsArticles {
		if article.ID == id {
			content := article.Content
			source := "article_content"
			if content == "" {
				content = article.RSSSummary
				source = "rss_summary"
			}
			truncated := false
			maxChars := s.maxChars
			if maxChars > 0 && len([]rune(content)) > maxChars {
				content = string([]rune(content)[:maxChars])
				truncated = true
			}
			return news.Article{ID: article.ID, Title: article.Title, URL: article.URL, PublishedAt: article.PublishedAt.Format("2006-01-02 15:04"), Content: content, ContentSource: source, Truncated: truncated, MaxChars: maxChars}, true
		}
	}
	return news.Article{}, false
}
