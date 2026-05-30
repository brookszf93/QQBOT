package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"qqbot-ai/internal/common"
	"qqbot-ai/internal/config"
	"strings"
)

func (c *LLMClient) callOpenAIChat(ctx context.Context, req LLMChatRequest, conf config.LLMProviderConfig) (map[string]any, map[string]any, map[string]any, error) {
	payload := toOpenAIChatPayload(req)
	nativeReq := common.JSONMap(payload)
	url := strings.TrimRight(conf.BaseURL, "/")
	if !strings.HasSuffix(url, "/chat/completions") && (req.Provider == "openai" || req.Provider == "deepseek") {
		url += "/chat/completions"
	}
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
		return nativeReq, nil, nativeResp, fmt.Errorf("LLM 上游服务调用失败: %s", res.Status)
	}
	response := fromOpenAIChatResponse(nativeResp, req.Provider, req.Model)
	return nativeReq, response, nativeResp, nil
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
			item["content"] = normalizeOpenAIContent(m.Content)
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
	if len(req.Tools) > 0 {
		tools := []map[string]any{}
		for _, tool := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters}})
		}
		payload["tools"] = tools
		payload["tool_choice"] = req.ToolChoice
	}
	return payload
}

func normalizeOpenAIContent(content any) any {
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
	toolCalls := []any{}
	if choices, ok := native["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if msg, ok := choice["message"].(map[string]any); ok {
				content = common.AsString(msg["content"])
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
	}
	return map[string]any{
		"provider": provider,
		"model":    model,
		"message":  map[string]any{"role": "assistant", "content": content, "toolCalls": toolCalls},
		"usage":    usage,
	}
}
