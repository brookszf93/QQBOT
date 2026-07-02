package root

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type ChatTarget struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type ChildState struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
}

type GroupState struct {
	GroupID        string
	Unread         int
	UnreadMessages []GroupUnreadMessage
	HasEntered     bool
}

type PrivateState struct {
	UserID         string
	Nickname       string
	Unread         int
	UnreadMessages []PrivateUnreadMessage
	HasEntered     bool
	FocusUseRecent bool
}

type PrivateUnreadMessage struct {
	UserID     string `json:"userId"`
	Nickname   string `json:"nickname"`
	RawMessage string `json:"rawMessage"`
	MessageSeq int    `json:"messageSeq"`
	MessageID  int    `json:"messageId"`
	EventTime  time.Time
}

type GroupUnreadMessage struct {
	GroupID    string `json:"groupId"`
	UserID     string `json:"userId"`
	Nickname   string `json:"nickname"`
	RawMessage string `json:"rawMessage"`
	MessageSeq int    `json:"messageSeq"`
	MessageID  int    `json:"messageId"`
	EventTime  time.Time
}

type Session struct {
	mu            sync.Mutex
	listenGroup   []string
	StateID       string
	Stack         []string
	Groups        map[string]*GroupState
	Privates      map[string]*PrivateState
	IthomeUnread  int
	IthomeEntered bool
	Target        *ChatTarget
	TerminalCWD   string
	CurrentApp    string
}

func NewSession(listenGroupIDs []string) *Session {
	groups := map[string]*GroupState{}
	for _, groupID := range listenGroupIDs {
		groupID = strings.TrimSpace(groupID)
		if groupID == "" {
			continue
		}
		groups[groupID] = &GroupState{GroupID: groupID}
	}
	return &Session{listenGroup: append([]string(nil), listenGroupIDs...), StateID: "main", Stack: []string{"main"}, Groups: groups, Privates: map[string]*PrivateState{}}
}

func (s *Session) SetTarget(target ChatTarget) {
	s.mu.Lock()
	s.Target = &target
	s.mu.Unlock()
}

func (s *Session) Enter(stateID string) map[string]any {
	stateID = normalizeStateID(stateID)
	if isAppID(stateID) {
		return s.EnterApp(stateID)
	}
	return map[string]any{"ok": false, "error": "ENTER_TARGET_NOT_AVAILABLE", "id": stateID, "message": "聊天、私聊和新闻已并行接入，不需要 enter。"}
}

func (s *Session) Back() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Stack) <= 1 {
		return map[string]any{"ok": false, "error": "STATE_TRANSITION_NOT_ALLOWED"}
	}
	exited := s.Stack[len(s.Stack)-1]
	s.Stack = s.Stack[:len(s.Stack)-1]
	s.StateID = s.Stack[len(s.Stack)-1]
	return map[string]any{"ok": true, "id": exited, "displayName": s.displayNameLocked(exited), "message": "已退出" + s.displayNameLocked(exited)}
}

func (s *Session) EnterApp(appID string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	appID = strings.TrimSpace(appID)
	if !isAppID(appID) {
		return map[string]any{"ok": false, "error": "APP_NOT_FOUND", "id": appID}
	}
	if s.CurrentApp != "" {
		if s.CurrentApp == appID {
			return map[string]any{"ok": true, "type": "app", "enteredApp": appID, "alreadyInApp": true, "message": "已经在 " + appID + " App 里；下一步请调用 personal_screen 或 " + appIDToolName(appID) + "。"}
		}
		return map[string]any{"ok": false, "error": "ALREADY_IN_APP", "message": fmt.Sprintf("你已经在 App %q 里。先 back_to_portal 退出，再进入 %q。", s.CurrentApp, appID)}
	}
	s.CurrentApp = appID
	return map[string]any{"ok": true, "type": "app", "enteredApp": appID, "message": "已进入 " + appID + " App。调用 help 查看可用工具。"}
}

func appIDToolName(appID string) string {
	switch appID {
	case "todo":
		return "todo_app"
	case "novel":
		return "novel_app"
	case "projects":
		return "project_app"
	case "music":
		return "music_app"
	case "news":
		return "news_app"
	case "browser":
		return "browser"
	default:
		return "help"
	}
}

