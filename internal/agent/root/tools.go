package root

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"QqBot/internal/agentruntime"
)

type WaitTool struct {
	MaxWait    time.Duration
	WaitSignal func(context.Context) bool
}

func (WaitTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "wait", Description: "沉默并等待新的外部事件。", Parameters: agentruntime.ObjectSchema(map[string]any{})}
}

func (WaitTool) Kind() string { return "control" }

func (t WaitTool) Execute(ctx context.Context, _ agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	if t.WaitSignal != nil {
		waitCtx := ctx
		cancel := func() {}
		if t.MaxWait > 0 {
			waitCtx, cancel = context.WithTimeout(ctx, t.MaxWait)
		}
		defer cancel()
		t.WaitSignal(waitCtx)
	}
	return agentruntime.ToolResult{Kind: "control", Content: "休息结束了"}, nil
}

// EnterTool switches into an app or exclusive tool environment.
type EnterTool struct{ Session *Session }

func (EnterTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "enter", Description: "进入需要独占工具环境的 App；聊天、私聊和新闻不需要进入。", Parameters: agentruntime.ObjectSchema(map[string]any{
		"id": map[string]any{"type": "string", "description": `App id: "todo", "novel", "projects", "browser", "music", "news", "calc", or "terminal".`, "enum": []string{"todo", "novel", "projects", "browser", "music", "news", "calc", "terminal"}},
	})}
}
func (EnterTool) Kind() string { return "business" }
func (t EnterTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	id := normalizeEnterArguments(call.Arguments)
	if t.Session == nil {
		data, _ := json.Marshal(map[string]any{"ok": true, "entered": id})
		return agentruntime.ToolResult{Kind: "control", Content: string(data)}, nil
	}
	if id == "" {
		if currentApp := t.Session.App(); currentApp != "" {
			data, _ := json.Marshal(t.Session.EnterApp(currentApp))
			return agentruntime.ToolResult{Kind: "control", Content: string(data)}, nil
		}
	}
	if isAppID(id) {
		data, _ := json.Marshal(t.Session.EnterApp(id))
		return agentruntime.ToolResult{Kind: "control", Content: string(data)}, nil
	}
	data, _ := json.Marshal(t.Session.Enter(id))
	return agentruntime.ToolResult{Kind: "control", Content: string(data)}, nil
}

// BackToPortalTool moves one navigation step back.
type BackToPortalTool struct{ Session *Session }

func (BackToPortalTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "back", Description: "退出当前焦点并返回上一层。", Parameters: agentruntime.ObjectSchema(map[string]any{})}
}
func (BackToPortalTool) Kind() string { return "business" }
func (t BackToPortalTool) Execute(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	if t.Session == nil {
		return agentruntime.ToolResult{Kind: "control", Content: `{"ok":true,"stateId":"portal"}`}, nil
	}
	data, _ := json.Marshal(t.Session.Back())
	return agentruntime.ToolResult{Kind: "control", Content: string(data)}, nil
}

// AppBackToPortalTool exits the current app and returns to the portal.
type AppBackToPortalTool struct{ Session *Session }

func (AppBackToPortalTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "back_to_portal", Description: "退出当前 App 返回桌面。", Parameters: agentruntime.ObjectSchema(map[string]any{})}
}
func (AppBackToPortalTool) Kind() string { return "business" }
func (t AppBackToPortalTool) Execute(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	if t.Session == nil {
		return agentruntime.ToolResult{Kind: "control", Content: `{"ok":false,"error":"SESSION_UNAVAILABLE"}`}, nil
	}
	data, _ := json.Marshal(t.Session.BackToPortal())
	return agentruntime.ToolResult{Kind: "control", Content: string(data)}, nil
}

// HelpTool returns a concise description of the current app.
type HelpTool struct{ Session *Session }

