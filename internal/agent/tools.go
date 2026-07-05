package agent

import (
	"QqBot/internal/agentruntime"
	"QqBot/internal/capabilities/airadar"
	browsercap "QqBot/internal/capabilities/browser"
	"QqBot/internal/capabilities/magnetsearch"
	"QqBot/internal/capabilities/messaging"
	"QqBot/internal/capabilities/news"
	"QqBot/internal/capabilities/personalapp"
	storycap "QqBot/internal/capabilities/story"
	"QqBot/internal/capabilities/terminal"
	"QqBot/internal/capabilities/vision"
	"QqBot/internal/capabilities/websearch"
	"QqBot/internal/config"
	"QqBot/internal/db"
	"QqBot/internal/llm"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func buildBusinessTools(cfg *config.Config, store *db.Store, sender messaging.Sender, terminalService *terminal.Service, llmClient *llm.LLMClient, personal *personalapp.Service) *agentruntime.ToolCatalog {
	indexer := storycap.NewMemoryIndexer(cfg, store)
	recall := storycap.NewVectorRecall(cfg, store)
	storyService := storycap.Service{Repo: storeStoryRepository{store: store, indexer: indexer}, Recall: recall}
	searchService := websearch.URLAwareService{
		Fallback: websearch.TavilyService{APIKey: cfg.Server.Tavily.APIKey},
	}
	aiToneEnabled := cfg.Server.Agent.AITone.EnabledValue()
	var aiToneClassifier *airadar.Classifier
	if aiToneEnabled {
		var err error
		aiToneClassifier, err = airadar.NewDefaultClassifier()
		if err != nil {
			store.Log("error", "AIRadar model load failed", map[string]any{"event": "airadar.model.load_failed", "error": err.Error()})
		}
	}
	catalog := agentruntime.NewToolCatalog(
		sendMessageTool{sender: sender, store: store, screenshotDir: cfg.Server.Browser.ScreenshotDir, aiToneClassifier: aiToneClassifier, aiToneThreshold: cfg.Server.Agent.AITone.Threshold, aiToneDisabled: !aiToneEnabled},
		analyzeImageTool{vision: vision.Agent{Client: llmClient}, requester: requesterFromSender(sender), screenshotDir: cfg.Server.Browser.ScreenshotDir},
		airadar.DetectTool{Classifier: aiToneClassifier},
		news.OpenIthomeArticleTool{Store: storeNewsStore{store: store}},
		storycap.SearchMemoryTool{Service: storyService, TopK: cfg.Server.Agent.Story.Memory.Retrieval.TopK},
		&WebSearchTaskAgentTool{service: searchService},
		personalapp.ScreenTool{Service: personal},
		personalapp.TodoTool{Service: personal},
		personalapp.NovelTool{Service: personal},
		personalapp.ProjectTool{Service: personal},
		personalapp.MusicTool{Service: personal},
		personalapp.NewsTool{Service: personal},
		personalapp.ActivityTool{Service: personal},
		personalapp.WorkspaceTool{Service: personal},
		calculateTool{},
	)
	if cfg.Server.Browser.Enabled {
		browserClient, err := browsercap.NewClient(browsercap.Config{
			BaseURL:        cfg.Server.Browser.BaseURL,
			AuthToken:      cfg.Server.Browser.AuthToken,
			Timeout:        time.Duration(cfg.Server.Browser.TimeoutMs) * time.Millisecond,
			MaxResultChars: cfg.Server.Browser.MaxResultChars,
		})
		if err != nil {
			store.Log("error", "Browser client configuration rejected", map[string]any{"event": "browser.config.invalid", "error": err.Error()})
		} else {
			catalog.Add(NewBrowserTaskAgentTool(
				browserClient,
				cfg.Server.Browser.DefaultSessionID,
				cfg.Server.Browser.MaxTaskRounds,
				llmClient,
				cfg.Server.Browser.ScreenshotMaxBytes,
				cfg.Server.Browser.ScreenshotDir,
			))
		}
	}
	if cfg.Server.MagnetSearch.Enabled {
		timeout := time.Duration(cfg.Server.MagnetSearch.TimeoutMs) * time.Millisecond
		if timeout <= 0 {
			timeout = 15 * time.Second
		}
		magnetService := magnetsearch.NewDefaultService(
			&http.Client{Timeout: timeout},
			cfg.Server.MagnetSearch.TokyoLibBaseURL,
		)
		catalog.Add(magnetsearch.SearchTool{
			Service:      magnetService,
			DefaultLimit: cfg.Server.MagnetSearch.DefaultLimit,
		})
	}
	if terminalService != nil {
		catalog.Add(terminal.BashTool{Service: terminalService})
		catalog.Add(terminal.ReadBashOutputTool{Service: terminalService})
	}
	return catalog
}

func requesterFromSender(sender messaging.Sender) napcatRequester {
	requester, _ := sender.(napcatRequester)
	return requester
}

type calculateTool struct{}

func (calculateTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "calculate", Description: "对两个有限实数做一次二元四则运算（+、-、*、/）。", Parameters: agentruntime.ObjectSchema(map[string]any{
		"a":  map[string]any{"type": "number", "description": "左操作数。"},
		"op": map[string]any{"type": "string", "description": `运算符。可选值: "+"、"-"、"*"、"/"。`},
		"b":  map[string]any{"type": "number", "description": "右操作数。"},
	})}
}

