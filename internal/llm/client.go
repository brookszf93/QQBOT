package llm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"qqbot-ai/internal/capabilities/vision"
	"qqbot-ai/internal/common"
	"qqbot-ai/internal/config"
	"qqbot-ai/internal/db"
	"strings"
	"time"
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
	System     string       `json:"system,omitempty"`
	Messages   []LLMMessage `json:"messages"`
	Tools      []LLMTool    `json:"tools"`
	ToolChoice any          `json:"toolChoice"`
}

// LLMMessage 是便于 JSON 处理和供应商映射的聊天消息。
type LLMMessage struct {
	Role       string        `json:"role"`
	Content    any           `json:"content"`
	ToolCalls  []LLMToolCall `json:"toolCalls,omitempty"`
	ToolCallID string        `json:"toolCallId,omitempty"`
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

// Describe 实现 vision.Client，用 vision usage 对图片生成中文摘要。
func (c *LLMClient) Describe(ctx context.Context, prompt string, images []vision.ImagePart) (string, error) {
	parts := []map[string]any{{"type": "text", "text": prompt}}
	for _, image := range images {
		mimeType := image.MimeType
		if mimeType == "" {
			mimeType = "image/png"
		}
		dataURL := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(image.Data)
		parts = append(parts, map[string]any{"type": "image", "dataUrl": dataURL})
	}
	resp, err := c.ChatUsage(ctx, "vision", LLMChatRequest{
		System:   prompt,
		Messages: []LLMMessage{{Role: "user", Content: parts}},
	})
	if err != nil {
		return "", err
	}
	message, _ := resp["message"].(map[string]any)
	return common.AsString(message["content"]), nil
}

func (c *LLMClient) ListProviders(usage string) map[string]any {
	providers := []map[string]any{}
	preferred := ""
	if u, ok := c.cfg.Server.LLM.Usages[usage]; ok && len(u.Attempts) > 0 {
		preferred = u.Attempts[0].Provider
	}
	for _, id := range []string{"deepseek", "openai", "openai-codex", "claude-code"} {
		conf := c.providerConfig(id)
		if len(conf.Models) == 0 {
			continue
		}
		if (id == "deepseek" || id == "openai") && strings.TrimSpace(conf.APIKey) == "" {
			continue
		}
		if (id == "openai-codex" || id == "claude-code") && c.bearerToken(conf) == "" {
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
	requestSummary := summarizeLLMRequest(req)
	nativeRequestSummary := summarizeNativePayload(nativeReq)
	nativeResponseSummary := summarizeNativePayload(nativeResp)
	if err != nil {
		c.store.AddLlmCall(db.LlmCallItem{
			RequestID: requestID, Seq: 1, Provider: provider, Model: model, Status: "failed", LatencyMs: &latency,
			RequestPayload: requestSummary, NativeRequestPayload: nativeRequestSummary, NativeResponsePayload: nativeResponseSummary,
			Error: map[string]any{"name": "LLMError", "message": err.Error()},
		})
		return nil, http.StatusBadGateway, err
	}
	if os := extractResponseOS(response); os != "" {
		response["os"] = os
	}
	c.store.AddLlmCall(db.LlmCallItem{
		RequestID: requestID, Seq: 1, Provider: provider, Model: model, Status: "success", LatencyMs: &latency,
		RequestPayload: requestSummary, ResponsePayload: summarizeLLMResponse(response), NativeRequestPayload: nativeRequestSummary, NativeResponsePayload: nativeResponseSummary,
	})
	response["nativeRequestPayload"] = nativeRequestSummary
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
			log.Printf("[LLM] usage=%s attempt=%d/%d provider=%s model=%s messages=%d tools=%d", usage, i+1, times, req.Provider, req.Model, len(req.Messages), len(req.Tools))
			resp, _, err := c.ChatDirect(ctx, req)
			if err == nil {
				if message, _ := resp["message"].(map[string]any); message != nil {
					log.Printf("[LLM] usage=%s provider=%s model=%s content=%q toolCalls=%s", usage, common.AsString(resp["provider"]), common.AsString(resp["model"]), trimLog(common.AsString(message["content"]), 500), toolCallNames(message["toolCalls"]))
					if os := trimLog(common.AsString(resp["os"]), 500); strings.TrimSpace(os) != "" {
						log.Printf("[LLM] usage=%s provider=%s model=%s os=%q", usage, common.AsString(resp["provider"]), common.AsString(resp["model"]), os)
					}
					if c.cfg != nil && c.cfg.Server.LLM.DebugReasoning {
						if reasoning := trimLog(common.AsString(resp["reasoning"]), 500); strings.TrimSpace(reasoning) != "" {
							log.Printf("[LLM] usage=%s provider=%s model=%s reasoning=%q", usage, common.AsString(resp["provider"]), common.AsString(resp["model"]), reasoning)
						}
					}
				} else {
					log.Printf("[LLM] usage=%s provider=%s model=%s success", usage, req.Provider, req.Model)
				}
				return resp, nil
			}
			log.Printf("[LLM] usage=%s provider=%s model=%s failed: %v", usage, req.Provider, req.Model, err)
			last = err
		}
	}
	if last == nil {
		last = errors.New("no LLM attempts configured")
	}
	return nil, last
}
