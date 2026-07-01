package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ToolDefinition 是暴露给 LLM 的工具函数 schema。
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolCall 是模型请求的调用，参数已从 JSON 解码。
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// NormalizeToolCall applies compatibility repairs to model tool calls before
// logging, persistence, and execution.
func NormalizeToolCall(call ToolCall) ToolCall {
	call.Arguments = withoutOSArgument(call.Arguments)
	if call.Name == "send_message" && strings.EqualFold(strings.TrimSpace(asString(call.Arguments["action"])), "wait") {
		call.Name = "wait"
		delete(call.Arguments, "action")
	}
	call = normalizeLegacyRootToolCall(call)
	return call
}

func normalizeLegacyRootToolCall(call ToolCall) ToolCall {
	args := cloneArguments(call.Arguments)
	switch strings.TrimSpace(call.Name) {
	case "act":
		action := legacyRootActionName(asString(args["action"]))
		if action == "" && strings.TrimSpace(asString(args["message"])) != "" {
			action = "send_message"
		}
		if action == "" {
			return call
		}
		delete(args, "action")
		call.Name = action
		call.Arguments = args
		return call
	case "invoke":
		action := ""
		if tool := strings.TrimSpace(asString(args["tool"])); tool != "" {
			action = legacyRootActionName(tool)
			if action == "" {
				action = tool
			}
		}
		if action == "" {
			return call
		}
		delete(args, "tool")
		delete(args, "toolName")
		call.Name = action
		call.Arguments = args
		return call
	default:
		if alias := legacyRootAlias(call.Name); alias != "" {
			call.Name = alias
		}
		return call
	}
}

func legacyRootActionName(name string) string {
	switch strings.TrimSpace(name) {
	case "wait", "send_message", "analyze_image", "detect_ai_tone", "browser", "search_web", "search_memory", "searchMagnetFromWeb", "open_ithome_article", "enter", "back_to_portal", "help", "personal_screen", "todo_app", "novel_app", "project_app", "music_app", "news_app", "calculate", "bash", "read_bash_output":
		return strings.TrimSpace(name)
	case "send", "sendMessage", "send_group_message", "send_private_message":
		return "send_message"
	case "searchMagnet", "search_magnet", "magnet_search":
		return "searchMagnetFromWeb"
	case "open_ithome", "ithome", "open_article":
		return "open_ithome_article"
	case "ai_tone", "detectAI":
		return "detect_ai_tone"
	case "back", "portal":
		return "back_to_portal"
	default:
		return ""
	}
}

func legacyRootAlias(name string) string {
	switch strings.TrimSpace(name) {
	case "send", "sendMessage", "send_group_message", "send_private_message":
		return "send_message"
	case "searchMagnet", "search_magnet", "magnet_search":
		return "searchMagnetFromWeb"
	case "open_ithome", "ithome", "open_article":
		return "open_ithome_article"
	case "ai_tone", "detectAI":
		return "detect_ai_tone"
	case "back", "portal":
		return "back_to_portal"
	default:
		return ""
	}
}

func cloneArguments(arguments map[string]any) map[string]any {
	if len(arguments) == 0 {
		return map[string]any{}
	}
	next := make(map[string]any, len(arguments))
	for key, value := range arguments {
		next[key] = value
	}
	return next
}

func withoutOSArgument(arguments map[string]any) map[string]any {
	if len(arguments) == 0 {
		return arguments
	}
	if _, ok := arguments["OS"]; !ok {
		if _, ok := arguments["os"]; !ok {
			return arguments
		}
	}
	next := make(map[string]any, len(arguments))
	for key, value := range arguments {
		if strings.EqualFold(key, "os") {
			continue
		}
		next[key] = value
	}
	return next
}

// ToolResult 是工具执行后返回给模型的内容。
type ToolResult struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

// Tool 是控制类和业务类能力共用的接口。
type Tool interface {
	Definition() ToolDefinition
	Kind() string
	Execute(context.Context, ToolCall) (ToolResult, error)
}

// ToolCatalog 按名称保存工具，并为提示词保留声明顺序。
type ToolCatalog struct {
	tools    map[string]Tool
	order    []string
	observer ToolExecutionObserver
}

type ToolExecutionObserver interface {
	BeforeTool(context.Context, ToolCall, ToolDefinition, string) (*ToolResult, error)
	AfterTool(context.Context, ToolCall, ToolDefinition, ToolResult, error)
}

// NewToolCatalog 根据零个或多个工具构建目录。
func NewToolCatalog(tools ...Tool) *ToolCatalog {
	c := &ToolCatalog{tools: map[string]Tool{}}
	for _, tool := range tools {
		c.Add(tool)
	}
	return c
}

// Add 按工具定义名称注册或替换工具。
func (c *ToolCatalog) Add(tool Tool) {
	name := tool.Definition().Name
	if _, exists := c.tools[name]; !exists {
		c.order = append(c.order, name)
	}
	c.tools[name] = tool
}

// Get 按名称返回工具。
func (c *ToolCatalog) Get(name string) (Tool, bool) {
	tool, ok := c.tools[name]
	return tool, ok
}

func (c *ToolCatalog) SetObserver(observer ToolExecutionObserver) {
	c.observer = observer
}

// Definitions 按稳定顺序返回面向 LLM 的 schema。
func (c *ToolCatalog) Definitions() []ToolDefinition {
	out := make([]ToolDefinition, 0, len(c.order))
	for _, name := range c.order {
		out = append(out, c.tools[name].Definition())
	}
	return out
}

// Pick 创建只包含指定工具名称的较小目录。
func (c *ToolCatalog) Pick(names ...string) *ToolCatalog {
	next := NewToolCatalog()
	for _, name := range names {
		if tool, ok := c.tools[name]; ok {
			next.Add(tool)
		}
	}
	return next
}

// Execute 分发模型工具调用，并把普通错误转换成 JSON 工具内容。
func (c *ToolCatalog) Execute(ctx context.Context, call ToolCall) (ToolResult, error) {
	tool, ok := c.Get(call.Name)
	if !ok {
		return ToolResult{Kind: "control", Content: mustJSON(map[string]any{"ok": false, "error": "UNKNOWN_TOOL", "toolName": call.Name})}, nil
	}
	definition := tool.Definition()
	if c.observer != nil {
		prior, err := c.observer.BeforeTool(ctx, call, definition, tool.Kind())
		if err != nil {
			return ToolResult{Kind: tool.Kind(), Content: mustJSON(map[string]any{"ok": false, "error": "TOOL_LEASE_FAILED", "toolName": call.Name, "message": err.Error()})}, nil
		}
		if prior != nil {
			return *prior, nil
		}
	}
	result, err := tool.Execute(ctx, call)
	if c.observer != nil {
		c.observer.AfterTool(ctx, call, definition, result, err)
	}
	if err != nil {
		return ToolResult{Kind: tool.Kind(), Content: mustJSON(map[string]any{"ok": false, "error": "TOOL_FAILED", "toolName": call.Name, "message": err.Error()})}, nil
	}
	return result, nil
}

// ObjectSchema 创建工具定义使用的 JSON Schema 外壳。
func ObjectSchema(properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": properties}
}

func asString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		return ""
	}
}

func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"ok":false,"error":"JSON_ENCODE_FAILED","message":%q}`, err.Error())
	}
	return string(data)
}