func (s *Session) BackToPortal() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.CurrentApp == "" {
		return map[string]any{"ok": false, "error": "NOT_IN_APP", "message": "你当前不在任何 App 里。这个工具是从 App 退回桌面用的。"}
	}
	exited := s.CurrentApp
	s.CurrentApp = ""
	return map[string]any{"ok": true, "exitedApp": exited, "message": "已退出 " + exited + " App，回到桌面。"}
}

func (s *Session) Portal() {
	s.mu.Lock()
	s.StateID = "main"
	s.Stack = []string{"main"}
	s.CurrentApp = ""
	s.mu.Unlock()
}

func (s *Session) OnGroupMessage(groupID, userID, nickname, rawMessage string, messageSeq, messageID int, eventTime time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Groups[groupID] == nil {
		return false
	}
	s.Target = &ChatTarget{Type: "group", ID: groupID}
	return true
}

func (s *Session) OnPrivateMessage(userID, nickname, rawMessage string, messageSeq, messageID int, eventTime time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePrivateLocked(userID, nickname)
	s.Target = &ChatTarget{Type: "private", ID: userID}
	return true
}

func (s *Session) OnNewsArticle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.IthomeUnread++
	return true
}

func (s *Session) CurrentChatTarget() *ChatTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Target == nil {
		return nil
	}
	target := *s.Target
	return &target
}

func (s *Session) SetTerminalCWD(cwd string) {
	s.mu.Lock()
	s.TerminalCWD = cwd
	s.mu.Unlock()
}

func (s *Session) SetIthomeOverview(unread int, hasEntered bool) {
	s.mu.Lock()
	if unread < 0 {
		unread = 0
	}
	s.IthomeUnread = unread
	s.IthomeEntered = hasEntered
	s.mu.Unlock()
}

func (s *Session) State() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.StateID
}

func (s *Session) App() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.CurrentApp
}

func (s *Session) AvailableTools() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.availableToolsLocked(s.StateID)
}

func (s *Session) IsToolAvailable(tool string) bool {
	for _, item := range s.AvailableTools() {
		if item == tool {
			return true
		}
	}
	return false
}

func (s *Session) Snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	stack := []map[string]string{}
	for _, item := range s.Stack {
		stack = append(stack, map[string]string{"id": item, "displayName": s.displayNameLocked(item)})
	}
	children := []ChildState{}
	for _, child := range s.childrenLocked(s.StateID) {
		children = append(children, child)
	}
	return map[string]any{
		"focusedStateId":          s.StateID,
		"focusedStateDisplayName": s.displayNameLocked(s.StateID),
		"focusedStateDescription": s.descriptionLocked(s.StateID),
		"currentApp":              s.CurrentApp,
		"stateStack":              stack,
		"children":                children,
		"availableInvokeTools":    s.availableToolsLockedForSnapshot(),
	}
}

func (s *Session) Export() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := []map[string]any{}
	for _, group := range s.Groups {
		unreadMessages := []map[string]any{}
		for _, message := range group.UnreadMessages {
			unreadMessages = append(unreadMessages, map[string]any{
				"groupId":    message.GroupID,
				"userId":     message.UserID,
				"nickname":   message.Nickname,
				"rawMessage": message.RawMessage,
				"messageSeq": message.MessageSeq,
				"messageId":  message.MessageID,
				"eventTime":  eventTimeString(message.EventTime),
			})
		}
		groups = append(groups, map[string]any{"groupId": group.GroupID, "unread": group.Unread, "unreadMessages": unreadMessages, "hasEntered": group.HasEntered})
	}
	privates := []map[string]any{}
	for _, private := range s.Privates {
		unreadMessages := []map[string]any{}
		for _, message := range private.UnreadMessages {
			unreadMessages = append(unreadMessages, map[string]any{
				"userId":     message.UserID,
				"nickname":   message.Nickname,
				"rawMessage": message.RawMessage,
				"messageSeq": message.MessageSeq,
				"messageId":  message.MessageID,
				"eventTime":  eventTimeString(message.EventTime),
			})
		}
		privates = append(privates, map[string]any{"userId": private.UserID, "nickname": private.Nickname, "unread": private.Unread, "unreadMessages": unreadMessages, "hasEntered": private.HasEntered})
	}
	return map[string]any{
		"focusedStateId": s.StateID,
		"stateStack":     append([]string(nil), s.Stack...),
		"groups":         groups,
		"privateChats":   privates,
		"ithomeFeedState": map[string]any{
			"unreadCount": s.IthomeUnread,
			"hasEntered":  s.IthomeEntered,
		},
		"terminalState": map[string]any{"cwd": s.TerminalCWD},
	}
}

