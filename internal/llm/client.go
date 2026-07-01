package llm

import (
	"QqBot/internal/capabilities/audio"
	"QqBot/internal/capabilities/video"
	"QqBot/internal/capabilities/vision"
	"QqBot/internal/common"
	"QqBot/internal/config"
	"QqBot/internal/db"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"google.golang.org/genai"
)

// LLMClient 编排供应商选择、OpenAI 兼容调用和调用日志。
type LLMClient struct {
	cfg   *config.Config
	store *db.Store
	http  *http.Client
}

// LLMChatRequest 是 /llm/chat 接收的调试台/API 请求结构。
type LLMChatRequest struct {
	Provider   string       `json:"provider"`
	Model      string       `json:"model"`
	MaxTokens  int          `json:"maxTokens,omitempty"`
	System     string       `json:"system,omitempty"`
	Messages   []LLMMessage `json:"messages"`
	Tools      []LLMTool    `json:"tools"`
	ToolChoice any          `json:"toolChoice"`
}

// LLMMessage 是便于 JSON 处理和供应商映射的聊天消息。
type LLMMessage struct {
	Role             string        `json:"role"`
	Content          any           `json:"content"`
	ReasoningContent string        `json:"reasoningContent,omitempty"`
	ToolCalls        []LLMToolCall `json:"toolCalls,omitempty"`
	ToolCallID       string        `json:"toolCallId,omitempty"`
}

// LLMTool 是工具定义传入时使用的 JSON schema。
type LLMTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

// LLMToolCall 是助手消息返回的已解码工具调用。
type LLMToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// NewLLMClient 使用配置中的供应商超时时间创建 LLM 客户端。
func NewLLMClient(cfg *config.Config, store *db.Store) *LLMClient {
	return &LLMClient{
		cfg:   cfg,
		store: store,
		http:  &http.Client{Timeout: time.Duration(cfg.Server.LLM.TimeoutMs) * time.Millisecond},
	}
}

func (c *LLMClient) ListProviders(usage string) map[string]any {
	providers := []map[string]any{}
	preferred := ""
	if u, ok := c.cfg.Server.LLM.Usages[usage]; ok && len(u.Attempts) > 0 {
		preferred = u.Attempts[0].Provider
	}
	for _, id := range []string{"deepseek", "openai", "longcat", "openai-codex", "claude-code", "google"} {
		conf := c.providerConfig(id)
		if len(conf.Models) == 0 {
			continue
		}
		if (id == "deepseek" || id == "openai" || id == "longcat" || id == "google") && strings.TrimSpace(conf.APIKey) == "" {
			continue
		}
		if (id == "openai-codex" || id == "claude-code") && c.bearerToken(id, conf) == "" {
			continue
		}
		item := map[string]any{"id": id, "models": conf.Models}
		if id == preferred {
			providers = append([]map[string]any{item}, providers...)
		} else {
			providers = append(providers, item)
		}
	}
	return map[string]any{"providers": providers}
}

func (c *LLMClient) PlaygroundTools() map[string]any {
	return map[string]any{"tools": []any{
		map[string]any{"name": "echo", "description": "返回给定文本", "parameters": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}},
		map[string]any{"name": "now", "description": "读取当前时间", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}},
	}}
}

func (c *LLMClient) ChatDirect(ctx context.Context, req LLMChatRequest) (map[string]any, int, error) {
	start := time.Now()
	requestID := common.NewID()
	provider := req.Provider
	model := req.Model
	nativeReq, response, nativeResp, err := c.callProvider(ctx, req)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		c.store.AddLlmCall(db.LlmCallItem{
			RequestID: requestID, Seq: 1, Provider: provider, Model: model, Status: "failed", LatencyMs: &latency,
			RequestPayload: common.JSONMap(req), NativeRequestPayload: nativeReq, NativeResponsePayload: nativeResp,
			Error: map[string]any{"name": "LLMError", "message": err.Error()},
		})
		return nil, http.StatusBadGateway, err
	}
	c.store.AddLlmCall(db.LlmCallItem{
		RequestID: requestID, Seq: 1, Provider: provider, Model: model, Status: "success", LatencyMs: &latency,
		RequestPayload: common.JSONMap(req), ResponsePayload: response, NativeRequestPayload: nativeReq, NativeResponsePayload: nativeResp,
	})
	response["nativeRequestPayload"] = nativeReq
	return response, http.StatusOK, nil
}

