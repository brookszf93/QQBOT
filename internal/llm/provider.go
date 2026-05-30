package llm

import (
	"context"
	"fmt"
	"qqbot-ai/internal/config"
	"strings"
)

func (c *LLMClient) callProvider(ctx context.Context, req LLMChatRequest) (map[string]any, map[string]any, map[string]any, error) {
	conf := c.providerConfig(req.Provider)
	if req.Model == "" || !contains(conf.Models, req.Model) {
		return nil, nil, nil, fmt.Errorf("所选 LLM 模型未在当前 provider 中配置")
	}
	if conf.BaseURL == "" {
		return nil, nil, nil, fmt.Errorf("provider baseUrl is empty")
	}
	switch req.Provider {
	case "openai-codex":
		return c.callOpenAICodex(ctx, req, conf)
	case "claude-code":
		return c.callClaudeCode(ctx, req, conf)
	case "deepseek", "openai":
		if conf.APIKey == "" {
			return nil, nil, nil, fmt.Errorf("provider apiKey is empty")
		}
		return c.callOpenAIChat(ctx, req, conf)
	default:
		return nil, nil, nil, fmt.Errorf("unsupported provider: %s", req.Provider)
	}
}

func (c *LLMClient) bearerToken(conf config.LLMProviderConfig) string {
	return strings.TrimSpace(conf.APIKey)
}

func (c *LLMClient) providerConfig(id string) config.LLMProviderConfig {
	switch id {
	case "deepseek":
		return c.cfg.Server.LLM.Providers.Deepseek
	case "openai":
		return c.cfg.Server.LLM.Providers.OpenAI
	case "openai-codex":
		return c.cfg.Server.LLM.Providers.OpenAICodex
	case "claude-code":
		return c.cfg.Server.LLM.Providers.ClaudeCode
	default:
		return config.LLMProviderConfig{}
	}
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