func (calculateTool) Kind() string { return "business" }

func (calculateTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	a, okA := numberArg(call.Arguments["a"])
	b, okB := numberArg(call.Arguments["b"])
	op, _ := call.Arguments["op"].(string)
	if !okA || !okB || math.IsNaN(a) || math.IsNaN(b) || math.IsInf(a, 0) || math.IsInf(b, 0) {
		return jsonToolResult(map[string]any{"ok": false, "error": "INVALID_ARGUMENTS", "message": "a 和 b 必须是有限数字。"}), nil
	}
	switch op {
	case "+":
		return jsonToolResult(map[string]any{"ok": true, "result": a + b}), nil
	case "-":
		return jsonToolResult(map[string]any{"ok": true, "result": a - b}), nil
	case "*":
		return jsonToolResult(map[string]any{"ok": true, "result": a * b}), nil
	case "/":
		if b == 0 {
			return jsonToolResult(map[string]any{"ok": false, "error": "DIVISION_BY_ZERO", "message": "除数不能是 0。"}), nil
		}
		return jsonToolResult(map[string]any{"ok": true, "result": a / b}), nil
	default:
		return jsonToolResult(map[string]any{"ok": false, "error": "INVALID_ARGUMENTS", "message": "op 必须是 +、-、*、/ 之一。"}), nil
	}
}

func numberArg(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		n, err := v.Float64()
		return n, err == nil
	default:
		return 0, false
	}
}

func jsonToolResult(value map[string]any) agentruntime.ToolResult {
	data, _ := json.Marshal(value)
	return agentruntime.ToolResult{Kind: "business", Content: string(data)}
}

type sendMessageTool struct {
	sender           messaging.Sender
	store            *db.Store
	screenshotDir    string
	aiToneClassifier *airadar.Classifier
	aiToneThreshold  float64
	aiToneDisabled   bool
}

func (t sendMessageTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "send_message", Description: "向指定群聊或私聊发送消息；省略目标时回复最新一条 QQ 消息所在会话。", Parameters: agentruntime.ObjectSchema(map[string]any{
		"targetType": map[string]any{"type": "string", "enum": []string{"group", "private"}, "description": "回复路由类型，对应 qq_message 的 target_type。"},
		"targetId":   map[string]any{"type": "string", "description": "回复路由 ID，对应 qq_message 的 target_id。"},
		"message":    map[string]any{"type": "string", "description": "要发送的消息内容。"},
		"imagePath":  map[string]any{"type": "string", "description": "可选浏览器截图路径，只允许 browser_screenshot 返回的受控截图文件。"},
	})}
}

func (t sendMessageTool) Kind() string { return "business" }