func (c *LLMClient) ChatUsage(ctx context.Context, usage string, req LLMChatRequest) (map[string]any, error) {
	usageCfg, ok := c.cfg.Server.LLM.Usages[usage]
	if !ok {
		return nil, fmt.Errorf("LLM usage is not configured: %s", usage)
	}
	var last error
	for _, attempt := range usageCfg.Attempts {
		times := attempt.Times
		if times <= 0 {
			times = 1
		}
		for i := 0; i < times; i++ {
			req.Provider = attempt.Provider
			req.Model = attempt.Model
			req.MaxTokens = attempt.MaxTokens
			originalMessageCount := len(req.Messages)
			req.Messages = repairThinkingHistory(req.Provider, req.Model, req.Messages)
			if len(req.Messages) != originalMessageCount {
				c.store.Log("warn", "LLM thinking history repaired", map[string]any{"event": "llm.history.repaired", "provider": req.Provider, "model": req.Model, "before": originalMessageCount, "after": len(req.Messages)})
			}
			c.store.Log("info", "LLM usage attempt", map[string]any{"event": "llm.usage.attempt", "usage": usage, "provider": req.Provider, "model": req.Model, "attempt": i + 1, "messages": len(req.Messages), "tools": len(req.Tools)})
			resp, _, err := c.ChatDirect(ctx, req)
			if err == nil {
				message, _ := resp["message"].(map[string]any)
				c.store.Log("info", "LLM usage response", map[string]any{"event": "llm.usage.response", "usage": usage, "provider": req.Provider, "model": req.Model, "content": trimLog(common.AsString(message["content"]), 500), "hasReasoningContent": strings.TrimSpace(common.AsString(message["reasoningContent"])) != "", "toolCalls": message["toolCalls"]})
				return resp, nil
			}
			c.store.Log("error", "LLM usage attempt failed", map[string]any{"event": "llm.usage.failed", "usage": usage, "provider": req.Provider, "model": req.Model, "error": err.Error()})
			last = err
		}
	}
	if last == nil {
		last = errors.New("no LLM attempts configured")
	}
	return nil, last
}

func repairThinkingHistory(provider, model string, messages []LLMMessage) []LLMMessage {
	if provider != "deepseek" || !strings.Contains(strings.ToLower(model), "v4") {
		return messages
	}
	out := make([]LLMMessage, 0, len(messages))
	dropToolResults := map[string]bool{}
	for _, message := range messages {
		if message.Role == "tool" && dropToolResults[message.ToolCallID] {
			continue
		}
		if message.Role == "assistant" && len(message.ToolCalls) > 0 && strings.TrimSpace(message.ReasoningContent) == "" {
			for _, call := range message.ToolCalls {
				dropToolResults[call.ID] = true
			}
			continue
		}
		out = append(out, message)
	}
	return out
}

func trimLog(text string, n int) string {
	text = strings.TrimSpace(text)
	if len([]rune(text)) <= n {
		return text
	}
	return string([]rune(text)[:n-1]) + "…"
}

func (c *LLMClient) Describe(ctx context.Context, prompt string, images []vision.ImagePart) (string, error) {
	parts := []any{map[string]any{"type": "text", "text": prompt}}
	hasImage := false
	for _, image := range images {
		if len(image.Data) == 0 || image.MimeType == "" {
			continue
		}
		hasImage = true
		parts = append(parts, map[string]any{
			"type":     "image",
			"mimeType": image.MimeType,
			"dataUrl":  "data:" + image.MimeType + ";base64," + base64.StdEncoding.EncodeToString(image.Data),
		})
	}
	if !hasImage {
		return "", errors.New("vision request has no valid image")
	}
	resp, err := c.chatVisionUsage(ctx, LLMChatRequest{
		System:   prompt,
		Messages: []LLMMessage{{Role: "user", Content: parts}},
	})
	if err != nil {
		return "", err
	}
	message, _ := resp["message"].(map[string]any)
	return strings.TrimSpace(common.AsString(message["content"])), nil
}

func (c *LLMClient) DescribeAudio(ctx context.Context, prompt string, part audio.Part) (string, error) {
	usageCfg, ok := c.cfg.Server.LLM.Usages["audio"]
	if !ok {
		return "", fmt.Errorf("LLM usage is not configured: audio")
	}
	if strings.TrimSpace(part.Path) == "" || strings.TrimSpace(part.MimeType) == "" {
		return "", fmt.Errorf("audio request has no valid file")
	}
	var last error
	for _, attempt := range usageCfg.Attempts {
		if attempt.Provider != "google" || !visionAttemptSupportsImages(attempt.Provider, attempt.Model) {
			continue
		}
		times := attempt.Times
		if times <= 0 {
			times = 1
		}
		for i := 0; i < times; i++ {
			text, err := c.callGoogleUploadedMedia(ctx, "audio", attempt.Model, prompt, part.Path, part.MimeType, part.Filename)
			if err == nil {
				return text, nil
			}
			last = err
		}
	}
	if last == nil {
		last = errors.New("audio usage has no supported attempts")
	}
	return "", last
}

func (c *LLMClient) DescribeVideo(ctx context.Context, prompt string, part video.Part) (string, error) {
	usageCfg, ok := c.cfg.Server.LLM.Usages["video"]
	if !ok {
		return "", fmt.Errorf("LLM usage is not configured: video")
	}
	if strings.TrimSpace(part.Path) == "" || strings.TrimSpace(part.MimeType) == "" {
		return "", fmt.Errorf("video request has no valid file")
	}
	var last error
	for _, attempt := range usageCfg.Attempts {
		if attempt.Provider != "google" || !visionAttemptSupportsImages(attempt.Provider, attempt.Model) {
			continue
		}
		times := attempt.Times
		if times <= 0 {
			times = 1
		}
		for i := 0; i < times; i++ {
			text, err := c.callGoogleUploadedMedia(ctx, "video", attempt.Model, prompt, part.Path, part.MimeType, part.Filename)
			if err == nil {
				return text, nil
			}
			last = err
		}
	}
	if last == nil {
		last = errors.New("video usage has no supported attempts")
	}
	return "", last
}