func (HelpTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "help", Description: "查看当前 App 的能力说明。", Parameters: agentruntime.ObjectSchema(map[string]any{})}
}
func (HelpTool) Kind() string { return "business" }
func (t HelpTool) Execute(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	currentApp := ""
	if t.Session != nil {
		t.Session.mu.Lock()
		currentApp = t.Session.CurrentApp
		t.Session.mu.Unlock()
	}
	if currentApp == "" {
		return agentruntime.ToolResult{Kind: "control", Content: "你不在任何 App 里。可用 enter 进入 todo、novel、projects、browser、music、news、calc 或 terminal。"}, nil
	}
	return agentruntime.ToolResult{Kind: "control", Content: "当前在 " + currentApp + " App。使用 personal_screen 查看状态；完成后可 back_to_portal。"}, nil
}

type ActTool struct {
	Wait   WaitTool
	Invoke InvokeTool
}

func (ActTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{
		Name:        "act",
		Description: "统一行动入口。选择 action 后分发到 wait、send_message、analyze_image、detect_ai_tone、browser、search_web、searchMagnetFromWeb、open_ithome_article 或个人 App。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"description": "本轮动作。沉默选 wait；发言选 send_message；其它能力填对应 action。",
					"enum": []string{
						"wait",
						"enter",
						"back_to_portal",
						"help",
						"send_message",
						"analyze_image",
						"detect_ai_tone",
						"browser",
						"search_web",
						"search_memory",
						"searchMagnetFromWeb",
						"open_ithome_article",
						"personal_screen",
						"todo_app",
						"novel_app",
						"project_app",
						"music_app",
						"news_app",
						"activity_app",
						"calculate",
						"bash",
						"read_bash_output",
					},
				},
				"message":     map[string]any{"type": "string", "description": "action=send_message 时要发送到 QQ 的文本。"},
				"targetType":  map[string]any{"type": "string", "enum": []string{"group", "private"}, "description": "action=send_message 时的回复路由类型。"},
				"targetId":    map[string]any{"type": "string", "description": "action=send_message 时的回复路由 ID。"},
				"imagePath":   map[string]any{"type": "string", "description": "action=send_message 时可发送的受控本地图片路径，或图片分析工具需要的路径。"},
				"messageId":   map[string]any{"type": "integer", "description": "action=analyze_image 时可指定 QQ 消息 ID。"},
				"text":        map[string]any{"type": "string", "description": "action=detect_ai_tone 时要检测的草稿；App action 时也可作为正文、笔记或任务文本。"},
				"threshold":   map[string]any{"type": "number", "description": "action=detect_ai_tone 时的 AI 腔调阈值，默认 0.65。"},
				"query":       map[string]any{"type": "string", "description": "搜索、浏览器、记忆检索或 enter app id。"},
				"articleId":   map[string]any{"type": "integer", "description": "action=open_ithome_article 时的文章 ID。"},
				"prompt":      map[string]any{"type": "string", "description": "图片、浏览器等工具的可选任务说明。"},
				"action_text": map[string]any{"type": "string", "description": "App 子动作，例如 novel_app 的 upsert_entry/screen/create_project/open_project/append_draft。"},
				"projectId":   map[string]any{"type": "string", "description": "App 项目 ID。"},
				"title":       map[string]any{"type": "string", "description": "创建或选择项目时的标题。"},
				"todoId":      map[string]any{"type": "string", "description": "待办 ID。"},
				"dueAt":       map[string]any{"type": "string", "description": "todo 截止时间，可选。"},
				"status":      map[string]any{"type": "string", "description": "状态，可选：open/done/paused/dropped。"},
			},
			"additionalProperties": true,
		},
	}
}

func (ActTool) Kind() string { return "business" }

func (t ActTool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	action := canonicalActAction(commonString(call.Arguments["action"]))
	if action == "" && strings.TrimSpace(commonString(call.Arguments["message"])) != "" {
		action = "send_message"
	}
	if action == "" {
		return jsonToolResult("business", map[string]any{
			"ok":      false,
			"error":   "ACT_ACTION_REQUIRED",
			"message": "act 需要先选择 action；如果不该说话，请选择 wait。",
		}), nil
	}
	if action == "wait" {
		return t.Wait.Execute(ctx, agentruntime.ToolCall{
			ID:        call.ID + ":wait",
			Name:      "wait",
			Arguments: actDelegatedArguments(call.Arguments, action),
		})
	}
	args := actDelegatedArguments(call.Arguments, action)
	args["tool"] = action
	result, err := t.Invoke.Execute(ctx, agentruntime.ToolCall{
		ID:        call.ID + ":act",
		Name:      "invoke",
		Arguments: args,
	})
	if err != nil || action != "enter" {
		return result, err
	}
	return t.enterWithScreen(ctx, call, result)
}

