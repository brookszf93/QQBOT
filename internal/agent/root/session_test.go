package root

import (
	"testing"
	"time"
)

func TestSessionUsesParallelMessagingTools(t *testing.T) {
	session := NewSession([]string{"1001", "1002"})
	for _, tool := range []string{"wait", "send_message", "analyze_image", "detect_ai_tone", "browser", "search_web", "searchMagnetFromWeb", "open_ithome_article", "personal_screen", "novel_app"} {
		if !session.IsToolAvailable(tool) {
			t.Fatalf("main event stream should expose %s", tool)
		}
	}
	if state := session.State(); state != "main" {
		t.Fatalf("unexpected initial state: %s", state)
	}
	if result := session.Enter("qq_group:1001"); result["ok"] != false {
		t.Fatalf("chat states should no longer require enter: %#v", result)
	}
}

func TestSessionRoutesMultipleChatsWithoutStateSwitching(t *testing.T) {
	session := NewSession([]string{"1001", "1002"})
	if !session.OnGroupMessage("1001", "2001", "alice", "first", 1, 2, time.Time{}) {
		t.Fatal("listened group messages should enter the shared event stream")
	}
	target := session.CurrentChatTarget()
	if target == nil || target.Type != "group" || target.ID != "1001" {
		t.Fatalf("unexpected first target: %#v", target)
	}

	if !session.OnPrivateMessage("3001", "bob", "second", 3, 4, time.Time{}) {
		t.Fatal("private messages should enter the shared event stream")
	}
	target = session.CurrentChatTarget()
	if target == nil || target.Type != "private" || target.ID != "3001" {
		t.Fatalf("latest chat should become the default target: %#v", target)
	}
	if state := session.State(); state != "main" {
		t.Fatalf("chat events must not switch state: %s", state)
	}
}

func TestSessionKeepsAppsIsolated(t *testing.T) {
	session := NewSession([]string{"1001"})
	if result := session.EnterApp("terminal"); result["ok"] != true {
		t.Fatalf("enter terminal app failed: %#v", result)
	}
	if !session.IsToolAvailable("bash") || session.IsToolAvailable("send_message") {
		t.Fatal("terminal app should expose only terminal tools")
	}
	if result := session.BackToPortal(); result["ok"] != true {
		t.Fatalf("back_to_portal failed: %#v", result)
	}
	if !session.IsToolAvailable("send_message") {
		t.Fatal("leaving the app should restore parallel messaging tools")
	}
}

func TestSessionPersonalAppsExposeScopedTools(t *testing.T) {
	session := NewSession([]string{"1001"})
	if result := session.EnterApp("novel"); result["ok"] != true {
		t.Fatalf("enter novel app failed: %#v", result)
	}
	if result := session.EnterApp("novel"); result["ok"] != true || result["alreadyInApp"] != true {
		t.Fatalf("re-entering same app should be idempotent: %#v", result)
	}
	for _, tool := range []string{"personal_screen", "novel_app", "project_app", "todo_app"} {
		if !session.IsToolAvailable(tool) {
			t.Fatalf("novel app should expose %s", tool)
		}
	}
	if session.IsToolAvailable("send_message") {
		t.Fatal("personal apps should not expose send_message directly")
	}
	if result := session.BackToPortal(); result["ok"] != true {
		t.Fatalf("back_to_portal failed: %#v", result)
	}
	if result := session.EnterApp("music"); result["ok"] != true {
		t.Fatalf("enter music app failed: %#v", result)
	}
	if !session.IsToolAvailable("music_app") || session.IsToolAvailable("novel_app") {
		t.Fatal("music app should expose only music workspace tools")
	}
}

func TestSessionRestoreCollapsesLegacyChatStack(t *testing.T) {
	session := NewSession([]string{"1001"})
	session.Restore(map[string]any{
		"focusedStateId": "qq_group:1001",
		"stateStack":     []string{"portal", "qq_group:1001"},
	})
	if state := session.State(); state != "main" {
		t.Fatalf("legacy state should collapse to main: %s", state)
	}
	snapshot := session.Snapshot()
	stack := snapshot["stateStack"].([]map[string]string)
	if len(stack) != 1 || stack[0]["id"] != "main" {
		t.Fatalf("unexpected restored stack: %#v", stack)
	}
}

func TestSessionNewsDoesNotReplaceLatestChatTarget(t *testing.T) {
	session := NewSession([]string{"1001"})
	session.OnGroupMessage("1001", "2001", "alice", "hello", 1, 2, time.Time{})
	if !session.OnNewsArticle() {
		t.Fatal("news should enter the shared event stream")
	}
	target := session.CurrentChatTarget()
	if target == nil || target.Type != "group" || target.ID != "1001" {
		t.Fatalf("news must not erase the latest chat reply target: %#v", target)
	}
}
