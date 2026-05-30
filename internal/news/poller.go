package news

import (
	"context"
	"encoding/xml"
	"html"
	"io"
	"net/http"
	rootagent "qqbot-ai/internal/agent"
	"qqbot-ai/internal/config"
	"qqbot-ai/internal/db"
	"regexp"
	"strings"
	"time"
)

// IthomePoller 拉取 IThome RSS 文章并发送 Agent 事件。
type IthomePoller struct {
	cfg    *config.Config
	store  *db.Store
	events *rootagent.EventQueue
	client *http.Client
}

// NewIthomePoller 创建一个尚未启动的 IThome 轮询器。
func NewIthomePoller(cfg *config.Config, store *db.Store, events *rootagent.EventQueue) *IthomePoller {
	return &IthomePoller{cfg: cfg, store: store, events: events, client: &http.Client{Timeout: 15 * time.Second}}
}

// Start 立即执行一次轮询，并持续按间隔轮询直到上下文取消。
func (p *IthomePoller) Start(ctx context.Context) {
	interval := time.Duration(p.cfg.Server.News.Ithome.PollIntervalMs) * time.Millisecond
	if interval <= 0 {
		return
	}
	go func() {
		p.poll(ctx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.poll(ctx)
			}
		}
	}()
}

type rssFeed struct {
	Items []rssItem `xml:"channel>item"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
}

func (p *IthomePoller) poll(ctx context.Context) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.ithome.com/rss/", nil)
	res, err := p.client.Do(req)
	if err != nil {
		p.store.Log("warn", "IThome poll failed", map[string]any{"error": err.Error()})
		return
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	var feed rssFeed
	if err := xml.Unmarshal(raw, &feed); err != nil {
		return
	}
	for _, item := range feed.Items {
		id := item.GUID
		if id == "" {
			id = item.Link
		}
		if id == "" {
			continue
		}
		pub, _ := time.Parse(time.RFC1123Z, item.PubDate)
		if pub.IsZero() {
			pub = time.Now()
		}
		content := p.fetchArticleContent(ctx, strings.TrimSpace(item.Link))
		article := db.NewsArticle{SourceKey: "ithome", UpstreamID: id, Title: strings.TrimSpace(item.Title), URL: strings.TrimSpace(item.Link), PublishedAt: pub, RSSSummary: strings.TrimSpace(item.Description), Content: content}
		saved, created := p.store.UpsertNewsArticle(article)
		if created {
			p.events.Enqueue(rootagent.AgentEvent{Type: "news_article_ingested", Data: map[string]any{"sourceKey": "ithome", "articleId": saved.ID, "title": saved.Title}})
		}
	}
}

func (p *IthomePoller) fetchArticleContent(ctx context.Context, url string) string {
	if url == "" {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	res, err := p.client.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return ""
	}
	return extractArticleText(string(raw))
}

var (
	scriptStyleRE = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>`)
	articleRE     = regexp.MustCompile(`(?is)<article[^>]*>(.*?)</article>`)
	contentRE     = regexp.MustCompile(`(?is)<div[^>]+(?:class|id)=["'][^"']*(?:post_content|news-content|article-content|content)[^"']*["'][^>]*>(.*?)</div>`)
	tagRE         = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRE       = regexp.MustCompile(`\s+`)
)

func extractArticleText(input string) string {
	for _, re := range []*regexp.Regexp{articleRE, contentRE} {
		if match := re.FindStringSubmatch(input); len(match) > 1 {
			if text := stripHTML(match[1]); text != "" {
				return text
			}
		}
	}
	return stripHTML(input)
}

func stripHTML(input string) string {
	text := scriptStyleRE.ReplaceAllString(input, " ")
	text = tagRE.ReplaceAllString(text, " ")
	text = html.UnescapeString(text)
	return strings.TrimSpace(spaceRE.ReplaceAllString(text, " "))
}

func (p *IthomePoller) hasArticle(upstreamID string) bool {
	data := p.store.Snapshot()
	for _, article := range data.NewsArticles {
		if article.SourceKey == "ithome" && article.UpstreamID == upstreamID {
			return true
		}
	}
	return false
}