func (c *LLMClient) callGoogleUploadedMedia(ctx context.Context, usage, model, prompt, filePath, mimeType, filename string) (string, error) {
	conf := c.providerConfig("google")
	if strings.TrimSpace(conf.APIKey) == "" {
		return "", fmt.Errorf("google provider apiKey is empty")
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:     conf.APIKey,
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: c.http,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: strings.TrimRight(conf.BaseURL, "/"),
		},
	})
	if err != nil {
		return "", err
	}
	start := time.Now()
	requestID := common.NewID()
	uploaded, err := client.Files.UploadFromPath(ctx, filePath, &genai.UploadFileConfig{
		MIMEType:    mimeType,
		DisplayName: filename,
	})
	if err != nil {
		c.logMediaCall(requestID, usage, model, start, "failed", prompt, mimeType, filename, "", err)
		return "", err
	}
	defer func() {
		if uploaded.Name != "" {
			_, _ = client.Files.Delete(context.Background(), uploaded.Name, nil)
		}
	}()
	uploaded, err = waitForGoogleFile(ctx, client, uploaded)
	if err != nil {
		c.logMediaCall(requestID, usage, model, start, "failed", prompt, mimeType, filename, uploaded.URI, err)
		return "", err
	}
	text, _, _, err := c.callGoogleInteractionMedia(ctx, conf, model, prompt, usage, uploaded.URI, uploaded.MIMEType)
	if err != nil {
		c.logMediaCall(requestID, usage, model, start, "failed", prompt, mimeType, filename, uploaded.URI, err)
		return "", err
	}
	if text == "" {
		err = fmt.Errorf("google interaction returned empty %s description", usage)
		c.logMediaCall(requestID, usage, model, start, "failed", prompt, mimeType, filename, uploaded.URI, err)
		return "", err
	}
	c.logMediaCall(requestID, usage, model, start, "success", prompt, mimeType, filename, uploaded.URI, nil)
	return text, nil
}

func waitForGoogleFile(ctx context.Context, client *genai.Client, file *genai.File) (*genai.File, error) {
	for file.State == genai.FileStateProcessing {
		select {
		case <-ctx.Done():
			return file, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		latest, err := client.Files.Get(ctx, file.Name, nil)
		if err != nil {
			return file, err
		}
		file = latest
	}
	if file.State == genai.FileStateFailed {
		return file, fmt.Errorf("google file processing failed")
	}
	return file, nil
}

func (c *LLMClient) logMediaCall(requestID, usage, model string, start time.Time, status, prompt, mimeType, filename, fileURI string, callErr error) {
	latency := int(time.Since(start).Milliseconds())
	item := db.LlmCallItem{
		RequestID: requestID,
		Seq:       1,
		Provider:  "google",
		Model:     model,
		Status:    status,
		LatencyMs: &latency,
		RequestPayload: map[string]any{
			"usage":    usage,
			"prompt":   prompt,
			"mimeType": mimeType,
			"filename": filename,
		},
		NativeRequestPayload: map[string]any{"fileUri": fileURI},
	}
	if callErr != nil {
		item.Error = map[string]any{"name": "LLMError", "message": callErr.Error()}
	}
	c.store.AddLlmCall(item)
}

func (c *LLMClient) chatVisionUsage(ctx context.Context, req LLMChatRequest) (map[string]any, error) {
	usageCfg, ok := c.cfg.Server.LLM.Usages["vision"]
	if !ok {
		return nil, fmt.Errorf("LLM usage is not configured: %s", "vision")
	}
	var last error
	attempted := false
	for _, attempt := range usageCfg.Attempts {
		if !visionAttemptSupportsImages(attempt.Provider, attempt.Model) {
			c.store.Log("warn", "Vision LLM attempt skipped", map[string]any{"event": "llm.vision.unsupported_model", "provider": attempt.Provider, "model": attempt.Model, "reason": "model_does_not_support_image_content"})
			continue
		}
		times := attempt.Times
		if times <= 0 {
			times = 1
		}
		for i := 0; i < times; i++ {
			attempted = true
			req.Provider = attempt.Provider
			req.Model = attempt.Model
			c.store.Log("info", "LLM usage attempt", map[string]any{"event": "llm.usage.attempt", "usage": "vision", "provider": req.Provider, "model": req.Model, "attempt": i + 1, "messages": len(req.Messages), "tools": len(req.Tools)})
			resp, _, err := c.ChatDirect(ctx, req)
			if err == nil {
				message, _ := resp["message"].(map[string]any)
				c.store.Log("info", "LLM usage response", map[string]any{"event": "llm.usage.response", "usage": "vision", "provider": req.Provider, "model": req.Model, "content": trimLog(common.AsString(message["content"]), 500), "hasReasoningContent": strings.TrimSpace(common.AsString(message["reasoningContent"])) != "", "toolCalls": message["toolCalls"]})
				return resp, nil
			}
			c.store.Log("error", "LLM usage attempt failed", map[string]any{"event": "llm.usage.failed", "usage": "vision", "provider": req.Provider, "model": req.Model, "error": err.Error()})
			last = err
		}
	}
	if last != nil {
		return nil, last
	}
	if !attempted {
		return nil, errors.New("vision usage has no image-capable attempts")
	}
	return nil, errors.New("no LLM attempts configured")
}

func visionAttemptSupportsImages(provider, model string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	switch provider {
	case "deepseek":
		return false
	case "openai":
		return strings.Contains(model, "gpt-4o") || strings.Contains(model, "gpt-4.1") || strings.Contains(model, "gpt-5") || strings.Contains(model, "o3") || strings.Contains(model, "o4")
	case "openai-codex", "claude-code":
		return true
	case "google":
		return strings.HasPrefix(model, "gemini-")
	default:
		return false
	}
}

func (c *LLMClient) callProvider(ctx context.Context, req LLMChatRequest) (map[string]any, map[string]any, map[string]any, error) {
	conf := c.providerConfig(req.Provider)
	if req.Model == "" || !contains(conf.Models, req.Model) {
		return nil, nil, nil, fmt.Errorf("所选 LLM 模型未在当前 provider 中配置")
	}
	if conf.BaseURL == "" {
		return nil, nil, nil, fmt.Errorf("provider baseUrl is empty")
	}
	if req.Provider == "openai-codex" {
		return c.callOpenAICodex(ctx, req, conf)
	}
	if req.Provider == "claude-code" {
		return c.callClaudeCode(ctx, req, conf)
	}
	if req.Provider == "google" {
		return c.callGoogle(ctx, req, conf)
	}
	if isOpenAICompatibleProvider(req.Provider) && conf.APIKey == "" {
		return nil, nil, nil, fmt.Errorf("provider apiKey is empty")
	}
	payload := toOpenAIChatPayload(req)
	nativeReq := common.JSONMap(payload)
	url := chatCompletionsURL(req.Provider, conf.BaseURL)
	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nativeReq, nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if conf.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+conf.APIKey)
	}
	res, err := c.http.Do(httpReq)
	if err != nil {
		return nativeReq, nil, nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	var nativeResp map[string]any
	_ = json.Unmarshal(raw, &nativeResp)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nativeReq, nil, nativeResp, fmt.Errorf("LLM 上游服务调用失败: %s%s", res.Status, upstreamErrorSuffix(nativeResp))
	}
	response := fromOpenAIChatResponse(nativeResp, req.Provider, req.Model)
	response = coerceRequiredSingleActResponse(req, response)
	return nativeReq, response, nativeResp, nil
}

