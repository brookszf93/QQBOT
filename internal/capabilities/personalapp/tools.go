package personalapp

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"QqBot/internal/agentruntime"
)

type ScreenTool struct{ Service *Service }
type TodoTool struct{ Service *Service }
type NovelTool struct{ Service *Service }
type ProjectTool struct{ Service *Service }
type MusicTool struct{ Service *Service }
type NewsTool struct{ Service *Service }
type ActivityTool struct{ Service *Service }
type WorkspaceTool struct{ Service *Service }

func (ScreenTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "personal_screen", Description: "读取当前个人 App 画面：todo、novel、projects、browser、music、news 或 activity。", Parameters: agentruntime.ObjectSchema(map[string]any{
		"app": map[string]any{"type": "string", "enum": []string{"workspace", "todo", "novel", "projects", "browser", "music", "news", "activity"}},
	})}
}
func (ScreenTool) Kind() string { return "business" }
func (t ScreenTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	app := stringArg(call.Arguments, "app")
	if app == "" {
		app = "projects"
	}
	screen, err := t.Service.Screen(app)
	if err != nil {
		return result(map[string]any{"ok": false, "error": "SCREEN_FAILED", "message": err.Error()}), nil
	}
	return result(map[string]any{"ok": true, "screen": screen}), nil
}

func (TodoTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "todo_app", Description: "个人待办 App。用于添加、列出、更新、完成或移除自己的任务。", Parameters: agentruntime.ObjectSchema(map[string]any{
		"action":    map[string]any{"type": "string", "enum": []string{"add", "list", "update", "complete", "remove"}},
		"id":        map[string]any{"type": "string"},
		"text":      map[string]any{"type": "string"},
		"projectId": map[string]any{"type": "string"},
		"status":    map[string]any{"type": "string", "enum": []string{"open", "done", "paused", "dropped"}},
		"dueAt":     map[string]any{"type": "string"},
	})}
}
func (TodoTool) Kind() string { return "business" }
func (t TodoTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	switch action(call.Arguments) {
	case "add":
		item, err := t.Service.AddTodo(stringArg(call.Arguments, "text"), stringArg(call.Arguments, "projectId"), stringArg(call.Arguments, "dueAt"))
		return jsonErrOr("todo", item, err), nil
	case "update":
		item, err := t.Service.UpdateTodo(stringArg(call.Arguments, "id"), stringArg(call.Arguments, "text"), stringArg(call.Arguments, "status"), stringArg(call.Arguments, "dueAt"))
		return jsonErrOr("todo", item, err), nil
	case "complete":
		item, err := t.Service.UpdateTodo(stringArg(call.Arguments, "id"), "", "done", "")
		return jsonErrOr("todo", item, err), nil
	case "remove":
		item, err := t.Service.UpdateTodo(stringArg(call.Arguments, "id"), "", "dropped", "")
		return jsonErrOr("todo", item, err), nil
	default:
		items, err := t.Service.ListTodos(stringArg(call.Arguments, "status"), stringArg(call.Arguments, "projectId"))
		return jsonErrOr("todos", items, err), nil
	}
}