func (t ActTool) enterWithScreen(ctx context.Context, call agentruntime.ToolCall, enterResult agentruntime.ToolResult) (agentruntime.ToolResult, error) {
	var payload map[string]any
	if json.Unmarshal([]byte(enterResult.Content), &payload) != nil || payload["ok"] != true {
		return enterResult, nil
	}
	app := commonString(payload["enteredApp"])
	if app == "" {
		return enterResult, nil
	}
	screenArgs := map[string]any{"tool": "personal_screen", "app": app}
	screenResult, err := t.Invoke.Execute(ctx, agentruntime.ToolCall{
		ID:        call.ID + ":screen",
		Name:      "invoke",
		Arguments: screenArgs,
	})
	if err != nil {
		return enterResult, nil
	}
	payload["screenResult"] = screenResult.Content
	return jsonToolResult(enterResult.Kind, payload), nil
}

func canonicalActAction(action string) string {
	action = strings.TrimSpace(action)
	switch action {
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
		return action
	}
}

func actDelegatedArguments(source map[string]any, action string) map[string]any {
	args := make(map[string]any, len(source)+1)
	for key, value := range source {
		if key == "action" {
			continue
		}
		args[key] = value
	}
	if _, ok := args["action"]; !ok && isAppAction(action) {
		for _, key := range []string{"action_text", "subaction", "operation", "op"} {
			if value := commonString(args[key]); strings.TrimSpace(value) != "" {
				args["action"] = normalizeAppSubaction(action, value)
				break
			}
		}
	}
	return args
}

func isAppAction(action string) bool {
	switch action {
	case "todo_app", "novel_app", "project_app", "music_app", "news_app", "activity_app":
		return true
	default:
		return false
	}
}

func normalizeAppSubaction(appAction, subaction string) string {
	subaction = strings.TrimSpace(subaction)
	switch appAction {
	case "novel_app":
		switch subaction {
		case "list":
			return "list_projects"
		case "create":
			return "create_project"
		case "open":
			return "open_project"
		case "draft", "write":
			return "append_draft"
		case "note":
			return "append_note"
		case "outline":
			return "update_outline"
		}
	case "project_app":
		switch subaction {
		case "list_projects":
			return "list"
		case "create_project":
			return "create"
		case "note":
			return "append_note"
		case "journal":
			return "append_journal"
		}
	}
	return subaction
}

// InvokeTool dispatches act actions to owned business tools.
type InvokeTool struct {
	Owners []InvokeSubtoolOwner
}

type InvokeGuard struct {
	OK      bool
	Error   string
	Message string
	Extras  map[string]any
}

type InvokeSubtoolOwner interface {
	ListOwnedTools() []agentruntime.ToolDefinition
	CanInvokeNow(name string) InvokeGuard
	ExecuteSubtool(context.Context, string, map[string]any, agentruntime.ToolCall) (agentruntime.ToolResult, error)
}

type CatalogSubtoolOwner struct {
	Tools           *agentruntime.ToolCatalog
	Session         *Session
	AlwaysAvailable map[string]bool
}

type DirectSubtool struct {
	Owner           CatalogSubtoolOwner
	DefinitionValue agentruntime.ToolDefinition
	ToolKind        string
	CheckPermission bool
}

func (t DirectSubtool) Definition() agentruntime.ToolDefinition { return t.DefinitionValue }
func (t DirectSubtool) Kind() string                            { return t.ToolKind }
func (t DirectSubtool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	if t.CheckPermission {
		if guard := t.Owner.CanInvokeNow(t.DefinitionValue.Name); !guard.OK {
			return invokeGuardResult(t.DefinitionValue.Name, guard), nil
		}
	}
	args := call.Arguments
	if isAppAction(t.DefinitionValue.Name) {
		args = actDelegatedArguments(call.Arguments, t.DefinitionValue.Name)
	}
	return t.Owner.ExecuteSubtool(ctx, t.DefinitionValue.Name, args, call)
}