func (c *LLMClient) callGoogle(ctx context.Context, req LLMChatRequest, conf config.LLMProviderConfig) (map[string]any, map[string]any, map[string]any, error) {
	return c.callGoogleInteraction(ctx, req, conf)
}

func (c *LLMClient) bearerToken(provider string, conf config.LLMProviderConfig) string {
	if strings.TrimSpace(conf.APIKey) != "" {
		return strings.TrimSpace(conf.APIKey)
	}
	if session, ok := c.store.OAuthSession(provider); ok && session.AccessToken != "" {
		return session.AccessToken
	}
	return ""
}

func (c *LLMClient) callOpenAICodex(ctx context.Context, req LLMChatRequest, conf config.LLMProviderConfig) (map[string]any, map[string]any, map[string]any, error) {
	token := c.bearerToken("openai-codex", conf)
	if token == "" {
		return nil, nil, nil, fmt.Errorf("openai-codex credentials are empty")
	}
	accountID := ""
	if session, ok := c.store.OAuthSession("openai-codex"); ok && session.AccountID != nil {
		accountID = *session.AccountID
	}
	payload := toCodexRequestBody(req, accountID)
	nativeReq := common.JSONMap(payload)
	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, conf.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nativeReq, nil, nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "brooks/1.0")
	if accountID != "" {
		httpReq.Header.Set("ChatGPT-Account-Id", accountID)
	}
	res, err := c.http.Do(httpReq)
	if err != nil {
		return nativeReq, nil, nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	text := string(raw)
	completed := extractSSEEvent(text, "response.completed")
	nativeResp := map[string]any{"status": res.StatusCode, "sse": text}
	if completed != nil {
		nativeResp = completed
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nativeReq, nil, nativeResp, fmt.Errorf("openai-codex unauthorized")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nativeReq, nil, nativeResp, fmt.Errorf("LLM 上游服务调用失败: %s", res.Status)
	}
	if completed == nil {
		return nativeReq, nil, nativeResp, fmt.Errorf("openai-codex invalid SSE response")
	}
	response, err := fromCodexCompletedEvent(completed, req.Model)
	if err != nil {
		return nativeReq, nil, nativeResp, err
	}
	return nativeReq, response, nativeResp, nil
}

