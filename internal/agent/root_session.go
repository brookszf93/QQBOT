package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"qqbot-ai/internal/agentruntime"
	"qqbot-ai/internal/config"
	"qqbot-ai/internal/db"
	"qqbot-ai/internal/prompts"
	"sort"
	"strings"
	"sync"
	"time"
)

// rootSession 复刻 TS RootAgentSession 的核心状态导航行为。
//
// 它维护 portal、群聊、私聊、IT 之家、终端和 zone_out 状态，
// 并根据当前聚焦状态决定 invoke 子工具是否可调用。
type rootSession struct {
	mu              sync.Mutex
	cfg             *config.Config
	store           *db.Store
	recentProvider  recentMessageProvider
	stack           []string
	groupUnread     map[string][]string
	privateUnread   map[string][]string
	privateKnown    map[string]bool
	notifications   map[string]notificationEntry
	lastNotifyFlush time.Time
	entered         map[string]bool
	ithomeUnread    int
	ithomeEntered   bool
	terminalEnabled bool
	lastZoneThought string
}

type recentMessageProvider interface {
	RecentGroupMessages(groupID string, count int) []string
	RecentPrivateMessages(userID string, count int) []string
}

type notificationEntry struct {
	StateID     string
	DisplayName string
	Summary     string
	At          time.Time
}

type rootSessionSnapshot struct {
	Stack           []string            `json:"stack"`
	GroupUnread     map[string][]string `json:"groupUnread"`
	PrivateUnread   map[string][]string `json:"privateUnread"`
	PrivateKnown    map[string]bool     `json:"privateKnown"`
	LastZoneThought string              `json:"lastZoneThought"`
	TerminalEnabled bool                `json:"terminalEnabled"`
	Entered         map[string]bool     `json:"entered"`
	IthomeUnread    int                 `json:"ithomeUnread"`
	IthomeEntered   bool                `json:"ithomeEntered"`
}

func newRootSession(cfg *config.Config, store *db.Store, terminalEnabled bool, recentProvider recentMessageProvider) *rootSession {
	return &rootSession{
		cfg:             cfg,
		store:           store,
		recentProvider:  recentProvider,
		stack:           []string{"portal"},
		groupUnread:     map[string][]string{},
		privateUnread:   map[string][]string{},
		privateKnown:    map[string]bool{},
		notifications:   map[string]notificationEntry{},
		lastNotifyFlush: time.Now(),
		entered:         map[string]bool{},
		terminalEnabled: terminalEnabled,
	}
}

func (s *rootSession) export() rootSessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return rootSessionSnapshot{
		Stack:           append([]string(nil), s.stack...),
		GroupUnread:     cloneStringSlices(s.groupUnread),
		PrivateUnread:   cloneStringSlices(s.privateUnread),
		PrivateKnown:    cloneBoolMap(s.privateKnown),
		LastZoneThought: s.lastZoneThought,
		TerminalEnabled: s.terminalEnabled,
		Entered:         cloneBoolMap(s.entered),
		IthomeUnread:    s.ithomeUnread,
		IthomeEntered:   s.ithomeEntered,
	}
}

func (s *rootSession) restore(snapshot rootSessionSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(snapshot.Stack) > 0 {
		s.stack = append([]string(nil), snapshot.Stack...)
	}
	if snapshot.GroupUnread != nil {
		s.groupUnread = cloneStringSlices(snapshot.GroupUnread)
	}
	if snapshot.PrivateUnread != nil {
		s.privateUnread = cloneStringSlices(snapshot.PrivateUnread)
	}
	if snapshot.PrivateKnown != nil {
		s.privateKnown = cloneBoolMap(snapshot.PrivateKnown)
	}
	if snapshot.Entered != nil {
		s.entered = cloneBoolMap(snapshot.Entered)
	}
	s.ithomeUnread = snapshot.IthomeUnread
	s.ithomeEntered = snapshot.IthomeEntered
	s.lastZoneThought = snapshot.LastZoneThought
}