func (s *Session) Restore(snapshot map[string]any) {
	if snapshot == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StateID = "main"
	s.Stack = []string{"main"}
	if groups, ok := snapshot["groups"].([]any); ok {
		for _, item := range groups {
			m, _ := item.(map[string]any)
			groupID, _ := m["groupId"].(string)
			if groupID == "" {
				continue
			}
			group := s.Groups[groupID]
			if group == nil {
				group = &GroupState{GroupID: groupID}
				s.Groups[groupID] = group
			}
			group.Unread = intNumber(m["unread"])
			group.HasEntered = boolValue(m["hasEntered"])
			group.UnreadMessages = groupUnreadMessagesFromAny(m["unreadMessages"])
			if len(group.UnreadMessages) > group.Unread {
				group.Unread = len(group.UnreadMessages)
			}
		}
	}
	if privates, ok := snapshot["privateChats"].([]any); ok {
		for _, item := range privates {
			m, _ := item.(map[string]any)
			userID, _ := m["userId"].(string)
			if userID == "" {
				continue
			}
			private := s.ensurePrivateLocked(userID, stringAny(m["nickname"]))
			private.Unread = intNumber(m["unread"])
			private.HasEntered = boolValue(m["hasEntered"])
			private.UnreadMessages = privateUnreadMessagesFromAny(m["unreadMessages"])
			if len(private.UnreadMessages) > private.Unread {
				private.Unread = len(private.UnreadMessages)
			}
		}
	}
	if ithome, ok := snapshot["ithomeFeedState"].(map[string]any); ok {
		s.IthomeUnread = intNumber(ithome["unreadCount"])
		s.IthomeEntered = boolValue(ithome["hasEntered"])
	}
	if terminal, ok := snapshot["terminalState"].(map[string]any); ok {
		s.TerminalCWD = stringAny(terminal["cwd"])
	}
	s.CurrentApp = ""
}

func (s *Session) canEnterLocked(stateID string) bool {
	for _, child := range s.childrenLocked(s.StateID) {
		if child.ID == stateID {
			return true
		}
	}
	return false
}

func (s *Session) canEnterFromPortalLocked(stateID string) bool {
	for _, child := range s.childrenLocked("portal") {
		if child.ID == stateID {
			return true
		}
	}
	return false
}

func (s *Session) markEnteredLocked(stateID string) {
	if groupID := strings.TrimPrefix(stateID, "qq_group:"); groupID != stateID {
		if group := s.Groups[groupID]; group != nil {
			group.HasEntered = true
			group.Unread = 0
		}
		return
	}
	if userID := strings.TrimPrefix(stateID, "qq_private:"); userID != stateID {
		if private := s.Privates[userID]; private != nil {
			private.FocusUseRecent = !private.HasEntered
			private.HasEntered = true
		}
		return
	}
	if stateID == "ithome" {
		s.IthomeEntered = true
		s.IthomeUnread = 0
	}
}