func (c *LLMClient) callClaudeCode(ctx context.Context, req LLMChatRequest, conf config.LLMProviderConfig) (map[string]any, map[string]any, map[string]any, error) {
	token := c.bearerToken("claude-code", conf)
	if token == "" {
		return nil, nil, nil, fmt.Errorf("claude-code credentials are empty")
	}
	payload := toClaudeCodeRequestBody(req)
	nativeReq := common.JSONMap(payload)
	body, _ := json.Marshal(payload)
	url := strings.TrimRight(conf.BaseURL, "/") + "/v1/messages?beta=true"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nativeReq, nil, nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Anthropic-Version", "2023-06-01")
	httpReq.Header.Set("Anthropic-Beta", "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,effort-2025-11-24")
	httpReq.Header.Set("Anthropic-Dangerous-Direct-Browser-Access", "true")
	httpReq.Header.Set("User-Agent", "claude-cli/2.1.76 (external, sdk-cli)")
	httpReq.Header.Set("X-App", "cli")
	res, err := c.http.Do(httpReq)
	if err != nil {
		return nativeReq, nil, nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	text := string(raw)
	nativeResp := parseJSONMap(text)
	if nativeResp == nil {
		nativeResp = map[string]any{"status": res.StatusCode, "body": text}
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nativeReq, nil, nativeResp, fmt.Errorf("claude-code unauthorized")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nativeReq, nil, nativeResp, fmt.Errorf("LLM 上游服务调用失败: %s", res.Status)
	}
	response, err := fromClaudeMessageResponse(nativeResp, req.Model)
	if err != nil {
		return nativeReq, nil, nativeResp, err
	}
	return nativeReq, response, nativeResp, nil
}

func (c *LLMClient) providerConfig(id string) config.LLMProviderConfig {
	switch id {
	case "deepseek":
		return c.cfg.Server.LLM.Providers.Deepseek
	case "openai":
		return c.cfg.Server.LLM.Providers.OpenAI
	case "longcat":
		return c.cfg.Server.LLM.Providers.LongCat
	case "openai-codex":
		return c.cfg.Server.LLM.Providers.OpenAICodex
	case "claude-code":
		return c.cfg.Server.LLM.Providers.ClaudeCode
	case "google":
		conf := c.cfg.Server.LLM.Providers.Google
		if strings.TrimSpace(conf.APIKey) == "" {
			conf.APIKey = c.cfg.Server.Agent.Story.Memory.Embedding.APIKey
		}
		if strings.TrimSpace(conf.BaseURL) == "" {
			conf.BaseURL = c.cfg.Server.Agent.Story.Memory.Embedding.BaseURL
		}
		return conf
	default:
		return config.LLMProviderConfig{}
	}
}

func isOpenAICompatibleProvider(provider string) bool {
	switch provider {
	case "deepseek", "openai", "longcat":
		return true
	default:
		return false
	}
}

func chatCompletionsURL(provider, baseURL string) string {
	url := strings.TrimRight(baseURL, "/")
	if !isOpenAICompatibleProvider(provider) || strings.HasSuffix(url, "/chat/completions") {
		return url
	}
	if provider == "longcat" && strings.HasSuffix(url, "/openai") {
		return url + "/v1/chat/completions"
	}
	return url + "/chat/completions"
}

func valueOrString(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func toOpenAIChatPayload(req LLMChatRequest) map[string]any {
	messages := []map[string]any{}
	if req.System != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		item := map[string]any{"role": m.Role}
		if m.Role == "tool" {
			item["tool_call_id"] = m.ToolCallID
			item["content"] = common.AsString(m.Content)
		} else {
			item["content"] = normalizeContent(m.Content)
		}
		if m.Role == "assistant" && strings.TrimSpace(m.ReasoningContent) != "" {
			item["reasoning_content"] = m.ReasoningContent
		}
		if len(m.ToolCalls) > 0 {
			calls := []map[string]any{}
			for _, tc := range m.ToolCalls {
				args, _ := json.Marshal(tc.Arguments)
				calls = append(calls, map[string]any{"id": tc.ID, "type": "function", "function": map[string]any{"name": tc.Name, "arguments": string(args)}})
			}
			item["tool_calls"] = calls
		}
		messages = append(messages, item)
	}
	payload := map[string]any{"model": req.Model, "messages": messages}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		tools := []map[string]any{}
		for _, tool := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters}})
		}
		payload["tools"] = tools
		payload["tool_choice"] = openAIChatToolChoice(req.Provider, req.ToolChoice)
	}
	return payload
}

func openAIChatToolChoice(provider string, choice any) any {
	if provider == "deepseek" && choice == "required" {
		return "auto"
	}
	if m, ok := choice.(map[string]any); ok && common.AsString(m["tool_name"]) != "" {
		if provider == "deepseek" {
			return "auto"
		}
		return map[string]any{"type": "function", "function": map[string]any{"name": common.AsString(m["tool_name"])}}
	}
	return choice
}

func coerceRequiredSingleActResponse(req LLMChatRequest, response map[string]any) map[string]any {
	if common.AsString(req.ToolChoice) != "required" || len(req.Tools) != 1 || req.Tools[0].Name != "act" {
		return response
	}
	message, _ := response["message"].(map[string]any)
	if message == nil {
		return response
	}
	if calls, ok := message["toolCalls"].([]any); ok && len(calls) > 0 {
		return response
	}
	content := strings.TrimSpace(common.AsString(message["content"]))
	if content == "" || !isPlainWaitText(content) {
		return response
	}
	message["content"] = ""
	message["toolCalls"] = []any{map[string]any{
		"id":        "coerced_act_" + common.NewID(),
		"name":      "act",
		"arguments": map[string]any{"action": "wait"},
	}}
	return response
}

func isPlainWaitText(content string) bool {
	content = strings.TrimSpace(strings.ToLower(content))
	content = strings.Trim(content, ".! \t\r\n")
	return content == "wait"
}

func upstreamErrorSuffix(native map[string]any) string {
	if native == nil {
		return ""
	}
	if errObj, ok := native["error"].(map[string]any); ok {
		if message := strings.TrimSpace(common.AsString(errObj["message"])); message != "" {
			return ": " + message
		}
	}
	return ""
}

