package ops

import (
	"embed"
	"net/http"
	rootagent "qqbot-ai/internal/agent"
	"qqbot-ai/internal/common"
	"qqbot-ai/internal/config"
	"qqbot-ai/internal/db"
	"qqbot-ai/internal/llm"
	"qqbot-ai/internal/metric"
	"qqbot-ai/internal/napcat"
	"strings"
	"time"
)

//go:embed web/static/*
var staticFiles embed.FS

type HTTPServer struct {
	cfg    *config.Config
	store  *db.Store
	llm    *llm.LLMClient
	napcat *napcat.NapcatGateway
	agent  *rootagent.AgentRuntime
	charts *metric.MetricChartService
	mux    *http.ServeMux
}

func NewHTTPServer(cfg *config.Config, store *db.Store, llmClient *llm.LLMClient, napcatGateway *napcat.NapcatGateway, agentRuntime *rootagent.AgentRuntime, charts *metric.MetricChartService) http.Handler {
	s := &HTTPServer{cfg: cfg, store: store, llm: llmClient, napcat: napcatGateway, agent: agentRuntime, charts: charts, mux: http.NewServeMux()}
	s.routes()
	return traceMiddleware(s.mux)
}

func (s *HTTPServer) routes() {
	s.mux.HandleFunc("/", s.static)
	s.mux.HandleFunc("/health", s.health)
	s.mux.HandleFunc("/llm/providers", s.llmProviders)
	s.mux.HandleFunc("/llm/playground-tools", s.llmTools)
	s.mux.HandleFunc("/llm/chat", s.llmChat)
	s.mux.HandleFunc("/napcat/group/send", s.sendGroup)
	s.mux.HandleFunc("/napcat/private/send", s.sendPrivate)
	s.mux.HandleFunc("/agent-dashboard/current", s.dashboard)
	s.mux.HandleFunc("/app-log/query", s.appLogQuery)
	s.mux.HandleFunc("/llm-chat-call/query", s.llmCallQuery)
	s.mux.HandleFunc("/napcat-event/query", s.napcatEventQuery)
	s.mux.HandleFunc("/napcat-group-message/query", s.napcatMessageQuery)
	s.mux.HandleFunc("/story/query", s.storyQuery)
	s.mux.HandleFunc("/story/reindex", s.storyReindex)
	s.mux.HandleFunc("/metric-chart/list", s.metricChartList)
	s.mux.HandleFunc("/metric-chart/data", s.metricChartData)
	s.mux.HandleFunc("/metric-chart/create", s.metricChartCreate)
	s.mux.HandleFunc("/metric-chart/delete", s.metricChartDelete)
}

func (s *HTTPServer) static(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/static/") {
		common.WriteJSON(w, http.StatusNotFound, map[string]any{"message": "not found"})
		return
	}
	path := "web/static/index.html"
	if strings.HasPrefix(r.URL.Path, "/static/") {
		path = "web/static/" + strings.TrimPrefix(r.URL.Path, "/static/")
	}
	http.ServeFileFS(w, r, staticFiles, path)
}

func traceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trace-Id", common.NewID())
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *HTTPServer) requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		common.WriteJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return false
	}
	return true
}

func (s *HTTPServer) health(w http.ResponseWriter, r *http.Request) {
	if !s.requireMethod(w, r, http.MethodGet) {
		return
	}
	common.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "timestamp": common.ISO(time.Now())})
}

func (s *HTTPServer) llmProviders(w http.ResponseWriter, r *http.Request) {
	if !s.requireMethod(w, r, http.MethodGet) {
		return
	}
	common.WriteJSON(w, http.StatusOK, s.llm.ListProviders("agent"))
}

func (s *HTTPServer) llmTools(w http.ResponseWriter, r *http.Request) {
	if !s.requireMethod(w, r, http.MethodGet) {
		return
	}
	common.WriteJSON(w, http.StatusOK, s.llm.PlaygroundTools())
}

func (s *HTTPServer) llmChat(w http.ResponseWriter, r *http.Request) {
	if !s.requireMethod(w, r, http.MethodPost) {
		return
	}
	var req llm.LLMChatRequest
	if err := common.ReadJSON(r, &req); err != nil {
		common.WriteJSON(w, http.StatusBadRequest, map[string]any{"message": "请求参数不合法"})
		return
	}
	resp, status, err := s.llm.ChatDirect(r.Context(), req)
	if err != nil {
		common.WriteJSON(w, status, map[string]any{"message": err.Error()})
		return
	}
	common.WriteJSON(w, status, resp)
}

func (s *HTTPServer) sendGroup(w http.ResponseWriter, r *http.Request) {
	if !s.requireMethod(w, r, http.MethodPost) {
		return
	}
	var req struct {
		GroupID string `json:"groupId"`
		Message string `json:"message"`
	}
	if err := common.ReadJSON(r, &req); err != nil || req.GroupID == "" || req.Message == "" {
		common.WriteJSON(w, http.StatusBadRequest, map[string]any{"message": "请求参数不合法"})
		return
	}
	id, err := s.napcat.SendGroupMessage(req.GroupID, req.Message)
	if err != nil {
		common.WriteJSON(w, http.StatusBadGateway, map[string]any{"message": err.Error()})
		return
	}
	common.WriteJSON(w, http.StatusOK, map[string]any{"messageId": id})
}