func (s *Session) childrenLocked(stateID string) []ChildState {
	if stateID != "portal" {
		return nil
	}
	children := []ChildState{}
	for _, groupID := range s.listenGroup {
		if strings.TrimSpace(groupID) == "" {
			continue
		}
		group := s.Groups[groupID]
		desc := "未读 0 条消息。"
		if group != nil && group.Unread > 0 {
			desc = fmt.Sprintf("未读 %d 条消息。", group.Unread)
		} else if group != nil && !group.HasEntered {
			desc = "尚未查看，可进去看看最近消息。"
		}
		children = append(children, ChildState{ID: "qq_group:" + groupID, DisplayName: "QQ 群 " + groupID, Description: desc})
	}
	privateIDs := make([]string, 0, len(s.Privates))
	for userID := range s.Privates {
		privateIDs = append(privateIDs, userID)
	}
	for _, userID := range privateIDs {
		private := s.Privates[userID]
		desc := "未读 0 条消息。"
		if private.Unread > 0 {
			desc = fmt.Sprintf("未读 %d 条消息。", private.Unread)
		} else if !private.HasEntered {
			desc = "尚未查看，可进去看看最近消息。"
		}
		children = append(children, ChildState{ID: "qq_private:" + userID, DisplayName: s.displayNameLocked("qq_private:" + userID), Description: desc})
	}
	ithomeDesc := "暂无新文章，可进去看看最近文章。"
	if s.IthomeUnread > 0 {
		ithomeDesc = fmt.Sprintf("新文章 %d 篇。", s.IthomeUnread)
	} else if !s.IthomeEntered {
		ithomeDesc = "尚未查看，可进去看看最近文章。"
	}
	children = append(children,
		ChildState{ID: "ithome", DisplayName: "IT 之家", Description: ithomeDesc},
	)
	return children
}

func (s *Session) displayNameLocked(stateID string) string {
	if groupID := strings.TrimPrefix(stateID, "qq_group:"); groupID != stateID {
		return "QQ 群 " + groupID
	}
	if userID := strings.TrimPrefix(stateID, "qq_private:"); userID != stateID {
		if private := s.Privates[userID]; private != nil && private.Nickname != "" {
			return "QQ 私聊 " + private.Nickname + " (" + userID + ")"
		}
		return "QQ 私聊 " + userID
	}
	switch stateID {
	case "main":
		return "并行事件流"
	case "portal":
		return "门户"
	case "ithome":
		return "IT 之家"
	default:
		return stateID
	}
}

func (s *Session) descriptionLocked(stateID string) string {
	if groupID := strings.TrimPrefix(stateID, "qq_group:"); groupID != stateID {
		if group := s.Groups[groupID]; group != nil && group.Unread > 0 {
			return fmt.Sprintf("未读 %d 条消息。", group.Unread)
		}
		return "未读 0 条消息。"
	}
	if userID := strings.TrimPrefix(stateID, "qq_private:"); userID != stateID {
		if private := s.Privates[userID]; private != nil && private.Unread > 0 {
			return fmt.Sprintf("未读 %d 条消息。", private.Unread)
		}
		return "未读 0 条消息。"
	}
	switch stateID {
	case "main":
		return "群聊、私聊和新闻并行接入。"
	case "portal":
		return "主入口，可从这里进入群聊、私聊和资讯，也可进入 calc/terminal App。"
	case "ithome":
		if s.IthomeUnread > 0 {
			return fmt.Sprintf("新文章 %d 篇。", s.IthomeUnread)
		}
		return "暂无新文章，可进去看看最近文章。"
	default:
		return ""
	}
}

func (s *Session) availableToolsLocked(stateID string) []string {
	switch s.CurrentApp {
	case "calc":
		return []string{"calculate"}
	case "terminal":
		return []string{"bash", "read_bash_output"}
	case "todo":
		return []string{"personal_screen", "workspace_app", "activity_app", "todo_app"}
	case "novel":
		return []string{"personal_screen", "workspace_app", "activity_app", "novel_app", "project_app", "todo_app"}
	case "projects":
		return []string{"personal_screen", "workspace_app", "activity_app", "project_app", "todo_app"}
	case "browser":
		return []string{"personal_screen", "workspace_app", "activity_app", "browser", "project_app"}
	case "music":
		return []string{"personal_screen", "workspace_app", "activity_app", "music_app"}
	case "news":
		return []string{"personal_screen", "workspace_app", "activity_app", "news_app", "open_ithome_article", "project_app"}
	}
	return []string{"wait", "send_message", "analyze_image", "detect_ai_tone", "browser", "search_web", "search_memory", "searchMagnetFromWeb", "open_ithome_article", "personal_screen", "workspace_app", "activity_app", "todo_app", "novel_app", "project_app", "music_app", "news_app"}
}

func (s *Session) availableToolsLockedForSnapshot() []string {
	return s.availableToolsLocked(s.StateID)
}