func toCodexRequestBody(req LLMChatRequest, accountID string) map[string]any {
	input := []map[string]any{}
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			input = append(input, map[string]any{"role": "user", "content": codexContent(m.Content)})
		case "assistant":
			if common.AsString(m.Content) != "" {
				input = append(input, map[string]any{"role": "assistant", "content": common.AsString(m.Content)})
			}
			for _, tc := range m.ToolCalls {
				args, _ := json.Marshal(tc.Arguments)
				input = append(input, map[string]any{"type": "function_call", "call_id": tc.ID, "name": tc.Name, "arguments": string(args)})
			}
		case "tool":
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": common.AsString(m.Content)})
		}
	}
	tools := []map[string]any{}
	for _, tool := range req.Tools {
		tools = append(tools, map[string]any{"type": "function", "name": tool.Name, "description": tool.Description, "parameters": tool.Parameters})
	}
	instructions := req.System
	if instructions == "" {
		instructions = "You are a helpful assistant."
	}
	return map[string]any{
		"model":            req.Model,
		"instructions":     instructions,
		"input":            input,
		"tools":            tools,
		"tool_choice":      codexToolChoice(req.ToolChoice),
		"prompt_cache_key": codexPromptCacheKey(req, accountID),
		"stream":           true,
		"store":            false,
	}
}

func codexContent(content any) any {
	switch parts := content.(type) {
	case []any:
		out := []map[string]any{}
		for _, part := range parts {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if m["type"] == "image" {
				out = append(out, map[string]any{"type": "input_image", "image_url": m["dataUrl"]})
			} else {
				out = append(out, map[string]any{"type": "input_text", "text": m["text"]})
			}
		}
		return out
	default:
		return content
	}
}

func codexToolChoice(choice any) any {
	if m, ok := choice.(map[string]any); ok && common.AsString(m["tool_name"]) != "" {
		return map[string]any{"type": "function", "name": common.AsString(m["tool_name"])}
	}
	return choice
}

func codexPromptCacheKey(req LLMChatRequest, accountID string) string {
	if accountID == "" {
		accountID = "default"
	}
	toolChoice := req.ToolChoice
	if choice, ok := req.ToolChoice.(map[string]any); ok && common.AsString(choice["tool_name"]) != "" {
		toolChoice = common.AsString(choice["tool_name"])
	}
	instructions := req.System
	if instructions == "" {
		instructions = "You are a helpful assistant."
	}
	seed, _ := json.Marshal(struct {
		Provider     string    `json:"provider"`
		AccountID    string    `json:"accountId"`
		Model        string    `json:"model"`
		Instructions string    `json:"instructions"`
		Tools        []LLMTool `json:"tools"`
		ToolChoice   any       `json:"toolChoice"`
	}{
		Provider:     "openai-codex",
		AccountID:    accountID,
		Model:        req.Model,
		Instructions: instructions,
		Tools:        req.Tools,
		ToolChoice:   toolChoice,
	})
	sum := sha256.Sum256(seed)
	return "kagami-codex-" + hex.EncodeToString(sum[:])[:32]
}

