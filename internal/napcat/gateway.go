package napcat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	rootagent "qqbot-ai/internal/agent"
	"qqbot-ai/internal/capabilities/vision"
	"qqbot-ai/internal/common"
	"qqbot-ai/internal/config"
	"qqbot-ai/internal/db"
	"qqbot-ai/internal/prompts"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// NapcatGateway 负责 NapCat websocket 连接和请求关联。
//
// 它会持久化原始事件、标准化消息事件，并发布 Agent 事件
// 到根事件队列中。
type NapcatGateway struct {
	cfg      *config.Config
	store    *db.Store
	events   *rootagent.EventQueue
	mu       sync.Mutex
	writeMu  sync.Mutex
	conn     *websocket.Conn
	pending  map[string]chan napcatResponse
	vision   vision.Agent
	stopOnce sync.Once
	cancel   context.CancelFunc
}

type napcatResponse struct {
	Status  string `json:"status"`
	Retcode int    `json:"retcode"`
	Data    any    `json:"data"`
	Echo    string `json:"echo"`
	Message string `json:"message"`
	Wording string `json:"wording"`
}

// NewNapcatGateway 创建一个尚未启动的网关。
func NewNapcatGateway(cfg *config.Config, store *db.Store, events *rootagent.EventQueue, analyzer vision.Agent) *NapcatGateway {
	return &NapcatGateway{cfg: cfg, store: store, events: events, pending: map[string]chan napcatResponse{}, vision: analyzer}
}

func (g *NapcatGateway) Start(parent context.Context) error {
	if g.cfg.Server.Napcat.WSURL == "" {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	g.cancel = cancel
	go g.connectLoop(ctx)
	return nil
}

func (g *NapcatGateway) Stop() {
	g.stopOnce.Do(func() {
		if g.cancel != nil {
			g.cancel()
		}
		g.mu.Lock()
		if g.conn != nil {
			_ = g.conn.Close()
		}
		g.mu.Unlock()
	})
}

func (g *NapcatGateway) connectLoop(ctx context.Context) {
	delay := time.Duration(g.cfg.Server.Napcat.ReconnectMs) * time.Millisecond
	if delay <= 0 {
		delay = 3 * time.Second
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		conn, _, err := websocket.DefaultDialer.DialContext(ctx, g.cfg.Server.Napcat.WSURL, http.Header{})
		if err != nil {
			g.store.Log("warn", "NapCat websocket connect failed", map[string]any{"event": "napcat.gateway.connect_failed", "error": err.Error()})
			time.Sleep(delay)
			continue
		}
		g.mu.Lock()
		g.conn = conn
		g.mu.Unlock()
		g.store.Log("info", "NapCat websocket connected", map[string]any{"event": "napcat.gateway.connected", "wsUrl": g.cfg.Server.Napcat.WSURL})
		go g.hydrateStartupContext(ctx)
		g.readLoop(ctx, conn)
		g.mu.Lock()
		if g.conn == conn {
			g.conn = nil
		}
		g.mu.Unlock()
		time.Sleep(delay)
	}
}

func (g *NapcatGateway) readLoop(ctx context.Context, conn *websocket.Conn) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			g.store.Log("warn", "NapCat websocket disconnected", map[string]any{"event": "napcat.gateway.disconnected", "error": err.Error()})
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			continue
		}
		if echo := common.AsString(payload["echo"]); echo != "" {
			var resp napcatResponse
			_ = json.Unmarshal(data, &resp)
			g.resolve(echo, resp)
			continue
		}
		g.handleEvent(payload)
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func (g *NapcatGateway) resolve(echo string, resp napcatResponse) {
	g.mu.Lock()
	ch := g.pending[echo]
	delete(g.pending, echo)
	g.mu.Unlock()
	if ch != nil {
		ch <- resp
	}
}