func (s *rootSession) focused() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stack[len(s.stack)-1]
}

func (s *rootSession) portalReminder() string {
	return prompts.PortalSnapshot(s.portalGroups(), s.portalPrivates(), s.portalFeeds())
}

func (s *rootSession) hasUnreadActivity() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, unread := range s.groupUnread {
		if len(unread) > 0 {
			return true
		}
	}
	for _, unread := range s.privateUnread {
		if len(unread) > 0 {
			return true
		}
	}
	return s.ithomeUnread > 0
}

func (s *rootSession) hasPrivateUnread() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, unread := range s.privateUnread {
		if len(unread) > 0 {
			return true
		}
	}
	return false
}

func (s *rootSession) markFocusedUnreadSeen() {
	s.mu.Lock()
	defer s.mu.Unlock()
	stateID := s.stack[len(s.stack)-1]
	if groupID := strings.TrimPrefix(stateID, "qq_group:"); groupID != stateID {
		s.groupUnread[groupID] = nil
		return
	}
	if userID := strings.TrimPrefix(stateID, "qq_private:"); userID != stateID {
		s.privateUnread[userID] = nil
	}
}

func (s *rootSession) enter(stateID string) (string, error) {
	stateID = strings.TrimSpace(stateID)
	if stateID == "" {
		return "", fmt.Errorf("ENTER_TARGET_NOT_AVAILABLE")
	}
	if !s.isAvailableState(stateID) {
		return "", fmt.Errorf("ENTER_TARGET_NOT_AVAILABLE: %s", stateID)
	}
	if s.focused() == stateID {
		return s.focusReminder(stateID), nil
	}
	if !s.canEnterFromFocused(stateID) {
		s.mu.Lock()
		s.stack = []string{"portal"}
		s.mu.Unlock()
	}
	s.mu.Lock()
	s.stack = append(s.stack, stateID)
	s.mu.Unlock()
	return s.focusReminder(stateID), nil
}

func (s *rootSession) back() string {
	s.mu.Lock()
	if len(s.stack) > 1 {
		s.stack = s.stack[:len(s.stack)-1]
	}
	stateID := s.stack[len(s.stack)-1]
	s.mu.Unlock()
	return s.focusReminder(stateID)
}

func (s *rootSession) zoneOut(thought string) {
	s.mu.Lock()
	s.lastZoneThought = strings.TrimSpace(thought)
	s.mu.Unlock()
}

func (s *rootSession) consume(event AgentEvent, rendered string) (message string, visible bool, shouldTrigger bool) {
	switch event.Type {
	case "napcat_friend_list_updated":
		s.mu.Lock()
		for _, friend := range friendItems(event.Data["friends"]) {
			userID := commonString(friend["userId"])
			if strings.TrimSpace(userID) != "" {
				s.privateKnown[userID] = true
			}
		}
		s.mu.Unlock()
		return s.portalReminder(), true, false
	case "napcat_group_message":
		groupID := commonString(event.Data["groupId"])
		stateID := "qq_group:" + groupID
		s.mu.Lock()
		focused := s.stack[len(s.stack)-1]
		if focused != stateID {
			s.groupUnread[groupID] = appendLimited(s.groupUnread[groupID], rendered, s.unreadLimit())
			s.pushNotificationLocked(stateID)
			s.mu.Unlock()
			return "", false, false
		}
		// 与 TS 保持一致：若当前已经聚焦该群，新消息直接进入当前状态上下文并触发一轮。
		// 只有跨状态消息才先累积为未读，等待模型自行切换。
		s.mu.Unlock()
		return rendered, true, true
	case "napcat_private_message":
		userID := commonString(event.Data["userId"])
		stateID := "qq_private:" + userID
		s.mu.Lock()
		s.privateKnown[userID] = true
		focused := s.stack[len(s.stack)-1]
		if focused != stateID {
			s.privateUnread[userID] = appendLimited(s.privateUnread[userID], rendered, s.unreadLimit())
			s.pushNotificationLocked(stateID)
			s.mu.Unlock()
			return "", false, false
		}
		// 与 TS 保持一致：若当前已经聚焦该私聊，新消息直接触发一轮。
		s.mu.Unlock()
		return rendered, true, true
	case "news_article_ingested":
		s.mu.Lock()
		s.ithomeUnread++
		s.mu.Unlock()
		return s.portalReminder(), true, false
	case "news_articles_ingested":
		count := len(intSlice(event.Data["articleIds"]))
		if count == 0 {
			count = 1
		}
		s.mu.Lock()
		s.ithomeUnread += count
		s.mu.Unlock()
		return s.portalReminder(), true, false
	default:
		return rendered, true, true
	}
}