func (NovelTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "novel_app", Description: "小说和随笔写作 App。用于管理写作项目、大纲、笔记、草稿和项目待办。", Parameters: agentruntime.ObjectSchema(map[string]any{
		"action":    map[string]any{"type": "string", "enum": []string{"screen", "upsert_entry", "write_entry", "create_project", "list_projects", "open_project", "append_draft", "append_note", "update_outline", "add_todo", "complete_todo"}},
		"projectId": map[string]any{"type": "string"},
		"title":     map[string]any{"type": "string"},
		"text":      map[string]any{"type": "string"},
		"file":      map[string]any{"type": "string", "enum": []string{"draft.md", "notes.md", "outline.md", "journal.md"}},
		"todoId":    map[string]any{"type": "string"},
		"dueAt":     map[string]any{"type": "string"},
	})}
}
func (NovelTool) Kind() string { return "business" }
func (t NovelTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	projectID := stringArg(call.Arguments, "projectId")
	switch action(call.Arguments) {
	case "upsert_entry":
		project, created, err := t.Service.UpsertNovelEntry(projectID, stringArg(call.Arguments, "title"), stringArg(call.Arguments, "text"), stringArg(call.Arguments, "file"))
		if err != nil {
			return jsonErrOr("project", project, err), nil
		}
		screen, _ := t.Service.Screen("novel")
		return result(map[string]any{"ok": true, "project": project, "created": created, "screen": screen}), nil
	case "write_entry":
		title := stringArg(call.Arguments, "title")
		text := stringArg(call.Arguments, "text")
		if strings.TrimSpace(text) == "" {
			return result(map[string]any{"ok": false, "error": "TEXT_REQUIRED", "message": "write_entry 需要填写 text。"}), nil
		}
		project, err := t.Service.CreateProject("novel", title)
		if err != nil {
			return jsonErrOr("project", project, err), nil
		}
		if err := t.Service.AppendProjectText(project.ID, "draft.md", text); err != nil {
			return jsonErrOr("project", project, err), nil
		}
		screen, _ := t.Service.Screen("novel")
		return result(map[string]any{"ok": true, "project": project, "screen": screen}), nil
	case "create_project":
		project, err := t.Service.CreateProject("novel", stringArg(call.Arguments, "title"))
		return jsonErrOr("project", project, err), nil
	case "list_projects":
		projects, err := t.Service.ListProjects("novel")
		return jsonErrOr("projects", projects, err), nil
	case "open_project":
		project, fallback, err := t.Service.OpenNovelProjectWithFallback(projectID)
		if err != nil {
			return jsonErrOr("project", project, err), nil
		}
		payload := map[string]any{"ok": true, "project": project}
		if fallback {
			payload["fallback"] = true
			payload["requestedProjectId"] = projectID
			payload["message"] = "请求的项目不存在，已改为打开当前活跃的小说项目。"
		}
		return result(payload), nil
	case "append_draft":
		err := t.Service.AppendProjectText(t.Service.ResolveNovelProjectID(projectID), "draft.md", stringArg(call.Arguments, "text"))
		return okErr(err), nil
	case "append_note":
		err := t.Service.AppendProjectText(t.Service.ResolveNovelProjectID(projectID), "notes.md", stringArg(call.Arguments, "text"))
		return okErr(err), nil
	case "update_outline":
		err := t.Service.ReplaceProjectText(t.Service.ResolveNovelProjectID(projectID), "outline.md", stringArg(call.Arguments, "text"))
		return okErr(err), nil
	case "add_todo":
		item, err := t.Service.AddTodo(stringArg(call.Arguments, "text"), projectID, stringArg(call.Arguments, "dueAt"))
		return jsonErrOr("todo", item, err), nil
	case "complete_todo":
		item, err := t.Service.UpdateTodo(stringArg(call.Arguments, "todoId"), "", "done", "")
		return jsonErrOr("todo", item, err), nil
	default:
		screen, err := t.Service.Screen("novel")
		return jsonErrOr("screen", screen, err), nil
	}
}

func (ProjectTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "project_app", Description: "通用个人项目工作区。用于创建项目，并追加受控 Markdown 笔记或日志。", Parameters: agentruntime.ObjectSchema(map[string]any{
		"action":    map[string]any{"type": "string", "enum": []string{"screen", "create", "list", "append_note", "append_journal"}},
		"kind":      map[string]any{"type": "string"},
		"title":     map[string]any{"type": "string"},
		"projectId": map[string]any{"type": "string"},
		"text":      map[string]any{"type": "string"},
	})}
}
func (ProjectTool) Kind() string { return "business" }
func (t ProjectTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	switch action(call.Arguments) {
	case "create":
		project, err := t.Service.CreateProject(stringArg(call.Arguments, "kind"), stringArg(call.Arguments, "title"))
		return jsonErrOr("project", project, err), nil
	case "list":
		projects, err := t.Service.ListProjects(stringArg(call.Arguments, "kind"))
		return jsonErrOr("projects", projects, err), nil
	case "append_note":
		err := t.Service.AppendProjectText(stringArg(call.Arguments, "projectId"), "notes.md", stringArg(call.Arguments, "text"))
		return okErr(err), nil
	case "append_journal":
		err := t.Service.AppendProjectText(stringArg(call.Arguments, "projectId"), "journal.md", stringArg(call.Arguments, "text"))
		return okErr(err), nil
	default:
		screen, err := t.Service.Screen("projects")
		return jsonErrOr("screen", screen, err), nil
	}
}