func (s *Session) ensurePrivateLocked(userID, nickname string) *PrivateState {
	private := s.Privates[userID]
	if private == nil {
		private = &PrivateState{UserID: userID}
		s.Privates[userID] = private
	}
	if nickname != "" {
		private.Nickname = nickname
	}
	return private
}

func (s *Session) ConsumePrivateFocusMessages(userID string) ([]PrivateUnreadMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	private := s.Privates[userID]
	if private == nil {
		return nil, true
	}
	useRecent := private.FocusUseRecent
	private.FocusUseRecent = false
	if useRecent {
		private.UnreadMessages = nil
		private.Unread = 0
		return nil, true
	}
	messages := append([]PrivateUnreadMessage(nil), private.UnreadMessages...)
	private.UnreadMessages = nil
	private.Unread = 0
	return messages, false
}

func (s *Session) ConsumeGroupFocusMessages(groupID string) []GroupUnreadMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	group := s.Groups[groupID]
	if group == nil {
		return nil
	}
	messages := append([]GroupUnreadMessage(nil), group.UnreadMessages...)
	group.UnreadMessages = nil
	group.Unread = 0
	return messages
}

func groupUnreadMessagesFromAny(value any) []GroupUnreadMessage {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]GroupUnreadMessage, 0, len(items))
	for _, item := range items {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		out = append(out, GroupUnreadMessage{
			GroupID:    stringAny(m["groupId"]),
			UserID:     stringAny(m["userId"]),
			Nickname:   stringAny(m["nickname"]),
			RawMessage: stringAny(m["rawMessage"]),
			MessageSeq: intNumber(m["messageSeq"]),
			MessageID:  intNumber(m["messageId"]),
			EventTime:  timeAny(m["eventTime"]),
		})
	}
	return takeLastGroupUnread(out, 20)
}

func privateUnreadMessagesFromAny(value any) []PrivateUnreadMessage {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]PrivateUnreadMessage, 0, len(items))
	for _, item := range items {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		out = append(out, PrivateUnreadMessage{
			UserID:     stringAny(m["userId"]),
			Nickname:   stringAny(m["nickname"]),
			RawMessage: stringAny(m["rawMessage"]),
			MessageSeq: intNumber(m["messageSeq"]),
			MessageID:  intNumber(m["messageId"]),
			EventTime:  timeAny(m["eventTime"]),
		})
	}
	return takeLastPrivateUnread(out, 20)
}

func takeLastPrivateUnread(items []PrivateUnreadMessage, limit int) []PrivateUnreadMessage {
	if limit <= 0 {
		return nil
	}
	if len(items) <= limit {
		return append([]PrivateUnreadMessage(nil), items...)
	}
	return append([]PrivateUnreadMessage(nil), items[len(items)-limit:]...)
}

func takeLastGroupUnread(items []GroupUnreadMessage, limit int) []GroupUnreadMessage {
	if limit <= 0 {
		return nil
	}
	if len(items) <= limit {
		return append([]GroupUnreadMessage(nil), items...)
	}
	return append([]GroupUnreadMessage(nil), items[len(items)-limit:]...)
}

func normalizeStateID(stateID string) string {
	stateID = strings.TrimSpace(stateID)
	if strings.HasPrefix(stateID, "qq_group:") || strings.HasPrefix(stateID, "qq_private:") {
		return stateID
	}
	switch stateID {
	case "portal", "ithome":
		return stateID
	default:
		return stateID
	}
}

func isAppID(appID string) bool {
	switch strings.TrimSpace(appID) {
	case "calc", "terminal", "todo", "novel", "projects", "browser", "music", "news":
		return true
	default:
		return false
	}
}

func intNumber(value any) int {
	switch x := value.(type) {
	case int:
		return x
	case float64:
		return int(x)
	default:
		return 0
	}
}

func boolValue(value any) bool {
	v, _ := value.(bool)
	return v
}

func stringAny(value any) string {
	v, _ := value.(string)
	return v
}

func eventTimeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339Nano)
}

func timeAny(value any) time.Time {
	switch v := value.(type) {
	case time.Time:
		return v
	case string:
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	}
	return time.Time{}
}