func (s *rootSession) availableInvokeTools() []string {
	stateID := s.focused()
	switch {
	case strings.HasPrefix(stateID, "qq_group:"), strings.HasPrefix(stateID, "qq_private:"):
		return []string{"send_message", "search_web", "search_memory"}
	case stateID == "ithome":
		return []string{"open_ithome_article"}
	case stateID == "terminal":
		return []string{"bash", "read_bash_output"}
	case stateID == "zone_out":
		return []string{"zone_out"}
	default:
		return []string{}
	}
}

func (s *rootSession) currentChatTarget() (chatReplyTarget, bool) {
	stateID := s.focused()
	if groupID := strings.TrimPrefix(stateID, "qq_group:"); groupID != stateID {
		return chatReplyTarget{Type: "group", ID: groupID}, true
	}
	if userID := strings.TrimPrefix(stateID, "qq_private:"); userID != stateID {
		return chatReplyTarget{Type: "private", ID: userID}, true
	}
	return chatReplyTarget{}, false
}

func (s *rootSession) canInvoke(tool string) bool {
	for _, name := range s.availableInvokeTools() {
		if name == tool {
			return true
		}
	}
	return false
}

func (s *rootSession) displayName(stateID string) string {
	switch {
	case stateID == "portal":
		return "Portal"
	case stateID == "ithome":
		return "IT 之家"
	case stateID == "terminal":
		return "Terminal"
	case stateID == "zone_out":
		return "Zone Out"
	case strings.HasPrefix(stateID, "qq_group:"):
		return "QQ群 " + strings.TrimPrefix(stateID, "qq_group:")
	case strings.HasPrefix(stateID, "qq_private:"):
		return "QQ私聊 " + strings.TrimPrefix(stateID, "qq_private:")
	default:
		return stateID
	}
}

func (s *rootSession) description(stateID string) string {
	switch {
	case stateID == "portal":
		return "主入口，可进入 QQ 群聊、私聊、IT 之家或终端状态。"
	case strings.HasPrefix(stateID, "qq_group:"):
		return "当前聚焦 QQ 群聊，可发送群消息。"
	case strings.HasPrefix(stateID, "qq_private:"):
		return "当前聚焦 QQ 私聊，可发送私聊消息。"
	case stateID == "ithome":
		return "当前聚焦 IT 之家资讯。"
	case stateID == "terminal":
		return "当前聚焦终端能力。"
	case stateID == "zone_out":
		return "当前处于神游状态。"
	default:
		return ""
	}
}

func (s *rootSession) stackSnapshot() []any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]any, 0, len(s.stack))
	for _, id := range s.stack {
		out = append(out, map[string]any{"id": id, "displayName": s.displayName(id)})
	}
	return out
}

