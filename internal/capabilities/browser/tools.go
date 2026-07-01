package browser

import (
	"QqBot/internal/agentruntime"
	"QqBot/internal/capabilities/vision"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ScreenshotDescriber interface {
	Describe(context.Context, string, []vision.ImagePart) (string, error)
}

type SessionTools struct {
	Client              *Client
	SessionID           string
	ScreenshotDescriber ScreenshotDescriber
	ScreenshotMaxBytes  int64
	ScreenshotDir       string
}

func (s SessionTools) Catalog() *agentruntime.ToolCatalog {
	return agentruntime.NewToolCatalog(
		actionTool{session: s, name: "browser_navigate", description: "打开指定网页地址。", properties: map[string]any{
			"url": map[string]any{"type": "string", "description": "要打开的完整 http/https URL。"},
		}},
		actionTool{session: s, name: "browser_search", description: "在真实浏览器中搜索关键词。", properties: map[string]any{
			"query": map[string]any{"type": "string", "description": "搜索关键词。"},
		}},
		actionTool{session: s, name: "browser_read", description: "读取当前页面正文和可交互元素编号。", properties: map[string]any{}},
		actionTool{session: s, name: "browser_click", description: "点击当前页面元素，优先使用 browser_read 返回的 ref。", properties: map[string]any{
			"ref":      map[string]any{"type": "string", "description": "元素编号，例如 e12。"},
			"selector": map[string]any{"type": "string", "description": "可选 CSS 选择器。"},
			"text":     map[string]any{"type": "string", "description": "可选可见文本。"},
		}},
		actionTool{session: s, name: "browser_type", description: "向输入框填写文字，可选择填写后按回车。", properties: map[string]any{
			"ref":      map[string]any{"type": "string"},
			"selector": map[string]any{"type": "string"},
			"text":     map[string]any{"type": "string", "description": "要输入的文字。"},
			"submit":   map[string]any{"type": "boolean", "description": "输入后是否按 Enter。"},
			"clear":    map[string]any{"type": "boolean", "description": "输入前是否清空原内容，默认 true。"},
		}},
		actionTool{session: s, name: "browser_scroll", description: "滚动当前网页。", properties: map[string]any{
			"direction": map[string]any{"type": "string", "enum": []string{"up", "down", "top", "bottom"}},
			"amount":    map[string]any{"type": "integer", "description": "滚动像素，默认 700。"},
		}},
		actionTool{session: s, name: "browser_back", description: "返回浏览器历史中的上一页。", properties: map[string]any{}},
		actionTool{session: s, name: "browser_next_page", description: "尝试点击下一页、更多或继续按钮。", properties: map[string]any{}},
		actionTool{session: s, name: "browser_inspect_media", description: "检查页面中的音视频/直播播放器状态，可执行播放、暂停、静音和跳转。", properties: map[string]any{
			"command": map[string]any{"type": "string", "enum": []string{"inspect", "play", "pause", "mute", "unmute", "seek"}},
			"index":   map[string]any{"type": "integer", "description": "媒体元素序号，默认 0。"},
			"seconds": map[string]any{"type": "number", "description": "seek 的目标秒数。"},
		}},
		actionTool{session: s, name: "browser_wait", description: "等待页面加载、直播更新或新内容出现。", properties: map[string]any{
			"milliseconds": map[string]any{"type": "integer", "description": "等待毫秒数，最大 30000。"},
		}},
		actionTool{session: s, name: "browser_watch", description: "观看直播或动态页面一段时间，然后截取最新画面并用视觉模型分析。", properties: map[string]any{
			"milliseconds": map[string]any{"type": "integer", "description": "观察时长，最大 30000。"},
			"mode":         screenshotModeSchema(),
			"prompt":       map[string]any{"type": "string", "description": "希望重点观察的画面内容。"},
		}},
		actionTool{session: s, name: "browser_screenshot", description: "截取当前页面，可选择识图、保存为可发送到 QQ 的图片，或两者同时执行。", properties: map[string]any{
			"fullPage": map[string]any{"type": "boolean"},
			"mode":     screenshotModeSchema(),
			"analyze":  map[string]any{"type": "boolean", "description": "兼容旧参数；mode 未设置时，false 等同 send，true 等同 analyze。"},
			"prompt":   map[string]any{"type": "string", "description": "可选的画面分析重点。"},
		}},
		actionTool{session: s, name: "browser_close", description: "关闭当前浏览器会话；只有明确不再使用该会话时才调用。", properties: map[string]any{}},
		finalizeTool{},
	)
}

type actionTool struct {
	session     SessionTools
	name        string
	description string
	properties  map[string]any
}

func (t actionTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: t.name, Description: t.description, Parameters: agentruntime.ObjectSchema(t.properties)}
}

func (actionTool) Kind() string { return "business" }

func (t actionTool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	if t.session.Client == nil {
		return toolJSON(map[string]any{"ok": false, "error": "BROWSER_NOT_CONFIGURED"}), nil
	}
	action := strings.TrimPrefix(t.name, "browser_")
	result, err := t.session.Client.Do(ctx, ActionRequest{
		SessionID: t.session.SessionID,
		Action:    action,
		Arguments: call.Arguments,
	})
	if err != nil {
		return toolJSON(map[string]any{"ok": false, "error": "BROWSER_ACTION_FAILED", "message": err.Error()}), nil
	}
	if action == "screenshot" || action == "watch" {
		t.processScreenshot(ctx, call.Arguments, &result)
	}
	result.ScreenshotBase64 = ""
	return toolJSON(result), nil
}

