package personalapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"QqBot/internal/agentruntime"
)

func TestServicePersistsNovelTodoMusicAndNews(t *testing.T) {
	service := NewService(t.TempDir())

	project, err := service.CreateProject("novel", "Base Novel")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenNovelProject(project.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.ReplaceProjectText(project.ID, "outline.md", "# Outline\n- Start here"); err != nil {
		t.Fatal(err)
	}
	if err := service.AppendProjectText(project.ID, "draft.md", "第一章草稿"); err != nil {
		t.Fatal(err)
	}
	todo, err := service.AddTodo("补沈印视角", project.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	music, err := service.AddMusic("晴天", "周杰伦", "https://music.163.com/song/186016", "QQ card")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateMusic(music.ID, "current", "雨声感很重"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddNewsNote("ithome", 44, "Gemini queue", "https://example.com", "卡在 99 一直刷 Gemini", []string{"ai"}); err != nil {
		t.Fatal(err)
	}

	screen, err := service.Screen("novel")
	if err != nil {
		t.Fatal(err)
	}
	if screen.State.ActiveNovelProjectID != project.ID || !strings.Contains(screen.Markdown, "第一章草稿") {
		t.Fatalf("novel screen missing active draft: %#v", screen)
	}
	if len(screen.Todos) != 1 || screen.Todos[0].ID != todo.ID {
		t.Fatalf("novel screen missing todo: %#v", screen.Todos)
	}

	reloaded := NewService(service.Root())
	musicScreen, err := reloaded.Screen("music")
	if err != nil {
		t.Fatal(err)
	}
	if len(musicScreen.Music) != 1 || musicScreen.Music[0].Impression == "" {
		t.Fatalf("music did not persist: %#v", musicScreen.Music)
	}
	newsScreen, err := reloaded.Screen("news")
	if err != nil {
		t.Fatal(err)
	}
	if len(newsScreen.News) != 1 || newsScreen.News[0].Takeaway == "" {
		t.Fatalf("news note did not persist: %#v", newsScreen.News)
	}
	if _, err := os.Stat(filepath.Join(service.Root(), "projects", project.ID, "draft.md")); err != nil {
		t.Fatalf("draft file missing: %v", err)
	}
}

func TestWorkspaceToolWritesFileAndOverview(t *testing.T) {
	service := NewService(t.TempDir())
	tool := WorkspaceTool{Service: service}

	result, err := tool.Execute(context.Background(), agentruntime.ToolCall{
		ID: "workspace-write",
		Arguments: map[string]any{
			"action": "write",
			"kind":   "journal",
			"title":  "今天的节奏",
			"text":   "先写一点，再去看群里有没有新梗。",
			"tags":   []any{"rhythm", "journal"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, `"ok":true`) || !strings.Contains(result.Content, "今天的节奏") {
		t.Fatalf("workspace write failed: %s", result.Content)
	}
	overview, err := service.WorkspaceOverview()
	if err != nil {
		t.Fatal(err)
	}
	if overview.Sections["journal"] != 1 || len(overview.Recent) != 1 {
		t.Fatalf("workspace overview missing journal entry: %#v", overview)
	}
	if _, err := os.Stat(filepath.Join(service.Root(), overview.Recent[0].Path)); err != nil {
		t.Fatalf("workspace markdown missing: %v", err)
	}
}

func TestServiceClearsDanglingActiveNovelProject(t *testing.T) {
	service := NewService(t.TempDir())
	if err := service.ensure(); err != nil {
		t.Fatal(err)
	}
	if err := service.saveProjects([]Project{}); err != nil {
		t.Fatal(err)
	}
	if err := service.saveState(stateFile{ActiveNovelProjectID: "novel-missing-123"}); err != nil {
		t.Fatal(err)
	}

	screen, err := service.Screen("novel")
	if err != nil {
		t.Fatal(err)
	}
	if screen.State.ActiveNovelProjectID != "" {
		t.Fatalf("dangling active novel project should be cleared: %#v", screen.State)
	}

	project, err := service.CreateProject("novel", "New Draft")
	if err != nil {
		t.Fatal(err)
	}
	screen, err = service.Screen("novel")
	if err != nil {
		t.Fatal(err)
	}
	if screen.State.ActiveNovelProjectID != project.ID {
		t.Fatalf("new novel should become active after dangling state is cleared: got %q want %q", screen.State.ActiveNovelProjectID, project.ID)
	}
}

func TestNovelWriteEntryCreatesProjectAndDraft(t *testing.T) {
	service := NewService(t.TempDir())
	tool := NovelTool{Service: service}
	result, err := tool.Execute(context.Background(), agentruntime.ToolCall{Arguments: map[string]any{
		"action": "write_entry",
		"title":  "Window Note",
		"text":   "The window keeps a small square of afternoon.",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, `"ok":true`) {
		t.Fatalf("write_entry failed: %s", result.Content)
	}
	projects, err := service.ListProjects("novel")
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected one project, got %#v", projects)
	}
	data, err := os.ReadFile(filepath.Join(service.Root(), "projects", projects[0].ID, "draft.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "small square of afternoon") {
		t.Fatalf("draft missing entry: %s", string(data))
	}
}

func TestActivityToolTracksLifeActivity(t *testing.T) {
	service := NewService(t.TempDir())
	tool := ActivityTool{Service: service}
	start, err := tool.Execute(context.Background(), agentruntime.ToolCall{Arguments: map[string]any{
		"action": "start",
		"kind":   "music",
		"title":  "Night Song",
		"text":   "想听点安静的",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(start.Content, `"ok":true`) {
		t.Fatalf("start activity failed: %s", start.Content)
	}
	screen, err := service.Screen("activity")
	if err != nil {
		t.Fatal(err)
	}
	if screen.State.CurrentActivityID == "" || len(screen.Activities) != 1 || screen.Activities[0].Kind != "music" {
		t.Fatalf("activity screen missing current activity: %#v", screen)
	}
	finish, err := tool.Execute(context.Background(), agentruntime.ToolCall{Arguments: map[string]any{
		"action": "finish",
		"text":   "听完觉得适合写夜路。",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(finish.Content, `"status":"done"`) {
		t.Fatalf("finish activity failed: %s", finish.Content)
	}
	screen, err = service.Screen("activity")
	if err != nil {
		t.Fatal(err)
	}
	if screen.State.CurrentActivityID != "" {
		t.Fatalf("finished activity should clear current id: %#v", screen.State)
	}
}

func TestNovelOpenProjectFallsBackToActiveWhenModelUsesStaleID(t *testing.T) {
	service := NewService(t.TempDir())
	project, err := service.CreateProject("novel", "Active Draft")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenNovelProject(project.ID); err != nil {
		t.Fatal(err)
	}

	tool := NovelTool{Service: service}
	result, err := tool.Execute(context.Background(), agentruntime.ToolCall{Arguments: map[string]any{
		"action":    "open_project",
		"projectId": "novel-missing-137300",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, `"fallback":true`) || !strings.Contains(result.Content, project.ID) {
		t.Fatalf("stale project id should fall back to active project: %s", result.Content)
	}

	result, err = tool.Execute(context.Background(), agentruntime.ToolCall{Arguments: map[string]any{
		"action":    "append_draft",
		"projectId": "novel-missing-137300",
		"text":      "continued through fallback",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, `"ok":true`) {
		t.Fatalf("append through stale id failed: %s", result.Content)
	}
	data, err := os.ReadFile(filepath.Join(service.Root(), "projects", project.ID, "draft.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "continued through fallback") {
		t.Fatalf("fallback append wrote to wrong draft: %s", string(data))
	}
}

func TestNovelUpsertEntryReadsExistingBeforeCreating(t *testing.T) {
	service := NewService(t.TempDir())
	project, err := service.CreateProject("novel", "Existing Essay")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenNovelProject(project.ID); err != nil {
		t.Fatal(err)
	}

	tool := NovelTool{Service: service}
	result, err := tool.Execute(context.Background(), agentruntime.ToolCall{Arguments: map[string]any{
		"action": "upsert_entry",
		"title":  "Another Title",
		"text":   "append to current project",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, `"created":false`) || !strings.Contains(result.Content, project.ID) {
		t.Fatalf("upsert should append to active project before creating: %s", result.Content)
	}
	projects, err := service.ListProjects("novel")
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("upsert should not create a second project when active exists: %#v", projects)
	}
	data, err := os.ReadFile(filepath.Join(service.Root(), "projects", project.ID, "draft.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "append to current project") {
		t.Fatalf("upsert did not append to active draft: %s", string(data))
	}
}