func (s *HTTPServer) sendPrivate(w http.ResponseWriter, r *http.Request) {
	if !s.requireMethod(w, r, http.MethodPost) {
		return
	}
	var req struct {
		UserID  string `json:"userId"`
		Message string `json:"message"`
	}
	if err := common.ReadJSON(r, &req); err != nil || req.UserID == "" || req.Message == "" {
		common.WriteJSON(w, http.StatusBadRequest, map[string]any{"message": "请求参数不合法"})
		return
	}
	id, err := s.napcat.SendPrivateMessage(req.UserID, req.Message)
	if err != nil {
		common.WriteJSON(w, http.StatusBadGateway, map[string]any{"message": err.Error()})
		return
	}
	common.WriteJSON(w, http.StatusOK, map[string]any{"messageId": id})
}

func (s *HTTPServer) dashboard(w http.ResponseWriter, r *http.Request) {
	if !s.requireMethod(w, r, http.MethodGet) {
		return
	}
	common.WriteJSON(w, http.StatusOK, s.agent.Snapshot(s.llm))
}

func (s *HTTPServer) appLogQuery(w http.ResponseWriter, r *http.Request) {
	if !s.requireMethod(w, r, http.MethodGet) {
		return
	}
	page, pageSize := common.ParsePage(r)
	data := s.store.Snapshot()
	items := db.NewestFirst(data.AppLogs, func(a, b db.AppLogItem) bool { return a.CreatedAt.After(b.CreatedAt) })
	level, traceID, msg, source := common.QueryString(r, "level"), common.QueryString(r, "traceId"), common.QueryString(r, "message"), common.QueryString(r, "source")
	start, end := common.ParseTimeQuery(r, "startAt"), common.ParseTimeQuery(r, "endAt")
	filtered := items[:0]
	for _, item := range items {
		if level != "" && item.Level != level {
			continue
		}
		if traceID != "" && item.TraceID != traceID {
			continue
		}
		if msg != "" && !common.ContainsFold(item.Message, msg) {
			continue
		}
		if source != "" && common.AsString(item.Metadata["source"]) != source {
			continue
		}
		if start != nil && item.CreatedAt.Before(*start) {
			continue
		}
		if end != nil && item.CreatedAt.After(*end) {
			continue
		}
		filtered = append(filtered, item)
	}
	pageItems, pagination := db.Paginate(filtered, page, pageSize)
	common.WriteJSON(w, http.StatusOK, map[string]any{"pagination": pagination, "items": pageItems})
}

func (s *HTTPServer) llmCallQuery(w http.ResponseWriter, r *http.Request) {
	if !s.requireMethod(w, r, http.MethodGet) {
		return
	}
	page, pageSize := common.ParsePage(r)
	data := s.store.Snapshot()
	items := db.NewestFirst(data.LlmCalls, func(a, b db.LlmCallItem) bool { return a.CreatedAt.After(b.CreatedAt) })
	provider, model, status := common.QueryString(r, "provider"), common.QueryString(r, "model"), common.QueryString(r, "status")
	filtered := items[:0]
	for _, item := range items {
		if provider != "" && item.Provider != provider {
			continue
		}
		if model != "" && item.Model != model {
			continue
		}
		if status != "" && item.Status != status {
			continue
		}
		filtered = append(filtered, item)
	}
	pageItems, pagination := db.Paginate(filtered, page, pageSize)
	common.WriteJSON(w, http.StatusOK, map[string]any{"pagination": pagination, "items": pageItems})
}

func (s *HTTPServer) napcatEventQuery(w http.ResponseWriter, r *http.Request) {
	if !s.requireMethod(w, r, http.MethodGet) {
		return
	}
	page, pageSize := common.ParsePage(r)
	data := s.store.Snapshot()
	items := db.NewestFirst(data.NapcatEvents, func(a, b db.NapcatEventItem) bool { return a.CreatedAt.After(b.CreatedAt) })
	postType, messageType, userID := common.QueryString(r, "postType"), common.QueryString(r, "messageType"), common.QueryString(r, "userId")
	start, end := common.ParseTimeQuery(r, "startAt"), common.ParseTimeQuery(r, "endAt")
	filtered := items[:0]
	for _, item := range items {
		if postType != "" && item.PostType != postType {
			continue
		}
		if messageType != "" && (item.MessageType == nil || *item.MessageType != messageType) {
			continue
		}
		if userID != "" && (item.UserID == nil || *item.UserID != userID) {
			continue
		}
		if start != nil && item.CreatedAt.Before(*start) {
			continue
		}
		if end != nil && item.CreatedAt.After(*end) {
			continue
		}
		filtered = append(filtered, item)
	}
	pageItems, pagination := db.Paginate(filtered, page, pageSize)
	common.WriteJSON(w, http.StatusOK, map[string]any{"pagination": pagination, "items": pageItems})
}

