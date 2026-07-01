package agent

import (
	"QqBot/internal/agentruntime"
	browsercap "QqBot/internal/capabilities/browser"
	"QqBot/internal/common"
	"QqBot/internal/prompts"
	"context"
	"encoding/json"
	"strings"
)

// BrowserTaskAgentTool 是根 Agent 唯一可见的浏览器入口。
// 具体页面动作只在这个子 Agent 运行期间披露。
type BrowserTaskAgentTool struct {
	client              *browsercap.Client
	model               agentruntime.Model
	systemPrompt        func() string
	contextMessages     func() []agentruntime.Message
	defaultSessionID    string
	maxRounds           int
	screenshotDescriber browsercap.ScreenshotDescriber
	screenshotMaxBytes  int64
	screenshotDir       string
}

func NewBrowserTaskAgentTool(client *browsercap.Client, defaultSessionID string, maxRounds int, describer browsercap.ScreenshotDescriber, screenshotMaxBytes int64, screenshotDir string) *BrowserTaskAgentTool {
	if strings.TrimSpace(defaultSessionID) == "" {
		defaultSessionID = "default"
	}
	if maxRounds <= 0 {
		maxRounds = 12
	}
	return &BrowserTaskAgentTool{
		client:              client,
		defaultSessionID:    defaultSessionID,
		maxRounds:           maxRounds,
		screenshotDescriber: describer,
		screenshotMaxBytes:  screenshotMaxBytes,
		screenshotDir:       screenshotDir,
	}
}

func (t *BrowserTaskAgentTool) SetModel(model agentruntime.Model) {
	t.model = model
}

func (t *BrowserTaskAgentTool) SetTaskContext(systemPrompt func() string, contextMessages func() []agentruntime.Message) {
	t.systemPrompt = systemPrompt
	t.contextMessages = contextMessages
}

func (t *BrowserTaskAgentTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "browser", Description: "启动真实浏览器子 Agent，完成搜索、打开动态网页、点击、输入、翻页、查看直播或媒体状态等连续网页操作。", Parameters: agentruntime.ObjectSchema(map[string]any{
		"task":      map[string]any{"type": "string", "description": "需要浏览器完整完成的任务和期望结果。"},
		"url":       map[string]any{"type": "string", "description": "可选起始网页地址。"},
		"sessionId": map[string]any{"type": "string", "description": "可选持久会话 ID；同一 ID 会复用页面、Cookie 和登录状态。"},
	})}
}

func (*BrowserTaskAgentTool) Kind() string { return "business" }

func (t *BrowserTaskAgentTool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	task := strings.TrimSpace(common.AsString(call.Arguments["task"]))
	startURL := strings.TrimSpace(common.AsString(call.Arguments["url"]))
	sessionID := strings.TrimSpace(common.AsString(call.Arguments["sessionId"]))
	if sessionID == "" {
		sessionID = t.defaultSessionID
	}
	if task == "" && startURL == "" {
		return browserTaskJSON(map[string]any{"ok": false, "error": "BROWSER_TASK_REQUIRED", "message": "task 和 url 至少需要一个。"}), nil
	}
	if t.client == nil {
		return browserTaskJSON(map[string]any{"ok": false, "error": "BROWSER_DISABLED", "message": "浏览器 sidecar 未启用。"}), nil
	}
	if t.model == nil {
		return browserTaskJSON(map[string]any{"ok": false, "error": "BROWSER_AGENT_MODEL_MISSING", "message": "浏览器 Agent 模型未配置。"}), nil
	}

	tools := browsercap.SessionTools{
		Client:              t.client,
		SessionID:           sessionID,
		ScreenshotDescriber: t.screenshotDescriber,
		ScreenshotMaxBytes:  t.screenshotMaxBytes,
		ScreenshotDir:       t.screenshotDir,
	}.Catalog()
	systemPrompt := prompts.BrowserSystemPrompt()
	messages := []agentruntime.Message{{Role: "user", Content: prompts.BrowserInstruction(task, startURL, sessionID)}}
	if t.contextMessages != nil {
		messages = append(t.contextMessages(), agentruntime.Message{Role: "user", Content: prompts.BrowserInstruction(task, startURL, sessionID)})
	}
	kernel := agentruntime.ReActKernel{Model: t.model}
	lastImagePath := ""
	for round := 0; round < t.maxRounds; round++ {
		result, err := kernel.RunRound(ctx, agentruntime.RoundInput{
			SystemPrompt: systemPrompt,
			Messages:     messages,
			Tools:        tools,
			ToolChoice:   "required",
		})
		if err != nil {
			return agentruntime.ToolResult{}, err
		}
		messages = append(messages, result.Assistant)
		for _, execution := range result.ToolExecutions {
			if execution.Call.Name == "finalize_browser" {
				return agentruntime.ToolResult{Kind: "business", Content: mergeBrowserTaskResult(execution.Result.Content, sessionID, lastImagePath)}, nil
			}
			if imagePath := browserResultImagePath(execution.Result.Content); imagePath != "" {
				lastImagePath = imagePath
			}
			messages = append(messages, agentruntime.Message{Role: "tool", ToolCallID: execution.Call.ID, Content: execution.Result.Content})
		}
	}
	return browserTaskJSON(map[string]any{
		"ok":        false,
		"error":     "BROWSER_TASK_ROUND_LIMIT",
		"message":   "浏览器任务达到最大操作轮数，页面会话仍被保留，可用同一 sessionId 继续。",
		"sessionId": sessionID,
	}), nil
}

func mergeBrowserTaskResult(content, sessionID, fallbackImagePath string) string {
	var payload map[string]any
	if json.Unmarshal([]byte(content), &payload) != nil {
		payload = map[string]any{"ok": true, "summary": content}
	}
	payload["sessionId"] = sessionID
	if strings.TrimSpace(common.AsString(payload["imagePath"])) == "" && strings.TrimSpace(fallbackImagePath) != "" {
		payload["imagePath"] = fallbackImagePath
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

func browserResultImagePath(content string) string {
	var payload map[string]any
	if json.Unmarshal([]byte(content), &payload) != nil {
		return ""
	}
	metadata, _ := payload["metadata"].(map[string]any)
	return strings.TrimSpace(common.AsString(metadata["imagePath"]))
}

func browserTaskJSON(value map[string]any) agentruntime.ToolResult {
	data, _ := json.Marshal(value)
	return agentruntime.ToolResult{Kind: "business", Content: string(data)}
}