func (s *rootSession) childrenSnapshot() []any {
	children := []any{}
	for _, group := range s.cfg.Server.Napcat.ListenGroupIDs {
		id := "qq_group:" + group
		children = append(children, map[string]any{"id": id, "displayName": s.displayName(id), "unreadCount": len(s.groupUnread[group])})
	}
	s.mu.Lock()
	privateIDs := make([]string, 0, len(s.privateKnown))
	for id := range s.privateKnown {
		privateIDs = append(privateIDs, id)
	}
	s.mu.Unlock()
	for _, userID := range privateIDs {
		id := "qq_private:" + userID
		children = append(children, map[string]any{"id": id, "displayName": s.displayName(id), "unreadCount": len(s.privateUnread[userID])})
	}
	s.mu.Lock()
	ithomeUnread := s.ithomeUnread
	ithomeEntered := s.ithomeEntered
	s.mu.Unlock()
	children = append(children, map[string]any{"id": "ithome", "displayName": "IT 之家", "unreadCount": ithomeUnread, "hasEntered": ithomeEntered})
	if s.terminalEnabled {
		children = append(children, map[string]any{"id": "terminal", "displayName": "Terminal"})
	}
	children = append(children, map[string]any{"id": "zone_out", "displayName": "Zone Out"})
	return children
}

func (s *rootSession) isAvailableState(stateID string) bool {
	if stateID == "portal" || stateID == "ithome" || stateID == "zone_out" {
		return true
	}
	if stateID == "terminal" {
		return s.terminalEnabled
	}
	if groupID := strings.TrimPrefix(stateID, "qq_group:"); groupID != stateID {
		for _, id := range s.cfg.Server.Napcat.ListenGroupIDs {
			if id == groupID {
				return true
			}
		}
	}
	if userID := strings.TrimPrefix(stateID, "qq_private:"); userID != stateID {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.privateKnown[userID]
	}
	return false
}

func (s *rootSession) canEnterFromFocused(stateID string) bool {
	s.mu.Lock()
	focused := s.stack[len(s.stack)-1]
	s.mu.Unlock()
	if focused == "portal" {
		return stateID != "portal"
	}
	return false
}

func (s *rootSession) focusReminder(stateID string) string {
	switch {
	case stateID == "portal":
		return s.portalReminder()
	case stateID == "ithome":
		s.mu.Lock()
		wasUnread := s.ithomeUnread
		s.ithomeUnread = 0
		s.ithomeEntered = true
		s.entered[stateID] = true
		s.mu.Unlock()
		return s.ithomeArticleList(wasUnread)
	case strings.HasPrefix(stateID, "qq_group:"):
		groupID := strings.TrimPrefix(stateID, "qq_group:")
		s.mu.Lock()
		unread := append([]string(nil), s.groupUnread[groupID]...)
		s.groupUnread[groupID] = nil
		firstEnter := !s.entered[stateID]
		s.entered[stateID] = true
		s.mu.Unlock()
		if len(unread) == 0 && firstEnter {
			unread = s.recentMessages("group", groupID)
		}
		return renderStateReminder(stateID, s.availableInvokeTools(), unread)
	case strings.HasPrefix(stateID, "qq_private:"):
		userID := strings.TrimPrefix(stateID, "qq_private:")
		s.mu.Lock()
		unread := append([]string(nil), s.privateUnread[userID]...)
		s.privateUnread[userID] = nil
		firstEnter := !s.entered[stateID]
		s.entered[stateID] = true
		s.mu.Unlock()
		if len(unread) == 0 && firstEnter {
			unread = s.recentMessages("private", userID)
		}
		return renderStateReminder(stateID, s.availableInvokeTools(), unread)
	default:
		return renderStateReminder(stateID, s.availableInvokeTools(), nil)
	}
}