func (MusicTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "music_app", Description: "个人音乐 App。用于维护歌单、当前在听歌曲，并保存听后感或 QQ 音乐卡片笔记。", Parameters: agentruntime.ObjectSchema(map[string]any{
		"action":     map[string]any{"type": "string", "enum": []string{"screen", "add", "list", "set_current", "save_impression", "finish", "drop"}},
		"id":         map[string]any{"type": "string"},
		"title":      map[string]any{"type": "string"},
		"artist":     map[string]any{"type": "string"},
		"url":        map[string]any{"type": "string"},
		"note":       map[string]any{"type": "string"},
		"impression": map[string]any{"type": "string"},
	})}
}
func (MusicTool) Kind() string { return "business" }
func (t MusicTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	switch action(call.Arguments) {
	case "add":
		item, err := t.Service.AddMusic(stringArg(call.Arguments, "title"), stringArg(call.Arguments, "artist"), stringArg(call.Arguments, "url"), stringArg(call.Arguments, "note"))
		return jsonErrOr("music", item, err), nil
	case "set_current":
		item, err := t.Service.UpdateMusic(stringArg(call.Arguments, "id"), "current", "")
		return jsonErrOr("music", item, err), nil
	case "save_impression":
		item, err := t.Service.UpdateMusic(stringArg(call.Arguments, "id"), "", stringArg(call.Arguments, "impression"))
		return jsonErrOr("music", item, err), nil
	case "finish":
		item, err := t.Service.UpdateMusic(stringArg(call.Arguments, "id"), "done", stringArg(call.Arguments, "impression"))
		return jsonErrOr("music", item, err), nil
	case "drop":
		item, err := t.Service.UpdateMusic(stringArg(call.Arguments, "id"), "dropped", "")
		return jsonErrOr("music", item, err), nil
	case "list":
		items, err := t.Service.ListMusic()
		return jsonErrOr("music", items, err), nil
	default:
		screen, err := t.Service.Screen("music")
		return jsonErrOr("screen", screen, err), nil
	}
}

func (NewsTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "news_app", Description: "个人新闻 App。用于保存和列出文章 takeaway，作为自己的阅读笔记。", Parameters: agentruntime.ObjectSchema(map[string]any{
		"action":    map[string]any{"type": "string", "enum": []string{"screen", "save_takeaway", "list"}},
		"source":    map[string]any{"type": "string"},
		"articleId": map[string]any{"type": "integer"},
		"title":     map[string]any{"type": "string"},
		"url":       map[string]any{"type": "string"},
		"takeaway":  map[string]any{"type": "string"},
		"tags":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	})}
}
func (NewsTool) Kind() string { return "business" }
func (t NewsTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	switch action(call.Arguments) {
	case "save_takeaway":
		item, err := t.Service.AddNewsNote(stringArg(call.Arguments, "source"), intArg(call.Arguments, "articleId"), stringArg(call.Arguments, "title"), stringArg(call.Arguments, "url"), stringArg(call.Arguments, "takeaway"), stringSliceArg(call.Arguments, "tags"))
		return jsonErrOr("news", item, err), nil
	case "list":
		items, err := t.Service.ListNewsNotes()
		return jsonErrOr("news", items, err), nil
	default:
		screen, err := t.Service.Screen("news")
		return jsonErrOr("screen", screen, err), nil
	}
}