func (s *HTTPServer) napcatMessageQuery(w http.ResponseWriter, r *http.Request) {
	if !s.requireMethod(w, r, http.MethodGet) {
		return
	}
	page, pageSize := common.ParsePage(r)
	data := s.store.Snapshot()
	items := db.NewestFirst(data.NapcatMessages, func(a, b db.NapcatMessageItem) bool { return a.CreatedAt.After(b.CreatedAt) })
	messageType, groupID, userID, nickname, keyword := common.QueryString(r, "messageType"), common.QueryString(r, "groupId"), common.QueryString(r, "userId"), common.QueryString(r, "nickname"), common.QueryString(r, "keyword")
	start, end := common.ParseTimeQuery(r, "startAt"), common.ParseTimeQuery(r, "endAt")
	filtered := items[:0]
	for _, item := range items {
		if messageType != "" && item.MessageType != messageType {
			continue
		}
		if groupID != "" && (item.GroupID == nil || *item.GroupID != groupID) {
			continue
		}
		if userID != "" && (item.UserID == nil || *item.UserID != userID) {
			continue
		}
		if nickname != "" && (item.Nickname == nil || !common.ContainsFold(*item.Nickname, nickname)) {
			continue
		}
		if keyword != "" && !common.ContainsFold(common.AsString(item.Message), keyword) && !common.ContainsFold(common.AsString(item.Payload["raw_message"]), keyword) {
			continue
		}
		if start != nil && item.CreatedAt.Before(*start) {
			continue
		}
		if end != nil && item.CreatedAt.After(*end) {
			continue
		}
		filtered = append(filtered, item)
	}
	pageItems, pagination := db.Paginate(filtered, page, pageSize)
	common.WriteJSON(w, http.StatusOK, map[string]any{"pagination": pagination, "items": pageItems})
}

func (s *HTTPServer) storyQuery(w http.ResponseWriter, r *http.Request) {
	if !s.requireMethod(w, r, http.MethodGet) {
		return
	}
	page, pageSize := common.ParsePage(r)
	query := common.QueryString(r, "query")
	data := s.store.Snapshot()
	items := db.NewestFirst(data.Stories, func(a, b db.StoryItem) bool { return a.UpdatedAt.After(b.UpdatedAt) })
	filtered := items[:0]
	for _, item := range items {
		if query != "" && !common.ContainsFold(item.Markdown+" "+item.Title+" "+item.Scene+" "+strings.Join(item.People, " "), query) {
			continue
		}
		filtered = append(filtered, item)
	}
	pageItems, pagination := db.Paginate(filtered, page, pageSize)
	common.WriteJSON(w, http.StatusOK, map[string]any{"pagination": pagination, "items": pageItems})
}

func (s *HTTPServer) storyReindex(w http.ResponseWriter, r *http.Request) {
	if !s.requireMethod(w, r, http.MethodPost) {
		return
	}
	var req map[string]any
	_ = common.ReadJSON(r, &req)
	mode := common.AsString(req["mode"])
	if mode == "" {
		mode = "outdated"
	}
	data := s.store.Snapshot()
	common.WriteJSON(w, http.StatusOK, map[string]any{"mode": mode, "totalStories": len(data.Stories), "targetedStories": len(data.Stories), "reindexedStories": 0, "skippedStories": len(data.Stories), "failedStories": 0, "failures": []any{}})
}

func (s *HTTPServer) metricChartList(w http.ResponseWriter, r *http.Request) {
	if !s.requireMethod(w, r, http.MethodGet) {
		return
	}
	common.WriteJSON(w, http.StatusOK, s.charts.List())
}

func (s *HTTPServer) metricChartData(w http.ResponseWriter, r *http.Request) {
	if !s.requireMethod(w, r, http.MethodGet) {
		return
	}
	resp, status := s.charts.Data(common.QueryString(r, "chartName"), common.QueryString(r, "bucket"), common.QueryString(r, "rangePreset"), common.ParseTimeQuery(r, "startAt"), common.ParseTimeQuery(r, "endAt"))
	common.WriteJSON(w, status, resp)
}

func (s *HTTPServer) metricChartCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireMethod(w, r, http.MethodPost) {
		return
	}
	var req map[string]any
	if err := common.ReadJSON(r, &req); err != nil {
		common.WriteJSON(w, http.StatusBadRequest, map[string]any{"message": "请求参数不合法"})
		return
	}
	resp, status := s.charts.Create(req)
	common.WriteJSON(w, status, resp)
}

func (s *HTTPServer) metricChartDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireMethod(w, r, http.MethodPost) {
		return
	}
	var req map[string]any
	if err := common.ReadJSON(r, &req); err != nil {
		common.WriteJSON(w, http.StatusBadRequest, map[string]any{"message": "请求参数不合法"})
		return
	}
	common.WriteJSON(w, http.StatusOK, s.charts.Delete(common.AsString(req["chartName"])))
}
