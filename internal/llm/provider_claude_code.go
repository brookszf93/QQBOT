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

func (c *LLMClient) callClaudeCode(ctx context.Context, req LLMChatRequest, conf config.LLMProviderConfig) (map[string]any, map[string]any, map[string]any, error) {
	token := c.bearerToken(conf)
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

func toClaudeCodeRequestBody(req LLMChatRequest) map[string]any {
	messages := []map[string]any{}
	systemParts := []map[string]any{}
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			if text := strings.TrimSpace(common.AsString(m.Content)); text != "" {
				systemParts = append(systemParts, map[string]any{"type": "text", "text": text})
			}
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
	system = append(system, systemParts...)
	if len(messages) == 0 {
		messages = append(messages, map[string]any{"role": "user", "content": "请根据当前系统状态继续运行；如果没有需要主动处理的内容，可以调用等待或保持沉默。"})
	}
	payload := map[string]any{
		"model":      req.Model,
		"stream":     true,
		"max_tokens": claudeMaxTokens(req.Model),
		"system":     system,
		"messages":   messages,
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

func fromClaudeMessageResponse(native map[string]any, fallbackModel string) (map[string]any, error) {
	if strings.HasPrefix(common.AsString(native["body"]), "event:") {
		if parsed := parseClaudeStream(common.AsString(native["body"])); parsed != nil {
			native = parsed
		}
	}
	content := ""
	reasoning := ""
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
			case "thinking":
				if text := firstStringValue(block, "thinking", "text"); text != "" {
					if reasoning != "" {
						reasoning += "\n"
					}
					reasoning += text
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
	out := map[string]any{"provider": "claude-code", "model": model, "message": map[string]any{"role": "assistant", "content": content, "toolCalls": toolCalls}, "usage": claudeUsage(native)}
	if strings.TrimSpace(reasoning) != "" {
		out["reasoning"] = reasoning
	}
	return out, nil
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
				if delta["type"] == "thinking_delta" {
					blocks[idx]["thinking"] = common.AsString(blocks[idx]["thinking"]) + common.AsString(delta["thinking"])
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