func (o CatalogSubtoolOwner) ListOwnedTools() []agentruntime.ToolDefinition {
	if o.Tools == nil {
		return nil
	}
	return o.Tools.Definitions()
}

func (o CatalogSubtoolOwner) CanInvokeNow(name string) InvokeGuard {
	if o.Session == nil || o.Session.IsToolAvailable(name) || o.AlwaysAvailable[name] {
		return InvokeGuard{OK: true}
	}
	availableNames := append([]string(nil), o.Session.AvailableTools()...)
	for toolName, available := range o.AlwaysAvailable {
		if available {
			availableNames = append(availableNames, toolName)
		}
	}
	definitions := filterToolDefinitions(o.ListOwnedTools(), availableNames)
	return InvokeGuard{
		OK:      false,
		Error:   "INVOKE_TOOL_NOT_AVAILABLE",
		Message: availableInvokeToolsDescription(definitions),
		Extras:  map[string]any{"availableTools": definitionNames(definitions)},
	}
}

func (o CatalogSubtoolOwner) ExecuteSubtool(ctx context.Context, name string, args map[string]any, parent agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	if o.Tools == nil {
		return agentruntime.ToolResult{Kind: "business", Content: `{"ok":false,"error":"NO_TOOLS"}`}, nil
	}
	if o.Session != nil && name == "send_message" {
		if target := o.Session.CurrentChatTarget(); target != nil {
			targetType := strings.TrimSpace(commonString(args["targetType"]))
			targetID := strings.TrimSpace(commonString(args["targetId"]))
			if targetType == "" && targetID == "" {
				args["targetType"] = target.Type
				args["targetId"] = target.ID
			}
		}
	}
	return o.Tools.Execute(ctx, agentruntime.ToolCall{ID: parent.ID + ":invoke", Name: name, Arguments: args})
}

func (InvokeTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{
		Name:        "invoke",
		Description: "内部 action 分发器。主模型应调用 act，不要直接调用本工具。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tool": map[string]any{"type": "string", "description": "要分发到的 action 名。"},
			},
			"additionalProperties": true,
		},
	}
}
func (InvokeTool) Kind() string { return "business" }
func (t InvokeTool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	name, _ := call.Arguments["tool"].(string)
	if name == "" {
		name, _ = call.Arguments["toolName"].(string)
	}
	args, _ := call.Arguments["arguments"].(map[string]any)
	if args == nil {
		if raw, ok := call.Arguments["arguments"].(string); ok && strings.TrimSpace(raw) != "" {
			_ = json.Unmarshal([]byte(raw), &args)
		}
		if args == nil {
			args = map[string]any{}
			for key, value := range call.Arguments {
				if key == "tool" || key == "toolName" || key == "arguments" {
					continue
				}
				args[key] = value
			}
		}
	}
	if name == "" && strings.TrimSpace(commonString(args["message"])) != "" {
		name = "send_message"
	}
	ownerByTool := map[string]InvokeSubtoolOwner{}
	definitionByTool := map[string]agentruntime.ToolDefinition{}
	for _, owner := range t.Owners {
		for _, definition := range owner.ListOwnedTools() {
			if _, exists := ownerByTool[definition.Name]; exists {
				data, _ := json.Marshal(map[string]any{"ok": false, "error": "DUPLICATE_INVOKE_TOOL", "tool": definition.Name})
				return agentruntime.ToolResult{Kind: "business", Content: string(data)}, nil
			}
			ownerByTool[definition.Name] = owner
			definitionByTool[definition.Name] = definition
		}
	}
	owner := ownerByTool[name]
	if owner == nil {
		definitions := make([]agentruntime.ToolDefinition, 0, len(definitionByTool))
		for _, definition := range definitionByTool {
			definitions = append(definitions, definition)
		}
		data, _ := json.Marshal(map[string]any{"ok": false, "error": "ACTION_NOT_FOUND", "tool": name, "message": "action " + name + " 不存在。\n" + availableInvokeToolsDescription(definitions), "availableTools": definitionNames(definitions)})
		return agentruntime.ToolResult{Kind: "business", Content: string(data)}, nil
	}
	guard := owner.CanInvokeNow(name)
	if !guard.OK {
		return invokeGuardResult(name, guard), nil
	}
	result, err := owner.ExecuteSubtool(ctx, name, args, call)
	if err != nil {
		return result, err
	}
	result.Content = enrichSubtoolFailureContent(name, result.Content, definitionByTool[name])
	return result, nil
}