func (s *rootSession) portalGroups() []prompts.PortalTarget {
	groups := make([]prompts.PortalTarget, 0, len(s.cfg.Server.Napcat.ListenGroupIDs))
	for _, id := range s.cfg.Server.Napcat.ListenGroupIDs {
		stateID := "qq_group:" + id
		s.mu.Lock()
		unread := len(s.groupUnread[id])
		summary := unreadSummaryText(unread, s.groupUnread[id])
		entered := s.entered[stateID]
		s.mu.Unlock()
		groups = append(groups, prompts.PortalTarget{
			Label:            "QQ群 " + id,
			Kind:             "qq_group",
			HasEntered:       entered,
			UnreadCount:      unread,
			Summary:          summary,
			EnterCommandText: fmt.Sprintf(`enter(id="qq_group:%s")`, id),
		})
	}
	return groups
}

func (s *rootSession) portalPrivates() []prompts.PortalTarget {
	s.mu.Lock()
	privateIDs := make([]string, 0, len(s.privateKnown))
	for id := range s.privateKnown {
		privateIDs = append(privateIDs, id)
	}
	sort.Strings(privateIDs)
	privates := make([]prompts.PortalTarget, 0, len(privateIDs))
	for _, userID := range privateIDs {
		stateID := "qq_private:" + userID
		unread := len(s.privateUnread[userID])
		privates = append(privates, prompts.PortalTarget{
			Label:            "QQ私聊 " + userID,
			Kind:             "qq_private",
			HasEntered:       s.entered[stateID],
			UnreadCount:      unread,
			Summary:          unreadSummaryText(unread, s.privateUnread[userID]),
			EnterCommandText: fmt.Sprintf(`enter(id="qq_private:%s")`, userID),
		})
	}
	s.mu.Unlock()
	return privates
}

func (s *rootSession) portalFeeds() []prompts.PortalTarget {
	s.mu.Lock()
	unread := s.ithomeUnread
	entered := s.ithomeEntered
	s.mu.Unlock()
	return []prompts.PortalTarget{{
		Label:            "IT 之家",
		Kind:             "ithome",
		HasEntered:       entered,
		UnreadCount:      unread,
		EnterCommandText: `enter(id="ithome")`,
	}}
}

func (s *rootSession) ithomeArticleList(previousUnread int) string {
	data := s.store.Snapshot()
	articles := ithomeArticlesNewestFirst(data.NewsArticles)
	cursor, hasCursor := s.store.NewsFeedCursor("ithome")
	modeNew := false
	totalNew := 0
	if hasCursor {
		newer := []db.NewsArticle{}
		for _, article := range articles {
			if articleNewerThanCursor(article, cursor) {
				newer = append(newer, article)
			}
		}
		totalNew = len(newer)
		if totalNew > 0 {
			articles = newer
			modeNew = true
		}
	}
	limit := s.cfg.Server.News.Ithome.RecentArticleLimit
	if limit <= 0 || limit > len(articles) {
		limit = len(articles)
	}
	summaries := make([]prompts.ArticleSummary, 0, limit)
	for _, article := range articles[:limit] {
		summaries = append(summaries, prompts.ArticleSummary{
			ID:              article.ID,
			Title:           article.Title,
			PublishedAtText: article.PublishedAt.Format("2006-01-02 15:04"),
			URL:             article.URL,
			RSSSummary:      article.RSSSummary,
		})
	}
	if len(articles) > 0 {
		s.store.UpsertNewsFeedCursor(db.NewsFeedCursor{
			SourceKey:           "ithome",
			LastSeenArticleID:   articles[0].ID,
			LastSeenPublishedAt: articles[0].PublishedAt,
		})
	}
	if !hasCursor {
		modeNew = false
		totalNew = 0
	} else if totalNew == 0 {
		totalNew = previousUnread
	}
	hidden := totalNew - len(summaries)
	if hidden < 0 {
		hidden = 0
	}
	return prompts.ITHomeArticleListInstruction("IT 之家", modeNew, hidden, summaries)
}

func ithomeArticlesNewestFirst(items []db.NewsArticle) []db.NewsArticle {
	out := []db.NewsArticle{}
	for _, article := range items {
		if article.SourceKey == "ithome" {
			out = append(out, article)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PublishedAt.Equal(out[j].PublishedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].PublishedAt.After(out[j].PublishedAt)
	})
	return out
}