func (ActivityTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "activity_app", Description: "个人活动 App。记录自己正在做什么：写作、浏览网页、听音乐、看新闻、处理待办、做项目或整理想法。", Parameters: agentruntime.ObjectSchema(map[string]any{
		"action":    map[string]any{"type": "string", "enum": []string{"screen", "start", "finish", "list"}},
		"kind":      map[string]any{"type": "string", "enum": []string{"writing", "browser", "music", "news", "todo", "project", "thought"}},
		"id":        map[string]any{"type": "string"},
		"title":     map[string]any{"type": "string"},
		"text":      map[string]any{"type": "string"},
		"url":       map[string]any{"type": "string"},
		"projectId": map[string]any{"type": "string"},
		"status":    map[string]any{"type": "string", "enum": []string{"active", "done"}},
		"tags":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	})}
}
func (ActivityTool) Kind() string { return "business" }
func (t ActivityTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	switch action(call.Arguments) {
	case "start":
		item, err := t.Service.StartActivity(stringArg(call.Arguments, "kind"), stringArg(call.Arguments, "title"), stringArg(call.Arguments, "text"), stringArg(call.Arguments, "url"), stringSliceArg(call.Arguments, "tags"))
		return jsonErrOr("activity", item, err), nil
	case "finish":
		item, err := t.Service.FinishActivity(stringArg(call.Arguments, "id"), stringArg(call.Arguments, "text"), stringArg(call.Arguments, "projectId"))
		return jsonErrOr("activity", item, err), nil
	case "list":
		items, err := t.Service.ListActivities(stringArg(call.Arguments, "status"), stringArg(call.Arguments, "kind"))
		return jsonErrOr("activities", items, err), nil
	default:
		screen, err := t.Service.Screen("activity")
		return jsonErrOr("screen", screen, err), nil
	}
}

func (WorkspaceTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "workspace_app", Description: "个人文件工作台。用于查看 journal/drafts/reading/music/scratchpad 总览，或写入一条随笔、草稿、阅读摘记、听歌记录。", Parameters: agentruntime.ObjectSchema(map[string]any{
		"action": map[string]any{"type": "string", "enum": []string{"overview", "write"}},
		"kind":   map[string]any{"type": "string", "enum": []string{"journal", "drafts", "reading", "music"}},
		"title":  map[string]any{"type": "string"},
		"text":   map[string]any{"type": "string"},
		"tags":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	})}
}
func (WorkspaceTool) Kind() string { return "business" }
func (t WorkspaceTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	switch action(call.Arguments) {
	case "write":
		item, err := t.Service.WriteWorkspaceEntry(stringArg(call.Arguments, "kind"), stringArg(call.Arguments, "title"), stringArg(call.Arguments, "text"), stringSliceArg(call.Arguments, "tags"))
		return jsonErrOr("file", item, err), nil
	default:
		overview, err := t.Service.WorkspaceOverview()
		return jsonErrOr("workspace", overview, err), nil
	}
}

func action(args map[string]any) string {
	value := strings.TrimSpace(stringArg(args, "action"))
	if value == "" {
		return "screen"
	}
	return value
}

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	switch v := args[key].(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

func intArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	default:
		return 0
	}
}

func stringSliceArg(args map[string]any, key string) []string {
	raw, _ := args[key].([]any)
	out := []string{}
	for _, item := range raw {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	if len(out) > 0 {
		return out
	}
	if s := stringArg(args, key); s != "" {
		for _, item := range strings.Split(s, ",") {
			if strings.TrimSpace(item) != "" {
				out = append(out, strings.TrimSpace(item))
			}
		}
	}
	return out
}

func jsonErrOr(key string, value any, err error) agentruntime.ToolResult {
	if err != nil {
		return result(map[string]any{"ok": false, "error": "PERSONAL_APP_FAILED", "message": err.Error()})
	}
	return result(map[string]any{"ok": true, key: value})
}

func okErr(err error) agentruntime.ToolResult {
	if err != nil {
		return result(map[string]any{"ok": false, "error": "PERSONAL_APP_FAILED", "message": err.Error()})
	}
	return result(map[string]any{"ok": true})
}

func result(payload map[string]any) agentruntime.ToolResult {
	data, err := json.Marshal(payload)
	if err != nil {
		return agentruntime.ToolResult{Kind: "business", Content: `{"ok":false,"error":"JSON_ENCODE_FAILED"}`}
	}
	return agentruntime.ToolResult{Kind: "business", Content: string(data)}
}
