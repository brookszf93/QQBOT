package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Result 是一条标准化网页搜索结果。
type Result struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// Service 由 Tavily 等具体搜索供应商实现。
type Service interface {
	Search(context.Context, string, int) ([]Result, error)
}

// URLAwareService 在关键词搜索前识别并直读网页 URL。
type URLAwareService struct {
	Fallback            Service
	Client              *http.Client
	AllowPrivateNetwork bool
	MaxContentBytes     int64
}

var (
	errURLFetchBlocked = errors.New("URL fetch blocked")
	htmlTitlePattern   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	htmlIgnoredPattern = regexp.MustCompile(`(?is)<(?:script|style|noscript|svg)[^>]*>.*?</(?:script|style|noscript|svg)\s*>`)
	htmlTagPattern     = regexp.MustCompile(`(?s)<[^>]+>`)
	spacePattern       = regexp.MustCompile(`\s+`)
)

// Search 对 URL 直接抓取正文，抓取失败时回退到常规搜索。
func (s URLAwareService) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	target, ok := extractHTTPURL(query)
	if !ok {
		return s.searchFallback(ctx, query, maxResults)
	}

	result, fetchErr := s.fetch(ctx, target)
	if fetchErr == nil {
		return []Result{result}, nil
	}
	if errors.Is(fetchErr, errURLFetchBlocked) {
		return nil, fetchErr
	}
	results, fallbackErr := s.searchFallback(ctx, query, maxResults)
	if fallbackErr != nil {
		return nil, fmt.Errorf("直接读取 URL 失败：%v；备用搜索也失败：%w", fetchErr, fallbackErr)
	}
	return results, nil
}

func (s URLAwareService) searchFallback(ctx context.Context, query string, maxResults int) ([]Result, error) {
	if s.Fallback == nil {
		return nil, fmt.Errorf("网页搜索备用服务未配置")
	}
	return s.Fallback.Search(ctx, query, maxResults)
}

func (s URLAwareService) fetch(ctx context.Context, target string) (Result, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return Result{}, fmt.Errorf("解析 URL 失败：%w", err)
	}
	if !s.AllowPrivateNetwork {
		if err := validatePublicHTTPURL(parsed); err != nil {
			return Result{}, err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Result{}, fmt.Errorf("创建 URL 请求失败：%w", err)
	}
	req.Header.Set("User-Agent", "Kagami/1.0")
	req.Header.Set("Accept", "text/html,text/plain,application/xhtml+xml")

	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	if !s.AllowPrivateNetwork {
		cloned := *client
		previousCheckRedirect := cloned.CheckRedirect
		cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if err := validatePublicHTTPURL(req.URL); err != nil {
				return err
			}
			if previousCheckRedirect != nil {
				return previousCheckRedirect(req, via)
			}
			if len(via) >= 10 {
				return fmt.Errorf("重定向超过 10 次，已停止")
			}
			return nil
		}
		client = &cloned
	}
	res, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("读取 URL 失败：%w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		return Result{}, fmt.Errorf("读取 URL 返回 %s", res.Status)
	}
	if !s.AllowPrivateNetwork && res.Request != nil {
		if err := validatePublicHTTPURL(res.Request.URL); err != nil {
			return Result{}, err
		}
	}

	contentType := strings.ToLower(res.Header.Get("Content-Type"))
	if contentType != "" &&
		!strings.Contains(contentType, "text/") &&
		!strings.Contains(contentType, "application/xhtml+xml") {
		return Result{}, fmt.Errorf("不支持的 URL 内容类型 %q", contentType)
	}

	limit := s.MaxContentBytes
	if limit <= 0 {
		limit = 1 << 20
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return Result{}, fmt.Errorf("读取 URL 正文失败：%w", err)
	}
	title, content := extractPageText(body, contentType)
	if title == "" {
		title = parsed.Hostname()
	}
	if content == "" {
		content = title
	}

	finalURL := parsed.String()
	if res.Request != nil && res.Request.URL != nil {
		finalURL = res.Request.URL.String()
	}
	return Result{Title: title, URL: finalURL, Content: content, Score: 1}, nil
}

func extractHTTPURL(query string) (string, bool) {
	for _, token := range strings.Fields(query) {
		token = strings.Trim(token, `"'<>[](){}，。！？；;`)
		if token == "" {
			continue
		}
		candidate := token
		if !strings.Contains(candidate, "://") {
			if !strings.Contains(candidate, ".") || strings.Contains(candidate, "@") {
				continue
			}
			candidate = "https://" + candidate
		}
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Hostname() == "" {
			continue
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			continue
		}
		return parsed.String(), true
	}
	return "", false
}

func validatePublicHTTPURL(parsed *url.URL) error {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("只能读取 HTTP 和 HTTPS URL")
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return fmt.Errorf("URL host 为空")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("%w：不允许读取本机 URL", errURLFetchBlocked)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("%w：不允许读取私有网络 URL", errURLFetchBlocked)
		}
	}
	return nil
}

func extractPageText(body []byte, contentType string) (string, string) {
	text := string(body)
	if !strings.Contains(contentType, "html") && !strings.Contains(strings.ToLower(text), "<html") {
		return "", trimPageText(text)
	}

	title := ""
	if matches := htmlTitlePattern.FindStringSubmatch(text); len(matches) > 1 {
		title = trimPageText(htmlTagPattern.ReplaceAllString(matches[1], " "))
	}
	text = htmlIgnoredPattern.ReplaceAllString(text, " ")
	text = htmlTagPattern.ReplaceAllString(text, " ")
	return title, trimPageText(text)
}

func trimPageText(text string) string {
	text = html.UnescapeString(text)
	text = spacePattern.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)
	const maxRunes = 12000
	runes := []rune(text)
	if len(runes) > maxRunes {
		text = string(runes[:maxRunes]) + "..."
	}
	return text
}

// TavilyService 调用 Tavily 的 HTTP 搜索 API。
type TavilyService struct {
	APIKey string
	Client *http.Client
}

// Search 向 Tavily 提交查询并返回标准化结果。
func (s TavilyService) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	if s.APIKey == "" {
		return nil, fmt.Errorf("Tavily API key 为空")
	}
	if maxResults <= 0 {
		maxResults = 5
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	body, _ := json.Marshal(map[string]any{"api_key": s.APIKey, "query": query, "max_results": maxResults})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("Tavily 搜索失败：%s", res.Status)
	}
	var payload struct {
		Results []Result `json:"results"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return payload.Results, nil
}

// MemoryService 是用于测试和离线运行的确定性搜索供应商。
type MemoryService struct {
	Results []Result
}

// Search 返回配置好的内存搜索结果。
func (s MemoryService) Search(_ context.Context, query string, maxResults int) ([]Result, error) {
	if maxResults <= 0 || maxResults > len(s.Results) {
		maxResults = len(s.Results)
	}
	return append([]Result(nil), s.Results[:maxResults]...), nil
}
