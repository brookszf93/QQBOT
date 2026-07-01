package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	BaseURL        string
	AuthToken      string
	Timeout        time.Duration
	MaxResultChars int
}

type ActionRequest struct {
	SessionID string         `json:"sessionId"`
	Action    string         `json:"action"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type ActionResponse struct {
	OK               bool           `json:"ok"`
	Error            string         `json:"error,omitempty"`
	Message          string         `json:"message,omitempty"`
	SessionID        string         `json:"sessionId,omitempty"`
	URL              string         `json:"url,omitempty"`
	Title            string         `json:"title,omitempty"`
	Text             string         `json:"text,omitempty"`
	Elements         []Element      `json:"elements,omitempty"`
	Media            []MediaState   `json:"media,omitempty"`
	ScreenshotBase64 string         `json:"screenshotBase64,omitempty"`
	ScreenshotMIME   string         `json:"screenshotMimeType,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type Element struct {
	Ref      string `json:"ref"`
	Role     string `json:"role,omitempty"`
	Name     string `json:"name,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

type MediaState struct {
	Tag         string  `json:"tag"`
	Source      string  `json:"source,omitempty"`
	CurrentTime float64 `json:"currentTime,omitempty"`
	Duration    float64 `json:"duration,omitempty"`
	Paused      bool    `json:"paused"`
	Muted       bool    `json:"muted"`
	Volume      float64 `json:"volume,omitempty"`
	ReadyState  int     `json:"readyState,omitempty"`
}

type Client struct {
	baseURL        string
	authToken      string
	http           *http.Client
	maxResultChars int
}

func NewClient(cfg Config) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("解析浏览器 base URL 失败：%w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("浏览器 base URL 必须使用 http 或 https")
	}
	if parsed.Hostname() == "" {
		return nil, errors.New("浏览器 base URL 缺少 host")
	}
	if !isLoopbackHost(parsed.Hostname()) && strings.TrimSpace(cfg.AuthToken) == "" {
		return nil, errors.New("远程浏览器 sidecar 需要配置 authToken")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	maxChars := cfg.MaxResultChars
	if maxChars <= 0 {
		maxChars = 16000
	}
	return &Client{
		baseURL:        baseURL,
		authToken:      strings.TrimSpace(cfg.AuthToken),
		http:           &http.Client{Timeout: timeout},
		maxResultChars: maxChars,
	}, nil
}

func (c *Client) Do(ctx context.Context, request ActionRequest) (ActionResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return ActionResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/action", bytes.NewReader(body))
	if err != nil {
		return ActionResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ActionResponse{}, fmt.Errorf("浏览器 sidecar 请求失败：%w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return ActionResponse{}, err
	}
	var result ActionResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return ActionResponse{}, fmt.Errorf("解析浏览器 sidecar 响应失败：%w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if result.Message == "" {
			result.Message = strings.TrimSpace(string(data))
		}
		return result, fmt.Errorf("浏览器 sidecar 返回 %s：%s", resp.Status, result.Message)
	}
	result.Text = trimRunes(result.Text, c.maxResultChars)
	return result, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func trimRunes(text string, max int) string {
	runes := []rune(strings.TrimSpace(text))
	if max <= 0 || len(runes) <= max {
		return strings.TrimSpace(text)
	}
	return string(runes[:max-1]) + "…"
}