func screenshotModeSchema() map[string]any {
	return map[string]any{
		"type":        "string",
		"enum":        []string{"analyze", "send", "both"},
		"description": "analyze 只识图；send 保存为可发送图片但不识图；both 同时识图并保存。默认 analyze。",
	}
}

func (t actionTool) processScreenshot(ctx context.Context, args map[string]any, result *ActionResponse) {
	if result.ScreenshotBase64 == "" {
		return
	}
	data, err := base64.StdEncoding.DecodeString(result.ScreenshotBase64)
	if err != nil {
		result.Message = appendMessage(result.Message, "截图解码失败："+err.Error())
		return
	}
	maxBytes := t.session.ScreenshotMaxBytes
	if maxBytes <= 0 {
		maxBytes = 8 << 20
	}
	if int64(len(data)) > maxBytes {
		result.Message = appendMessage(result.Message, fmt.Sprintf("截图超过处理上限 %d 字节", maxBytes))
		return
	}
	mode := screenshotMode(args)
	if mode == "send" || mode == "both" {
		path, err := t.saveScreenshot(data, result.ScreenshotMIME)
		if err != nil {
			result.Message = appendMessage(result.Message, "截图保存失败："+err.Error())
		} else {
			if result.Metadata == nil {
				result.Metadata = map[string]any{}
			}
			result.Metadata["imagePath"] = path
			result.Metadata["imagePurpose"] = "qq_attachment"
		}
	}
	if mode != "analyze" && mode != "both" {
		return
	}
	if t.session.ScreenshotDescriber == nil {
		result.Message = appendMessage(result.Message, "未配置截图视觉分析模型")
		return
	}
	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		prompt = "请描述当前网页截图中最重要的内容、页面状态、可见文字和正在播放的画面，简洁说明下一步可操作项。"
	}
	mimeType := result.ScreenshotMIME
	if mimeType == "" {
		mimeType = "image/png"
	}
	description, err := t.session.ScreenshotDescriber.Describe(ctx, prompt, []vision.ImagePart{{MimeType: mimeType, Data: data, Filename: "browser.png"}})
	if err != nil {
		result.Message = appendMessage(result.Message, "视觉分析失败："+err.Error())
		return
	}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["visualDescription"] = strings.TrimSpace(description)
}

func screenshotMode(args map[string]any) string {
	mode := strings.ToLower(strings.TrimSpace(stringArg(args["mode"])))
	switch mode {
	case "analyze", "send", "both":
		return mode
	}
	if analyze, exists := args["analyze"].(bool); exists && !analyze {
		return "send"
	}
	return "analyze"
}

func (t actionTool) saveScreenshot(data []byte, mimeType string) (string, error) {
	root := strings.TrimSpace(t.session.ScreenshotDir)
	if root == "" {
		root = "data/browser-screenshots"
	}
	rootPath, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		return "", err
	}
	extension := ".png"
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg":
		extension = ".jpg"
	case "image/gif":
		extension = ".gif"
	case "image/webp":
		extension = ".webp"
	case "image/bmp":
		extension = ".bmp"
	}
	sum := sha256.Sum256(data)
	name := fmt.Sprintf("browser-%s-%x%s", time.Now().Format("20060102-150405"), sum[:6], extension)
	path := filepath.Join(rootPath, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func appendMessage(current, next string) string {
	if strings.TrimSpace(current) == "" {
		return next
	}
	return current + "；" + next
}

type finalizeTool struct{}

func (finalizeTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "finalize_browser", Description: "浏览器任务完成后，把结果和当前页面状态提交给主智能体。", Parameters: agentruntime.ObjectSchema(map[string]any{
		"summary":   map[string]any{"type": "string", "description": "基于浏览器实际页面得到的中文结果。"},
		"url":       map[string]any{"type": "string", "description": "最终页面 URL。"},
		"title":     map[string]any{"type": "string", "description": "最终页面标题。"},
		"imagePath": map[string]any{"type": "string", "description": "需要让主智能体发送截图时，原样填写 browser_screenshot/browser_watch 返回的 metadata.imagePath；仅识图时留空。"},
	})}
}

func (finalizeTool) Kind() string { return "business" }

func (finalizeTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	return toolJSON(map[string]any{
		"ok":        true,
		"summary":   strings.TrimSpace(stringArg(call.Arguments["summary"])),
		"url":       strings.TrimSpace(stringArg(call.Arguments["url"])),
		"title":     strings.TrimSpace(stringArg(call.Arguments["title"])),
		"imagePath": strings.TrimSpace(stringArg(call.Arguments["imagePath"])),
	}), nil
}

func stringArg(value any) string {
	text, _ := value.(string)
	return text
}

func toolJSON(value any) agentruntime.ToolResult {
	data, _ := json.Marshal(value)
	return agentruntime.ToolResult{Kind: "business", Content: string(data)}
}