func toClaudeCodeRequestBody(req LLMChatRequest) map[string]any {
	messages := []map[string]any{}
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			messages = append(messages, map[string]any{"role": "user", "content": claudeUserContent(m.Content)})
		case "assistant":
			content := []map[string]any{}
			if common.AsString(m.Content) != "" {
				content = append(content, map[string]any{"type": "text", "text": common.AsString(m.Content)})
			}
			for _, tc := range m.ToolCalls {
				content = append(content, map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": tc.Arguments})
			}
			if len(content) > 0 {
				messages = append(messages, map[string]any{"role": "assistant", "content": content})
			}
		case "tool":
			messages = append(messages, map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": common.AsString(m.Content)}}})
		}
	}
	system := []map[string]any{
		{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.76.b57; cc_entrypoint=sdk-cli; cch=00000;"},
		{"type": "text", "text": "You are a Claude agent, built on Anthropic's Claude Agent SDK."},
	}
	if req.System != "" {
		system = append(system, map[string]any{"type": "text", "text": req.System})
	}
	payload := map[string]any{
		"model":         req.Model,
		"stream":        true,
		"max_tokens":    claudeMaxTokens(req.Model),
		"cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"},
		"system":        system,
		"messages":      messages,
	}
	if len(req.Tools) > 0 && req.ToolChoice != "none" {
		tools := []map[string]any{}
		for _, tool := range req.Tools {
			tools = append(tools, map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": map[string]any{"type": "object", "properties": tool.Parameters["properties"]}})
		}
		payload["tools"] = tools
		if choice := claudeToolChoice(req.ToolChoice); choice != nil {
			payload["tool_choice"] = choice
		}
	}
	if strings.HasPrefix(req.Model, "claude-sonnet-4-6") || strings.HasPrefix(req.Model, "claude-opus-4-6") {
		payload["thinking"] = map[string]any{"type": "adaptive"}
		payload["output_config"] = map[string]any{"effort": "medium"}
		payload["context_management"] = map[string]any{"edits": []map[string]any{{"type": "clear_thinking_20251015", "keep": "all"}}}
	} else if strings.HasPrefix(req.Model, "claude-sonnet-4-") || strings.HasPrefix(req.Model, "claude-opus-4-") {
		payload["thinking"] = map[string]any{"type": "enabled", "budget_tokens": 1024}
	}
	return payload
}

func claudeUserContent(content any) any {
	switch parts := content.(type) {
	case []any:
		out := []map[string]any{}
		for _, part := range parts {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if m["type"] == "image" {
				out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": m["mimeType"], "data": dataURLPayload(common.AsString(m["dataUrl"]))}})
			} else {
				out = append(out, map[string]any{"type": "text", "text": m["text"]})
			}
		}
		return out
	default:
		return []map[string]any{{"type": "text", "text": common.AsString(content)}}
	}
}

func claudeToolChoice(choice any) any {
	switch v := choice.(type) {
	case string:
		if v == "auto" {
			return map[string]any{"type": "auto"}
		}
		if v == "required" {
			return map[string]any{"type": "any"}
		}
		return nil
	case map[string]any:
		if name := common.AsString(v["tool_name"]); name != "" {
			return map[string]any{"type": "tool", "name": name}
		}
	}
	return nil
}

func claudeMaxTokens(model string) int {
	if strings.HasPrefix(model, "claude-sonnet-4-") || strings.HasPrefix(model, "claude-opus-4-") {
		return 32000
	}
	return 4096
}

func normalizeContent(content any) any {
	switch value := content.(type) {
	case []any:
		parts := []map[string]any{}
		for _, part := range value {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if m["type"] == "image" {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": m["dataUrl"]}})
			} else {
				parts = append(parts, map[string]any{"type": "text", "text": m["text"]})
			}
		}
		return parts
	default:
		return content
	}
}

func fromOpenAIChatResponse(native map[string]any, provider, fallbackModel string) map[string]any {
	model := common.AsString(native["model"])
	if model == "" {
		model = fallbackModel
	}
	content := ""
	reasoningContent := ""
	toolCalls := []any{}
	if choices, ok := native["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if msg, ok := choice["message"].(map[string]any); ok {
				content = common.AsString(msg["content"])
				reasoningContent = common.AsString(msg["reasoning_content"])
				if calls, ok := msg["tool_calls"].([]any); ok {
					for _, call := range calls {
						if cm, ok := call.(map[string]any); ok {
							fn, _ := cm["function"].(map[string]any)
							args := map[string]any{}
							_ = json.Unmarshal([]byte(common.AsString(fn["arguments"])), &args)
							toolCalls = append(toolCalls, map[string]any{"id": common.AsString(cm["id"]), "name": common.AsString(fn["name"]), "arguments": args})
						}
					}
				}
			}
		}
	}
	usage := map[string]any{}
	if u, ok := native["usage"].(map[string]any); ok {
		usage["promptTokens"] = u["prompt_tokens"]
		usage["completionTokens"] = u["completion_tokens"]
		usage["totalTokens"] = u["total_tokens"]
		if details, ok := u["prompt_tokens_details"].(map[string]any); ok {
			if cached := numberAny(details["cached_tokens"]); cached > 0 {
				usage["cacheHitTokens"] = int(cached)
			}
		}
		if _, ok := usage["cacheHitTokens"]; !ok {
			if cached := numberAny(u["prompt_cache_hit_tokens"]); cached > 0 {
				usage["cacheHitTokens"] = int(cached)
			}
		}
		if miss := numberAny(u["prompt_cache_miss_tokens"]); miss > 0 {
			usage["cacheMissTokens"] = int(miss)
		} else if hit := numberAny(usage["cacheHitTokens"]); hit > 0 {
			if prompt := numberAny(u["prompt_tokens"]); prompt > 0 {
				usage["cacheMissTokens"] = int(math.Max(prompt-hit, 0))
			}
		}
	}
	return map[string]any{
		"provider": provider,
		"model":    model,
		"message":  map[string]any{"role": "assistant", "content": content, "reasoningContent": reasoningContent, "toolCalls": toolCalls},
		"usage":    usage,
	}
}

func fromCodexCompletedEvent(event map[string]any, fallbackModel string) (map[string]any, error) {
	response, _ := event["response"].(map[string]any)
	if response == nil {
		return nil, fmt.Errorf("openai-codex missing response")
	}
	content := ""
	toolCalls := []any{}
	if output, ok := response["output"].([]any); ok {
		for _, itemAny := range output {
			item, _ := itemAny.(map[string]any)
			switch item["type"] {
			case "function_call":
				args := map[string]any{}
				_ = json.Unmarshal([]byte(common.AsString(item["arguments"])), &args)
				id := common.AsString(item["call_id"])
				if id == "" {
					id = common.AsString(item["id"])
				}
				toolCalls = append(toolCalls, map[string]any{"id": id, "name": common.AsString(item["name"]), "arguments": args})
			case "message":
				if item["role"] == "assistant" {
					if parts, ok := item["content"].([]any); ok {
						for _, partAny := range parts {
							part, _ := partAny.(map[string]any)
							if part["type"] == "output_text" {
								content += common.AsString(part["text"])
							}
						}
					}
				}
			}
		}
	}
	if content == "" && len(toolCalls) == 0 {
		return nil, fmt.Errorf("openai-codex empty assistant output")
	}
	model := common.AsString(response["model"])
	if model == "" {
		model = fallbackModel
	}
	return map[string]any{"provider": "openai-codex", "model": model, "message": map[string]any{"role": "assistant", "content": content, "toolCalls": toolCalls}, "usage": codexUsage(response)}, nil
}

