package news

import (
	"context"
	"encoding/json"
	"fmt"

	"qqbot-ai/internal/agentruntime"
)

// Article 是 Agent 可使用的标准化新闻文章。
type Article struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	URL           string `json:"url"`
	PublishedAt   string `json:"publishedAt,omitempty"`
	Content       string `json:"content"`
	ContentSource string `json:"contentSource,omitempty"`
	Truncated     bool   `json:"truncated,omitempty"`
	MaxChars      int    `json:"maxChars,omitempty"`
}

// ArticleStore 按数字 ID 查找已入库的新闻文章。
type ArticleStore interface {
	FindArticle(id int) (Article, bool)
}

// OpenIthomeArticleTool 向 Agent 返回已保存的 IThome 文章内容。
type OpenIthomeArticleTool struct{ Store ArticleStore }

func (t OpenIthomeArticleTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "open_ithome_article", Description: "打开已轮询到的 IT之家文章", Parameters: agentruntime.ObjectSchema(map[string]any{"articleId": map[string]any{"type": "integer"}})}
}
func (t OpenIthomeArticleTool) Kind() string { return "business" }
func (t OpenIthomeArticleTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	if t.Store == nil {
		return agentruntime.ToolResult{}, fmt.Errorf("article store is nil")
	}
	id := 0
	if v, ok := call.Arguments["articleId"].(float64); ok {
		id = int(v)
	}
	article, ok := t.Store.FindArticle(id)
	if !ok {
		return agentruntime.ToolResult{Kind: "business", Content: `{"ok":false,"error":"ARTICLE_NOT_FOUND"}`}, nil
	}
	data, _ := json.Marshal(article)
	return agentruntime.ToolResult{Kind: "business", Content: string(data)}, nil
}