func (t sendMessageTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	if t.sender == nil {
		return agentruntime.ToolResult{}, fmt.Errorf("消息发送器不可用")
	}
	targetType, _ := call.Arguments["targetType"].(string)
	targetType = strings.TrimSpace(targetType)
	if targetType == "" {
		groupType, _ := call.Arguments["groupType"].(string)
		targetType = strings.TrimSpace(groupType)
	}
	targetID, _ := call.Arguments["targetId"].(string)
	targetID = strings.TrimSpace(targetID)
	message, _ := call.Arguments["message"].(string)
	imagePath, _ := call.Arguments["imagePath"].(string)
	if strings.TrimSpace(imagePath) != "" {
		safePath, err := allowedScreenshotPath(t.screenshotDir, imagePath)
		if err != nil {
			return jsonToolResult(map[string]any{"ok": false, "error": "IMAGE_PATH_NOT_ALLOWED", "message": err.Error()}), nil
		}
		message += cqImageFile(safePath)
	}
	if strings.TrimSpace(message) == "" {
		return jsonToolResult(map[string]any{"ok": false, "error": "EMPTY_MESSAGE", "message": "message 和 imagePath 至少需要一个。"}), nil
	}
	if block := t.blockHighAITone(message); block != nil {
		return *block, nil
	}
	if targetID == "" {
		return jsonToolResult(map[string]any{"ok": false, "error": "MESSAGE_TARGET_REQUIRED", "message": "缺少 targetType/targetId；请使用 qq_message 标签中的回复路由。"}), nil
	}
	if targetType == "" {
		return jsonToolResult(map[string]any{"ok": false, "error": "MESSAGE_TARGET_REQUIRED", "message": "缺少 targetType/targetId；请使用 qq_message 标签中的回复路由。"}), nil
	}
	var id int
	var err error
	if targetType == "private" {
		id, err = t.sender.SendPrivateMessage(targetID, message)
	} else {
		id, err = t.sender.SendGroupMessage(targetID, message)
	}
	data, _ := json.Marshal(map[string]any{"messageId": id})
	return agentruntime.ToolResult{Kind: "business", Content: string(data)}, err
}

func (t sendMessageTool) blockHighAITone(message string) *agentruntime.ToolResult {
	if t.aiToneDisabled {
		return nil
	}
	text := strings.TrimSpace(stripCQSegments(message))
	if text == "" {
		return nil
	}
	classifier := t.aiToneClassifier
	if classifier == nil {
		var err error
		classifier, err = airadar.NewDefaultClassifier()
		if err != nil {
			result := jsonToolResult(map[string]any{"ok": false, "error": "AI_TONE_MODEL_UNAVAILABLE", "message": "AIRadar 模型加载失败：" + err.Error()})
			return &result
		}
	}
	threshold := t.aiToneThreshold
	if threshold <= 0 {
		threshold = 0.65
	}
	result := classifier.Predict(text, threshold)
	t.logAIToneCheck(text, result.Prob, threshold, result.Prob > threshold)
	if result.Prob <= threshold {
		return nil
	}
	blocked := jsonToolResult(map[string]any{
		"ok":        false,
		"error":     "AI_TONE_TOO_HIGH",
		"prob":      roundFloat(result.Prob, 6),
		"label":     result.Label,
		"threshold": threshold,
		"message":   "这条 send_message 已被 AIRadar 拦截：AI 腔调概率超过阈值。请不要原样发送，改成更短、更具体的接梗、追问或吐槽；改不出来就 wait。",
	})
	return &blocked
}

func (t sendMessageTool) logAIToneCheck(text string, prob, threshold float64, blocked bool) {
	if t.store == nil {
		return
	}
	t.store.Log("info", "Send message AI tone checked", map[string]any{
		"event":     "agent.send_message.ai_tone_checked",
		"prob":      roundFloat(prob, 6),
		"threshold": threshold,
		"blocked":   blocked,
		"message":   trimRunes(text, 240),
	})
}

func trimRunes(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}

func stripCQSegments(text string) string {
	for {
		start := strings.Index(text, "[CQ:")
		if start < 0 {
			return text
		}
		end := strings.Index(text[start:], "]")
		if end < 0 {
			return text[:start]
		}
		text = text[:start] + text[start+end+1:]
	}
}

func roundFloat(value float64, digits int) float64 {
	scale := math.Pow10(digits)
	return math.Round(value*scale) / scale
}