func fromClaudeMessageResponse(native map[string]any, fallbackModel string) (map[string]any, error) {
	if strings.HasPrefix(common.AsString(native["body"]), "event:") {
		if parsed := parseClaudeStream(common.AsString(native["body"])); parsed != nil {
			native = parsed
		}
	}
	content := ""
	toolCalls := []any{}
	if blocks, ok := native["content"].([]any); ok {
		for _, blockAny := range blocks {
			block, _ := blockAny.(map[string]any)
			switch block["type"] {
			case "text":
				if text := common.AsString(block["text"]); text != "" {
					if content != "" {
						content += "\n"
					}
					content += text
				}
			case "tool_use":
				args, _ := block["input"].(map[string]any)
				if args == nil {
					args = map[string]any{}
				}
				toolCalls = append(toolCalls, map[string]any{"id": common.AsString(block["id"]), "name": common.AsString(block["name"]), "arguments": args})
			}
		}
	}
	if content == "" && len(toolCalls) == 0 {
		return nil, fmt.Errorf("claude-code empty assistant output")
	}
	model := common.AsString(native["model"])
	if model == "" {
		model = fallbackModel
	}
	return map[string]any{"provider": "claude-code", "model": model, "message": map[string]any{"role": "assistant", "content": content, "toolCalls": toolCalls}, "usage": claudeUsage(native)}, nil
}

func codexUsage(response map[string]any) map[string]any {
	u, _ := response["usage"].(map[string]any)
	if u == nil {
		return nil
	}
	out := map[string]any{"promptTokens": u["input_tokens"], "completionTokens": u["output_tokens"], "totalTokens": u["total_tokens"]}
	if details, ok := u["input_tokens_details"].(map[string]any); ok {
		if cached := numberAny(details["cached_tokens"]); cached > 0 {
			out["cacheHitTokens"] = int(cached)
			if input := numberAny(u["input_tokens"]); input > 0 {
				out["cacheMissTokens"] = int(math.Max(input-cached, 0))
			}
		}
	}
	return out
}

func claudeUsage(native map[string]any) map[string]any {
	u, _ := native["usage"].(map[string]any)
	if u == nil {
		return nil
	}
	input := numberAny(u["input_tokens"]) + numberAny(u["cache_read_input_tokens"]) + numberAny(u["cache_creation_input_tokens"])
	output := numberAny(u["output_tokens"])
	out := map[string]any{}
	if input > 0 {
		out["promptTokens"] = int(input)
	}
	if output > 0 {
		out["completionTokens"] = int(output)
	}
	if input+output > 0 {
		out["totalTokens"] = int(input + output)
	}
	if v := numberAny(u["cache_read_input_tokens"]); v > 0 {
		out["cacheHitTokens"] = int(v)
	}
	if v := numberAny(u["cache_creation_input_tokens"]) + numberAny(u["input_tokens"]); v > 0 {
		out["cacheMissTokens"] = int(v)
	}
	return out
}

func extractSSEEvent(text, eventName string) map[string]any {
	for _, block := range strings.Split(text, "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		eventOK := false
		dataLines := []string{}
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "event:") && strings.TrimSpace(strings.TrimPrefix(line, "event:")) == eventName {
				eventOK = true
			}
			if strings.HasPrefix(line, "data:") {
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if eventOK && len(dataLines) > 0 {
			if parsed := parseJSONMap(strings.Join(dataLines, "\n")); parsed != nil {
				return parsed
			}
		}
	}
	return nil
}

func parseClaudeStream(text string) map[string]any {
	blocks := []map[string]any{}
	model := ""
	usage := map[string]any{}
	toolPartials := map[int]string{}
	for _, block := range strings.Split(text, "\n\n") {
		data := ""
		for _, line := range strings.Split(strings.TrimSpace(block), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data:") {
				data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
		if data == "" {
			continue
		}
		ev := parseJSONMap(data)
		if ev == nil {
			continue
		}
		switch ev["type"] {
		case "message_start":
			msg, _ := ev["message"].(map[string]any)
			model = common.AsString(msg["model"])
			if u, ok := msg["usage"].(map[string]any); ok {
				usage = u
			}
		case "content_block_start":
			idx := int(numberAny(ev["index"]))
			cb, _ := ev["content_block"].(map[string]any)
			for len(blocks) <= idx {
				blocks = append(blocks, map[string]any{"type": "ignored"})
			}
			if cb != nil {
				blocks[idx] = cb
			}
		case "content_block_delta":
			idx := int(numberAny(ev["index"]))
			delta, _ := ev["delta"].(map[string]any)
			if idx >= 0 && idx < len(blocks) && delta != nil {
				if delta["type"] == "text_delta" {
					blocks[idx]["text"] = common.AsString(blocks[idx]["text"]) + common.AsString(delta["text"])
				}
				if delta["type"] == "input_json_delta" {
					toolPartials[idx] += common.AsString(delta["partial_json"])
				}
			}
		case "content_block_stop":
			idx := int(numberAny(ev["index"]))
			if partial := toolPartials[idx]; partial != "" && idx >= 0 && idx < len(blocks) {
				args := map[string]any{}
				_ = json.Unmarshal([]byte(partial), &args)
				blocks[idx]["input"] = args
			}
		case "message_delta":
			if u, ok := ev["usage"].(map[string]any); ok {
				for k, v := range u {
					usage[k] = v
				}
			}
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	return map[string]any{"type": "message", "role": "assistant", "model": model, "content": anySlice(blocks), "usage": usage}
}

func parseJSONMap(text string) map[string]any {
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil
	}
	return out
}

func numberAny(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	default:
		return 0
	}
}

func anySlice(items []map[string]any) []any {
	out := make([]any, len(items))
	for i := range items {
		out[i] = items[i]
	}
	return out
}

func dataURLPayload(value string) string {
	if idx := strings.Index(value, ","); idx >= 0 {
		return value[idx+1:]
	}
	return value
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