func articleNewerThanCursor(article db.NewsArticle, cursor db.NewsFeedCursor) bool {
	if article.PublishedAt.After(cursor.LastSeenPublishedAt) {
		return true
	}
	return article.PublishedAt.Equal(cursor.LastSeenPublishedAt) && article.ID > cursor.LastSeenArticleID
}

func (s *rootSession) notificationFor(stateID string) string {
	count := 0
	s.mu.Lock()
	if groupID := strings.TrimPrefix(stateID, "qq_group:"); groupID != stateID {
		count = len(s.groupUnread[groupID])
	} else if userID := strings.TrimPrefix(stateID, "qq_private:"); userID != stateID {
		count = len(s.privateUnread[userID])
	}
	s.mu.Unlock()
	if count > 0 {
		return fmt.Sprintf(`<system_reminder kind="notification" time="%s">
state_id: %s
unread: %d
instruction: enter(id=%q) to inspect or reply.
</system_reminder>`, time.Now().Format("2006-01-02 15:04:05 -07:00"), stateID, count, stateID)
	}
	return fmt.Sprintf(`<system_reminder kind="notification" time="%s">
state_id: %s
instruction: enter(id=%q) to inspect or reply.
</system_reminder>`, time.Now().Format("2006-01-02 15:04:05 -07:00"), stateID, stateID)
}

func (s *rootSession) pushNotificationLocked(stateID string) {
	s.notifications[stateID] = notificationEntry{
		StateID:     stateID,
		DisplayName: s.displayNameUnlocked(stateID),
		Summary:     s.notificationSummaryUnlocked(stateID),
		At:          time.Now(),
	}
}

func (s *rootSession) flushNotificationsIfReady(window time.Duration) string {
	if window <= 0 {
		window = 30 * time.Second
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.notifications) == 0 || time.Since(s.lastNotifyFlush) < window {
		return ""
	}
	entries := make([]notificationEntry, 0, len(s.notifications))
	for _, entry := range s.notifications {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].StateID < entries[j].StateID })
	s.notifications = map[string]notificationEntry{}
	s.lastNotifyFlush = time.Now()
	lines := []string{
		fmt.Sprintf(`<system_reminder kind="cross_state_notification" time="%s">`, time.Now().Format("2006-01-02 15:04:05 -07:00")),
		"states:",
	}
	for _, entry := range entries {
		lines = append(lines, fmt.Sprintf("- id: %s", entry.StateID))
		lines = append(lines, fmt.Sprintf("  label: %s", entry.DisplayName))
		lines = append(lines, fmt.Sprintf("  summary: %s", entry.Summary))
		lines = append(lines, fmt.Sprintf("  enter: enter(id=%q)", entry.StateID))
	}
	lines = append(lines, "instruction: use back first if needed, then enter target; or stay if current conversation is better.")
	lines = append(lines, "</system_reminder>")
	return strings.Join(lines, "\n")
}

func (s *rootSession) displayNameUnlocked(stateID string) string {
	switch {
	case stateID == "portal":
		return "门户"
	case stateID == "ithome":
		return "IT 之家"
	case stateID == "terminal":
		return "终端"
	case stateID == "zone_out":
		return "神游"
	case strings.HasPrefix(stateID, "qq_group:"):
		return "QQ群 " + strings.TrimPrefix(stateID, "qq_group:")
	case strings.HasPrefix(stateID, "qq_private:"):
		return "QQ私聊 " + strings.TrimPrefix(stateID, "qq_private:")
	default:
		return stateID
	}
}

func (s *rootSession) notificationSummaryUnlocked(stateID string) string {
	if groupID := strings.TrimPrefix(stateID, "qq_group:"); groupID != stateID {
		return unreadSummaryText(len(s.groupUnread[groupID]), s.groupUnread[groupID])
	}
	if userID := strings.TrimPrefix(stateID, "qq_private:"); userID != stateID {
		return unreadSummaryText(len(s.privateUnread[userID]), s.privateUnread[userID])
	}
	return "有新的活动。"
}