func (g *NapcatGateway) Request(action string, params map[string]any) (any, error) {
	g.mu.Lock()
	conn := g.conn
	if conn == nil {
		g.mu.Unlock()
		return nil, fmt.Errorf("NapCat WebSocket 未连接")
	}
	echo := common.NewID()
	ch := make(chan napcatResponse, 1)
	g.pending[echo] = ch
	g.mu.Unlock()
	g.writeMu.Lock()
	if err := conn.WriteJSON(map[string]any{"action": action, "params": params, "echo": echo}); err != nil {
		g.writeMu.Unlock()
		g.resolve(echo, napcatResponse{})
		return nil, err
	}
	g.writeMu.Unlock()
	timeout := time.Duration(g.cfg.Server.Napcat.RequestTimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	select {
	case resp := <-ch:
		if resp.Status != "ok" || resp.Retcode != 0 {
			if resp.Wording != "" {
				return nil, errors.New(resp.Wording)
			}
			if resp.Message != "" {
				return nil, errors.New(resp.Message)
			}
			return nil, fmt.Errorf("NapCat 返回错误: %d", resp.Retcode)
		}
		return resp.Data, nil
	case <-time.After(timeout):
		g.mu.Lock()
		delete(g.pending, echo)
		g.mu.Unlock()
		return nil, fmt.Errorf("NapCat 请求超时")
	}
}

func (g *NapcatGateway) hydrateStartupContext(ctx context.Context) {
	select {
	case <-time.After(1200 * time.Millisecond):
	case <-ctx.Done():
		return
	}
	g.refreshFriendList()
	g.hydrateRecentGroupMessages()
}

func (g *NapcatGateway) refreshFriendList() {
	data, err := g.Request("get_friend_list", map[string]any{})
	if err != nil {
		g.store.Log("warn", "NapCat friend list refresh failed", map[string]any{"event": "napcat.gateway.friend_list_refresh_failed", "error": err.Error()})
		return
	}
	friends := normalizeFriendList(data)
	if len(friends) == 0 {
		return
	}
	g.events.Enqueue(rootagent.AgentEvent{Type: "napcat_friend_list_updated", Data: map[string]any{"friends": friends}})
}

func (g *NapcatGateway) hydrateRecentGroupMessages() {
	count := g.cfg.Server.Napcat.StartupContextRecentMessageCount
	if count <= 0 {
		return
	}
	for _, groupID := range g.cfg.Server.Napcat.ListenGroupIDs {
		data, err := g.Request("get_group_msg_history", map[string]any{"group_id": groupID, "count": count})
		if err != nil {
			g.store.Log("warn", "NapCat startup group history failed", map[string]any{"event": "agent.startup_context_group_hydrate_failed", "groupId": groupID, "error": err.Error()})
			continue
		}
		messages := normalizeHistoryMessages(data)
		for _, payload := range messages {
			payload["message_type"] = "group"
			payload["group_id"] = groupID
			if userID := common.AsString(payload["user_id"]); userID != "" && g.cfg.Server.Bot.QQ != "" && userID == g.cfg.Server.Bot.QQ {
				continue
			}
			g.handleHydratedMessage(payload)
		}
	}
}

func (g *NapcatGateway) handleHydratedMessage(payload map[string]any) {
	messageType := common.AsString(payload["message_type"])
	if messageType == "" {
		return
	}
	groupID := db.StringPtr(payload["group_id"])
	userID := db.StringPtr(payload["user_id"])
	nickname := ""
	if sender, ok := payload["sender"].(map[string]any); ok {
		nickname = firstNonEmpty(common.AsString(sender["card"]), common.AsString(sender["nickname"]))
	}
	raw := g.renderIncomingMessageWithoutImageAnalysis(payload)
	if raw == "" {
		raw = common.AsString(payload["raw_message"])
	}
	payload["rendered_message"] = raw
	messageID := db.IntPtr(payload["message_id"])
	var eventTime *time.Time
	if t := db.IntPtr(payload["time"]); t != nil {
		eventTime = new(time.Unix(int64(*t), 0))
	}
	item := db.NapcatMessageItem{MessageType: messageType, SubType: "startup", GroupID: groupID, UserID: userID, Nickname: ptrOrNil(nickname), MessageID: messageID, Message: payload["message"], EventTime: eventTime, Payload: payload}
	seq := g.store.AddNapcatMessage(item)
	eventType := "napcat_group_message"
	if messageType == "private" {
		eventType = "napcat_private_message"
	}
	at := time.Now()
	if eventTime != nil && !eventTime.IsZero() {
		at = *eventTime
	}
	g.events.Enqueue(rootagent.AgentEvent{Type: eventType, Data: map[string]any{"groupId": deref(groupID), "userId": deref(userID), "nickname": nickname, "rawMessage": raw, "messageId": valueInt(messageID), "messageSeq": seq, "startup": true}, At: at})
}

func (g *NapcatGateway) SendGroupMessage(groupID, message string) (int, error) {
	data, err := g.Request("send_group_msg", map[string]any{"group_id": groupID, "message": parseOutgoingMessage(message)})
	if err != nil {
		return 0, err
	}
	if m, ok := data.(map[string]any); ok {
		if id := db.IntPtr(m["message_id"]); id != nil {
			return *id, nil
		}
	}
	return 0, fmt.Errorf("NapCat 返回结果缺少 message_id")
}

func (g *NapcatGateway) SendPrivateMessage(userID, message string) (int, error) {
	data, err := g.Request("send_private_msg", map[string]any{"user_id": userID, "message": parseOutgoingMessage(message)})
	if err != nil {
		return 0, err
	}
	if m, ok := data.(map[string]any); ok {
		if id := db.IntPtr(m["message_id"]); id != nil {
			return *id, nil
		}
	}
	return 0, fmt.Errorf("NapCat 返回结果缺少 message_id")
}

func (g *NapcatGateway) RecentGroupMessages(groupID string, count int) []string {
	if count <= 0 {
		return nil
	}
	data, err := g.Request("get_group_msg_history", map[string]any{"group_id": groupID, "count": count})
	if err != nil {
		g.store.Log("warn", "NapCat recent group messages failed", map[string]any{"event": "napcat.gateway.recent_group_failed", "groupId": groupID, "error": err.Error()})
		return nil
	}
	return g.renderHistoryMessages(normalizeHistoryMessages(data), "group", groupID)
}

func (g *NapcatGateway) RecentPrivateMessages(userID string, count int) []string {
	if count <= 0 {
		return nil
	}
	data, err := g.Request("get_friend_msg_history", map[string]any{"user_id": userID, "count": count})
	if err != nil {
		g.store.Log("warn", "NapCat recent private messages failed", map[string]any{"event": "napcat.gateway.recent_private_failed", "userId": userID, "error": err.Error()})
		return nil
	}
	return g.renderHistoryMessages(normalizeHistoryMessages(data), "private", userID)
}

func (g *NapcatGateway) renderHistoryMessages(items []map[string]any, kind, id string) []string {
	out := make([]string, 0, len(items))
	for _, payload := range items {
		payload["message_type"] = kind
		if kind == "group" {
			payload["group_id"] = id
		} else {
			payload["user_id"] = id
		}
		nickname := "未知用户"
		if sender, ok := payload["sender"].(map[string]any); ok {
			nickname = firstNonEmpty(common.AsString(sender["card"]), common.AsString(sender["nickname"]))
		}
		userID := common.AsString(payload["user_id"])
		raw := g.renderIncomingMessageWithoutImageAnalysis(payload)
		if raw == "" {
			raw = common.AsString(payload["raw_message"])
		}
		if strings.TrimSpace(raw) != "" {
			out = append(out, prompts.QQMessageAt(nickname, userID, raw, payloadMessageTime(payload)))
		}
	}
	return out
}

func payloadMessageTime(payload map[string]any) time.Time {
	switch v := payload["time"].(type) {
	case int:
		return time.Unix(int64(v), 0)
	case int64:
		return time.Unix(v, 0)
	case float64:
		if v > 0 {
			return time.Unix(int64(v), 0)
		}
	case json.Number:
		if n, err := v.Int64(); err == nil && n > 0 {
			return time.Unix(n, 0)
		}
	}
	return time.Time{}
}

func (g *NapcatGateway) handleEvent(payload map[string]any) {
	postType := common.AsString(payload["post_type"])
	messageType := db.StringPtr(payload["message_type"])
	subType := db.StringPtr(payload["sub_type"])
	userID := db.StringPtr(payload["user_id"])
	groupID := db.StringPtr(payload["group_id"])
	var eventTime *time.Time
	if t := db.IntPtr(payload["time"]); t != nil {
		eventTime = new(time.Unix(int64(*t), 0))
	}
	g.store.AddNapcatEvent(db.NapcatEventItem{PostType: postType, MessageType: messageType, SubType: subType, UserID: userID, GroupID: groupID, EventTime: eventTime, Payload: payload})
	if postType != "message" || messageType == nil {
		return
	}
	if *messageType == "group" && groupID != nil && !contains(g.cfg.Server.Napcat.ListenGroupIDs, *groupID) {
		g.store.Log("info", "NapCat group message ignored by listenGroupIds", map[string]any{"event": "napcat.message.ignored_group", "groupId": *groupID, "listenGroupIds": g.cfg.Server.Napcat.ListenGroupIDs})
		return
	}
	if userID != nil && g.cfg.Server.Bot.QQ != "" && *userID == g.cfg.Server.Bot.QQ {
		g.store.Log("info", "NapCat self message ignored", map[string]any{"event": "napcat.message.ignored_self", "userId": *userID})
		return
	}
	nickname := ""
	if sender, ok := payload["sender"].(map[string]any); ok {
		nickname = common.AsString(sender["nickname"])
	}
	raw := g.renderIncomingMessage(payload)
	if raw == "" {
		raw = common.AsString(payload["raw_message"])
	}
	payload["rendered_message"] = raw
	messageID := db.IntPtr(payload["message_id"])
	item := db.NapcatMessageItem{MessageType: *messageType, SubType: valueOr(subType, "normal"), GroupID: groupID, UserID: userID, Nickname: ptrOrNil(nickname), MessageID: messageID, Message: payload["message"], EventTime: eventTime, Payload: payload}
	seq := g.store.AddNapcatMessage(item)
	g.store.Log("info", "NapCat message accepted", map[string]any{"event": "napcat.message.accepted", "messageType": *messageType, "groupId": deref(groupID), "userId": deref(userID), "messageSeq": seq, "rawMessage": raw})
	eventType := "napcat_group_message"
	if *messageType == "private" {
		eventType = "napcat_private_message"
	}
	at := time.Now()
	if eventTime != nil && !eventTime.IsZero() {
		at = *eventTime
	}
	g.events.Enqueue(rootagent.AgentEvent{Type: eventType, Data: map[string]any{"groupId": deref(groupID), "userId": deref(userID), "nickname": nickname, "rawMessage": raw, "messageId": valueInt(messageID), "messageSeq": seq}, At: at})
}

func parseOutgoingMessage(message string) []map[string]any {
	return []map[string]any{{"type": "text", "data": map[string]any{"text": message}}}
}

func (g *NapcatGateway) renderIncomingMessage(payload map[string]any) string {
	return g.renderIncomingMessageWithImageAnalysis(payload, true)
}

func (g *NapcatGateway) renderIncomingMessageWithoutImageAnalysis(payload map[string]any) string {
	return g.renderIncomingMessageWithImageAnalysis(payload, false)
}

func (g *NapcatGateway) renderIncomingMessageWithImageAnalysis(payload map[string]any, analyzeImages bool) string {
	message, ok := payload["message"].([]any)
	if !ok || len(message) == 0 {
		return strings.TrimSpace(common.AsString(payload["raw_message"]))
	}
	parts := []string{}
	for _, item := range message {
		seg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		typ := common.AsString(seg["type"])
		data, _ := seg["data"].(map[string]any)
		switch typ {
		case "text":
			parts = append(parts, common.AsString(data["text"]))
		case "at":
			qq := common.AsString(data["qq"])
			if qq == "all" {
				parts = append(parts, "@全体成员")
			} else {
				name := common.AsString(data["name"])
				if name == "" {
					name = g.resolveAtName(common.AsString(payload["group_id"]), qq)
				}
				if name != "" {
					parts = append(parts, "@"+name+"("+qq+")")
				} else {
					parts = append(parts, "@"+qq)
				}
			}
		case "reply":
			id := firstNonEmpty(common.AsString(data["id"]), common.AsString(data["message_id"]))
			if preview := g.replyPreview(id); preview != "" {
				parts = append(parts, preview)
			} else if id != "" {
				parts = append(parts, "[回复消息:"+id+"]")
			} else {
				parts = append(parts, "[回复消息]")
			}
		case "image":
			desc := firstNonEmpty(common.AsString(data["summary"]), common.AsString(data["file"]), common.AsString(data["url"]))
			if analyzeImages {
				if analyzed := g.analyzeImageSegment(data); analyzed != "" {
					desc = analyzed
				}
			}
			if desc == "" {
				desc = "图片"
			}
			parts = append(parts, "[图片:"+desc+"]")
		case "face":
			id := common.AsString(data["id"])
			if id == "" {
				parts = append(parts, "[表情]")
			} else {
				parts = append(parts, "[表情:"+id+"]")
			}
		case "record":
			parts = append(parts, "[语音]")
		case "video":
			parts = append(parts, "[视频]")
		case "file":
			name := firstNonEmpty(common.AsString(data["name"]), common.AsString(data["file"]))
			if name == "" {
				name = "文件"
			}
			parts = append(parts, "[文件:"+name+"]")
		default:
			if typ != "" {
				parts = append(parts, "["+typ+"]")
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, ""))
}

func (g *NapcatGateway) resolveAtName(groupID, userID string) string {
	if groupID == "" || userID == "" {
		return ""
	}
	data, err := g.Request("get_group_member_info", map[string]any{"group_id": groupID, "user_id": userID, "no_cache": false})
	if err != nil {
		return ""
	}
	m, ok := data.(map[string]any)
	if !ok {
		return ""
	}
	return firstNonEmpty(common.AsString(m["card"]), common.AsString(m["nickname"]))
}

func (g *NapcatGateway) replyPreview(id string) string {
	if id == "" {
		return ""
	}
	messageID := 0
	_, _ = fmt.Sscanf(id, "%d", &messageID)
	if messageID == 0 {
		return ""
	}
	data := g.store.Snapshot()
	for i := len(data.NapcatMessages) - 1; i >= 0; i-- {
		msg := data.NapcatMessages[i]
		if msg.MessageID == nil || *msg.MessageID != messageID {
			continue
		}
		nickname := ""
		if msg.Nickname != nil {
			nickname = *msg.Nickname
		}
		userID := ""
		if msg.UserID != nil {
			userID = *msg.UserID
		}
		raw := common.AsString(msg.Payload["rendered_message"])
		if raw == "" {
			raw = common.AsString(msg.Payload["raw_message"])
		}
		if len([]rune(raw)) > 50 {
			raw = string([]rune(raw)[:50]) + "…"
		}
		return fmt.Sprintf("[回复 %s(%s): %s]", nickname, userID, raw)
	}
	return ""
}

func (g *NapcatGateway) analyzeImageSegment(data map[string]any) string {
	if g.vision.Client == nil {
		return ""
	}
	url := common.AsString(data["url"])
	if url == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 6<<20))
	if err != nil || len(body) == 0 {
		return ""
	}
	mimeType := resp.Header.Get("Content-Type")
	if idx := strings.Index(mimeType, ";"); idx >= 0 {
		mimeType = mimeType[:idx]
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(body)
	}
	desc, err := g.vision.Analyze(ctx, "", []vision.ImagePart{{MimeType: mimeType, Data: body, Filename: common.AsString(data["file"])}})
	if err != nil {
		g.store.Log("warn", "NapCat image analyze failed", map[string]any{"event": "napcat.image.analyze_failed", "error": err.Error()})
		return ""
	}
	return strings.TrimSpace(desc)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func valueOr(ptr *string, fallback string) string {
	if ptr == nil || *ptr == "" {
		return fallback
	}
	return *ptr
}

func ptrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

func valueInt(ptr *int) int {
	if ptr == nil {
		return 0
	}
	return *ptr
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func normalizeFriendList(data any) []map[string]any {
	items := dataArray(data)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		userID := firstNonEmpty(common.AsString(item["user_id"]), common.AsString(item["userId"]))
		if userID == "" {
			continue
		}
		out = append(out, map[string]any{
			"userId":   userID,
			"nickname": firstNonEmpty(common.AsString(item["nickname"]), common.AsString(item["nick"])),
			"remark":   common.AsString(item["remark"]),
		})
	}
	return out
}

func normalizeHistoryMessages(data any) []map[string]any {
	if m, ok := data.(map[string]any); ok {
		for _, key := range []string{"messages", "message", "data"} {
			if items := dataArray(m[key]); len(items) > 0 {
				return items
			}
		}
	}
	return dataArray(data)
}

func dataArray(data any) []map[string]any {
	items, ok := data.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
