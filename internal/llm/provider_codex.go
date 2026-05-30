package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"qqbot-ai/internal/common"
	"qqbot-ai/internal/config"
	"strings"
)

func (c *LLMClient) callOpenAICodex(ctx context.Context, req LLMChatRequest, conf config.LLMProviderConfig) (map[string]any, map[string]any, map[string]any, error) {
	token := c.bearerToken(conf)
	if token == "" {
		return nil, nil, nil, fmt.Errorf("openai-codex credentials are empty")
	}
	payload := toCodexRequestBody(req, "")
	nativeReq := common.JSONMap(payload)
	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, conf.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nativeReq, nil, nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "qqbot-ai/1.0")
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
		return nativeReq, nil, nativeResp, fmt.Errorf("openai-codex invalid SSE response: status=%s contentType=%q bytes=%d preview=%q", res.Status, res.Header.Get("Content-Type"), len(raw), trimLog(text, 300))
	}
	response, err := fromCodexCompletedEvent(completed, req.Model)
	if err != nil {
		return nativeReq, nil, nativeResp, err
	}
	return nativeReq, response, nativeResp, nil
}

func toCodexRequestBody(req LLMChatRequest, accountID string) map[string]any {
	input := []map[string]any{}
	systemParts := []string{}
	seenFunctionCalls := map[string]bool{}
	availableFunctionOutputs := map[string]bool{}
	for _, m := range req.Messages {
		if m.Role == "tool" && strings.TrimSpace(m.ToolCallID) != "" {
			availableFunctionOutputs[m.ToolCallID] = true
		}
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			if text := strings.TrimSpace(common.AsString(m.Content)); text != "" {
				systemParts = append(systemParts, text)
			}
		case "user":
			input = append(input, map[string]any{"role": "user", "content": codexContent(m.Content)})
		case "assistant":
			if common.AsString(m.Content) != "" {
				input = append(input, map[string]any{"role": "assistant", "content": common.AsString(m.Content)})
			}
			for _, tc := range m.ToolCalls {
				if strings.TrimSpace(tc.ID) == "" || strings.TrimSpace(tc.Name) == "" {
					continue
				}
				if !availableFunctionOutputs[tc.ID] {
					continue
				}
				args, _ := json.Marshal(tc.Arguments)
				seenFunctionCalls[tc.ID] = true
				input = append(input, map[string]any{"type": "function_call", "call_id": tc.ID, "name": tc.Name, "arguments": string(args)})
			}
		case "tool":
			if strings.TrimSpace(m.ToolCallID) == "" || !seenFunctionCalls[m.ToolCallID] {
				continue
			}
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
	if len(systemParts) > 0 {
		instructions += "\n\n" + strings.Join(systemParts, "\n\n")
	}
	if len(input) == 0 {
		input = append(input, map[string]any{"role": "user", "content": "请根据当前系统状态继续运行；如果没有需要主动处理的内容，可以调用等待或保持沉默。"})
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
		return codexContentParts(parts)
	case []map[string]any:
		items := make([]any, 0, len(parts))
		for _, part := range parts {
			items = append(items, part)
		}
		return codexContentParts(items)
	default:
		return content
	}
}

func codexContentParts(parts []any) []map[string]any {
	out := []map[string]any{}
	for _, part := range parts {
		m, ok := part.(map[string]any)
		if !ok {
			continue
		}
		switch common.AsString(m["type"]) {
		case "input_text":
			out = append(out, map[string]any{"type": "input_text", "text": common.AsString(m["text"])})
		case "input_image":
			out = append(out, map[string]any{"type": "input_image", "image_url": firstStringValue(m, "image_url", "dataUrl")})
		case "image":
			out = append(out, map[string]any{"type": "input_image", "image_url": m["dataUrl"]})
		default:
			out = append(out, map[string]any{"type": "input_text", "text": common.AsString(m["text"])})
		}
	}
	return out
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
	seed, _ := json.Marshal(map[string]any{"provider": "openai-codex", "accountId": accountID, "model": req.Model, "instructions": req.System, "tools": req.Tools, "toolChoice": req.ToolChoice})
	sum := sha256.Sum256(seed)
	return "codex-" + base64.RawURLEncoding.EncodeToString(sum[:])[:32]
}

func fromCodexCompletedEvent(event map[string]any, fallbackModel string) (map[string]any, error) {
	response, _ := event["response"].(map[string]any)
	if response == nil {
		return nil, fmt.Errorf("openai-codex missing response")
	}
	content := ""
	reasoning := ""
	toolCalls := []any{}
	appendText := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if content != "" {
			content += "\n"
		}
		content += text
	}
	appendReasoning := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if reasoning != "" {
			reasoning += "\n"
		}
		reasoning += text
	}
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
							switch part["type"] {
							case "output_text", "text":
								appendText(firstStringValue(part, "text", "output_text"))
							case "summary_text":
								appendReasoning(common.AsString(part["text"]))
							}
						}
					} else {
						appendText(common.AsString(item["content"]))
					}
				}
			case "output_text", "text":
				appendText(common.AsString(item["text"]))
			}
		}
	}
	appendText(firstStringValue(response, "output_text", "text"))
	if content == "" && len(toolCalls) == 0 {
		status := common.AsString(response["status"])
		incomplete, _ := response["incomplete_details"].(map[string]any)
		if status == "incomplete" || incomplete != nil {
			return nil, fmt.Errorf("openai-codex incomplete response: %v", incomplete)
		}
	}
	model := common.AsString(response["model"])
	if model == "" {
		model = fallbackModel
	}
	out := map[string]any{"provider": "openai-codex", "model": model, "message": map[string]any{"role": "assistant", "content": content, "toolCalls": toolCalls}, "usage": codexUsage(response)}
	if strings.TrimSpace(reasoning) != "" {
		out["reasoning"] = reasoning
	}
	return out, nil
}

func codexUsage(response map[string]any) map[string]any {
	u, _ := response["usage"].(map[string]any)
	if u == nil {
		return nil
	}
	out := map[string]any{"promptTokens": u["input_tokens"], "completionTokens": u["output_tokens"], "totalTokens": u["total_tokens"]}
	if details, ok := u["input_tokens_details"].(map[string]any); ok {
		out["cacheHitTokens"] = details["cached_tokens"]
	}
	return out
}

func extractSSEEvent(text, eventName string) map[string]any {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
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