func allowedScreenshotPath(root, candidate string) (string, error) {
	if strings.TrimSpace(root) == "" {
		root = "data/browser-screenshots"
	}
	rootPath, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	candidatePath, err := filepath.Abs(strings.TrimSpace(candidate))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(candidatePath)
	if err != nil {
		return "", fmt.Errorf("截图文件不可用：%w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("截图路径不能是目录")
	}
	linkInfo, err := os.Lstat(candidatePath)
	if err != nil {
		return "", fmt.Errorf("截图文件不可用：%w", err)
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("截图文件不能是符号链接")
	}
	relative, err := filepath.Rel(rootPath, candidatePath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("图片必须位于浏览器截图目录 %s", rootPath)
	}
	if resolvedRoot, rootErr := filepath.EvalSymlinks(rootPath); rootErr == nil {
		if resolvedCandidate, candidateErr := filepath.EvalSymlinks(candidatePath); candidateErr == nil {
			resolvedRelative, relErr := filepath.Rel(resolvedRoot, resolvedCandidate)
			if relErr != nil || resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("图片必须位于浏览器截图目录 %s", rootPath)
			}
		}
	}
	switch strings.ToLower(filepath.Ext(candidatePath)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
	default:
		return "", fmt.Errorf("不支持的图片格式")
	}
	return candidatePath, nil
}

func cqImageFile(path string) string {
	slashPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	fileURL := (&url.URL{Scheme: "file", Path: slashPath}).String()
	return "[CQ:image,file=" + fileURL + "]"
}

func buildStoryTools(cfg *config.Config, store *db.Store) *agentruntime.ToolCatalog {
	storyService := storycap.Service{Repo: storeStoryRepository{store: store, indexer: storycap.NewMemoryIndexer(cfg, store)}}
	return agentruntime.NewToolCatalog(
		storycap.CreateStoryTool{Service: storyService},
		storycap.RewriteStoryTool{Service: storyService},
		storycap.FinishStoryBatchTool{},
	)
}

func firstString(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return items[0]
}

type storeStoryRepository struct {
	store   *db.Store
	indexer *storycap.MemoryIndexer
}

func (r storeStoryRepository) Save(ctx context.Context, story storycap.Story) error {
	item := db.StoryItem{
		ID:                    story.ID,
		Markdown:              story.Markdown,
		Title:                 story.Title,
		Time:                  story.Time,
		Scene:                 story.Scene,
		People:                story.People,
		Impact:                story.Impact,
		SourceMessageSeqStart: story.SourceMessageSeqStart,
		SourceMessageSeqEnd:   story.SourceMessageSeqEnd,
		CreatedAt:             story.CreatedAt,
		UpdatedAt:             story.UpdatedAt,
		Score:                 story.Score,
		MatchedKinds:          story.MatchedKinds,
	}
	if item.Title == "" {
		item.Title = storycap.ExtractTitle(item.Markdown)
	}
	r.store.AddStory(item)
	if r.indexer != nil {
		_ = r.indexer.ReindexStory(ctx, item)
	}
	return nil
}

func (r storeStoryRepository) List(context.Context) ([]storycap.Story, error) {
	items := r.store.Snapshot().Stories
	out := make([]storycap.Story, 0, len(items))
	for _, item := range items {
		out = append(out, storycap.Story{
			ID:                    item.ID,
			Markdown:              item.Markdown,
			Title:                 item.Title,
			Time:                  item.Time,
			Scene:                 item.Scene,
			People:                item.People,
			Impact:                item.Impact,
			SourceMessageSeqStart: item.SourceMessageSeqStart,
			SourceMessageSeqEnd:   item.SourceMessageSeqEnd,
			CreatedAt:             item.CreatedAt,
			UpdatedAt:             item.UpdatedAt,
			Score:                 item.Score,
			MatchedKinds:          item.MatchedKinds,
		})
	}
	return out, nil
}

func (r storeStoryRepository) Delete(_ context.Context, id string) error {
	r.store.DeleteStory(id)
	return nil
}

type storeNewsStore struct {
	store *db.Store
}

func (s storeNewsStore) FindArticle(id int) (news.Article, bool) {
	for _, article := range s.store.Snapshot().NewsArticles {
		if article.ID == id {
			content := article.Content
			if content == "" {
				content = article.RSSSummary
			}
			return news.Article{ID: article.ID, Title: article.Title, URL: article.URL, Content: content}, true
		}
	}
	return news.Article{}, false
}