func unreadSummaryText(count int, unread []string) string {
	if count <= 0 {
		return "有新的活动。"
	}
	if latest := latestUnreadSummary(unread); latest != "" {
		return fmt.Sprintf("未读 %d 条消息。最新：%s", count, latest)
	}
	return fmt.Sprintf("未读 %d 条消息。", count)
}

func latestUnreadSummary(unread []string) string {
	if len(unread) == 0 {
		return ""
	}
	raw := strings.TrimSpace(unread[len(unread)-1])
	if raw == "" {
		return ""
	}
	lines := strings.Split(raw, "\n")
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "<") {
			continue
		}
		parts = append(parts, line)
	}
	if len(parts) == 0 {
		return ""
	}
	return trimPreview(strings.Join(parts, " / "), 80)
}

func (s *rootSession) unreadLimit() int {
	limit := s.cfg.Server.Napcat.StartupContextRecentMessageCount
	if limit <= 0 {
		return 40
	}
	return limit
}

func (s *rootSession) recentMessages(kind, id string) []string {
	limit := s.unreadLimit()
	if s.recentProvider != nil {
		if kind == "group" {
			if items := s.recentProvider.RecentGroupMessages(id, limit); len(items) > 0 {
				return items
			}
		} else if items := s.recentProvider.RecentPrivateMessages(id, limit); len(items) > 0 {
			return items
		}
	}
	if s.store == nil {
		return nil
	}
	data := s.store.Snapshot()
	out := []string{}
	for i := len(data.NapcatMessages) - 1; i >= 0; i-- {
		msg := data.NapcatMessages[i]
		if kind == "group" {
			if msg.MessageType != "group" || msg.GroupID == nil || *msg.GroupID != id {
				continue
			}
		} else if msg.MessageType != "private" || msg.UserID == nil || *msg.UserID != id {
			continue
		}
		nickname := "未知用户"
		if msg.Nickname != nil && *msg.Nickname != "" {
			nickname = *msg.Nickname
		}
		raw := commonString(msg.Payload["raw_message"])
		if rendered := commonString(msg.Payload["rendered_message"]); strings.TrimSpace(rendered) != "" {
			raw = rendered
		}
		userID := ""
		if msg.UserID != nil {
			userID = *msg.UserID
		}
		messageTime := msg.CreatedAt
		if msg.EventTime != nil && !msg.EventTime.IsZero() {
			messageTime = *msg.EventTime
		}
		out = append(out, prompts.QQMessageAt(nickname, userID, raw, messageTime))
		if len(out) >= limit {
			break
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

type facadeEnterTool struct{ Session *rootSession }

func (facadeEnterTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "enter", Description: "进入一个可用状态，例如 qq_group:562223500、qq_private:用户QQ、ithome、terminal 或 zone_out", Parameters: agentruntime.ObjectSchema(map[string]any{
		"id": map[string]any{"type": "string"},
		"os": map[string]any{"type": "string", "description": "可选。公开展示用的一句话 OS/旁白，不是私密推理链。"},
	})}
}
func (facadeEnterTool) Kind() string { return "control" }
func (t facadeEnterTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	id := ""
	kind, _ := call.Arguments["kind"].(string)
	switch kind {
	case "qq_group":
		groupID, _ := call.Arguments["groupId"].(string)
		if groupID == "" {
			groupID, _ = call.Arguments["id"].(string)
		}
		if groupID != "" {
			id = "qq_group:" + groupID
		}
	case "qq_private":
		userID, _ := call.Arguments["userId"].(string)
		if userID == "" {
			userID, _ = call.Arguments["id"].(string)
		}
		if userID != "" {
			id = "qq_private:" + userID
		}
	case "ithome", "zone_out", "terminal":
		id = kind
	default:
		id, _ = call.Arguments["id"].(string)
		if id == "" {
			id, _ = call.Arguments["stateId"].(string)
		}
	}
	reminder, err := t.Session.enter(id)
	if err != nil {
		return agentruntime.ToolResult{Kind: "control", Content: mustSessionJSON(map[string]any{
			"ok":             false,
			"error":          err.Error(),
			"focusedStateId": t.Session.focused(),
			"allowedTools":   t.Session.availableInvokeTools(),
		})}, nil
	}
	return agentruntime.ToolResult{Kind: "control", Content: mustSessionJSON(map[string]any{
		"ok":             true,
		"entered":        id,
		"focusedStateId": t.Session.focused(),
		"allowedTools":   t.Session.availableInvokeTools(),
		"context":        reminder,
	})}, nil
}

type facadeBackTool struct{ Session *rootSession }

func (facadeBackTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "back", Description: "返回上一级状态；在顶层时保持 portal", Parameters: agentruntime.ObjectSchema(map[string]any{
		"os": map[string]any{"type": "string", "description": "可选。公开展示用的一句话 OS/旁白，不是私密推理链。"},
	})}
}
func (facadeBackTool) Kind() string { return "control" }
func (t facadeBackTool) Execute(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	reminder := t.Session.back()
	return agentruntime.ToolResult{Kind: "control", Content: mustSessionJSON(map[string]any{
		"ok":             true,
		"focusedStateId": t.Session.focused(),
		"allowedTools":   t.Session.availableInvokeTools(),
		"context":        reminder,
	})}, nil
}

type facadeZoneOutTool struct{ Session *rootSession }

func (facadeZoneOutTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "zone_out", Description: "不主动响应，记录当前选择或想法", Parameters: agentruntime.ObjectSchema(map[string]any{
		"thought": map[string]any{"type": "string"},
		"os":      map[string]any{"type": "string", "description": "可选。公开展示用的一句话 OS/旁白，不是私密推理链。"},
	})}
}
func (facadeZoneOutTool) Kind() string { return "control" }
func (t facadeZoneOutTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	thought, _ := call.Arguments["thought"].(string)
	t.Session.zoneOut(thought)
	return agentruntime.ToolResult{Kind: "control", Content: mustSessionJSON(map[string]any{
		"ok":             true,
		"zonedOut":       true,
		"focusedStateId": t.Session.focused(),
		"allowedTools":   t.Session.availableInvokeTools(),
	})}, nil
}