func invokeGuardResult(name string, guard InvokeGuard) agentruntime.ToolResult {
	payload := map[string]any{"ok": false, "error": guard.Error, "tool": name, "message": guard.Message}
	for key, value := range guard.Extras {
		payload[key] = value
	}
	data, _ := json.Marshal(payload)
	return agentruntime.ToolResult{Kind: "business", Content: string(data)}
}

func jsonToolResult(kind string, payload map[string]any) agentruntime.ToolResult {
	data, _ := json.Marshal(payload)
	return agentruntime.ToolResult{Kind: kind, Content: string(data)}
}

func filterToolDefinitions(definitions []agentruntime.ToolDefinition, names []string) []agentruntime.ToolDefinition {
	allowed := map[string]bool{}
	for _, name := range names {
		allowed[name] = true
	}
	out := []agentruntime.ToolDefinition{}
	for _, definition := range definitions {
		if allowed[definition.Name] {
			out = append(out, definition)
		}
	}
	return out
}

func definitionNames(definitions []agentruntime.ToolDefinition) []string {
	out := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, definition.Name)
	}
	return out
}

func availableInvokeToolsDescription(definitions []agentruntime.ToolDefinition) string {
	if len(definitions) == 0 {
		return "当前没有可用 action。"
	}
	return "当前可用 action 说明：\n" + renderInvokeToolGuide(definitions)
}

func enrichSubtoolFailureContent(name, content string, definition agentruntime.ToolDefinition) string {
	var data map[string]any
	if err := json.Unmarshal([]byte(content), &data); err != nil {
		return content
	}
	if data["ok"] != false && commonString(data["error"]) == "" {
		return content
	}
	message := commonString(data["message"])
	if message == "" {
		if errName := commonString(data["error"]); errName != "" {
			message = "action " + name + " 调用失败：" + errName + "。"
		} else {
			message = "action " + name + " 调用失败。"
		}
	}
	if definition.Name != "" {
		message += "\n当前 action 说明：\n" + renderInvokeToolGuide([]agentruntime.ToolDefinition{definition})
	}
	data["message"] = message
	encoded, _ := json.Marshal(data)
	return string(encoded)
}

func renderInvokeToolGuide(definitions []agentruntime.ToolDefinition) string {
	lines := []string{}
	for _, definition := range definitions {
		params := []string{}
		if properties, ok := definition.Parameters["properties"].(map[string]any); ok {
			params = orderedParameterNames(properties)
		}
		if len(params) > 0 {
			lines = append(lines, fmt.Sprintf("- %s：%s。参数：%s。", definition.Name, definition.Description, strings.Join(params, "、")))
		} else {
			lines = append(lines, fmt.Sprintf("- %s：%s。", definition.Name, definition.Description))
		}
	}
	return strings.Join(lines, "\n")
}

func orderedParameterNames(properties map[string]any) []string {
	params := make([]string, 0, len(properties))
	for name := range properties {
		params = append(params, name)
	}
	sort.Strings(params)
	return params
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func normalizeEnterArguments(args map[string]any) string {
	kind := stringValue(args["kind"])
	id := stringValue(args["id"])
	if id == "" {
		id = stringValue(args["stateId"])
	}
	if id == "" {
		id = stringValue(args["app"])
	}
	if id == "" {
		id = stringValue(args["appId"])
	}
	if id == "" {
		id = stringValue(args["target"])
	}
	if id == "" {
		id = stringValue(args["query"])
	}
	if id == "" {
		id = stringValue(args["message"])
	}
	switch kind {
	case "qq_group":
		if id == "" {
			return ""
		}
		if strings.HasPrefix(id, "qq_group:") {
			return id
		}
		return "qq_group:" + id
	case "qq_private":
		if id == "" {
			return ""
		}
		if strings.HasPrefix(id, "qq_private:") {
			return id
		}
		return "qq_private:" + id
	case "ithome":
		return kind
	default:
		return id
	}
}

func commonString(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}