func renderStateReminder(stateID string, tools []string, unread []string) string {
	lines := []string{
		fmt.Sprintf(`<system_reminder kind="state" time="%s">`, time.Now().Format("2006-01-02 15:04:05 -07:00")),
		"current_state: " + stateID,
		"allowed_invoke_tools: " + strings.Join(tools, ", "),
	}
	if len(unread) > 0 {
		lines = append(lines, "unread_messages:")
		lines = append(lines, unread...)
	}
	lines = append(lines, "</system_reminder>")
	return strings.Join(lines, "\n")
}

func appendLimited(items []string, item string, limit int) []string {
	items = append(items, item)
	if limit > 0 && len(items) > limit {
		items = items[len(items)-limit:]
	}
	return items
}

func cloneStringSlices(input map[string][]string) map[string][]string {
	out := map[string][]string{}
	for key, value := range input {
		out[key] = append([]string(nil), value...)
	}
	return out
}

func cloneBoolMap(input map[string]bool) map[string]bool {
	out := map[string]bool{}
	for key, value := range input {
		out[key] = value
	}
	return out
}

func commonString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func friendItems(v any) []map[string]any {
	switch items := v.(type) {
	case []map[string]any:
		return items
	case []any:
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func mustSessionJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return `{"ok":false,"error":"JSON_ENCODE_FAILED"}`
	}
	return string(data)
}
