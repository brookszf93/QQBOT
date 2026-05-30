package db

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"qqbot-ai/internal/common"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Store 是使用的本地 SQLite 持久化层。
type Store struct {
	mu   sync.Mutex
	path string
	db   *sql.DB
}

// StoreData 是管理台和现有上层代码使用的兼容快照结构。
type StoreData struct {
	AppLogs         []AppLogItem               `json:"appLogs"`
	LlmCalls        []LlmCallItem              `json:"llmCalls"`
	NapcatEvents    []NapcatEventItem          `json:"napcatEvents"`
	NapcatMessages  []NapcatMessageItem        `json:"napcatMessages"`
	StoryLedger     []StoryLedgerItem          `json:"storyLedger"`
	Stories         []StoryItem                `json:"stories"`
	StoryDocuments  []StoryMemoryDocument      `json:"storyMemoryDocuments"`
	EmbeddingCache  []EmbeddingCacheItem       `json:"embeddingCache"`
	Metrics         []MetricItem               `json:"metrics"`
	MetricCharts    []MetricChart              `json:"metricCharts"`
	NewsArticles    []NewsArticle              `json:"newsArticles"`
	NewsFeedCursors []NewsFeedCursor           `json:"newsFeedCursors"`
	AgentSnapshots  map[string]json.RawMessage `json:"agentSnapshots"`
}

const (
	maxStoredAppLogs        = 2000
	maxStoredLlmCalls       = 1000
	maxStoredNapcatEvents   = 1000
	maxStoredNapcatMessages = 10000
	maxStoredMetrics        = 10000
	maxStoredLlmPayload     = 12000
	maxStoredLlmPreview     = 4000
)

// AppLogItem 对应 TS 的 app_log 读取模型。
type AppLogItem struct {
	ID        int            `json:"id"`
	TraceID   string         `json:"traceId"`
	Level     string         `json:"level"`
	Message   string         `json:"message"`
	Metadata  map[string]any `json:"metadata"`
	CreatedAt time.Time      `json:"createdAt"`
}

// LlmCallItem 对应 TS 的 llm_chat_call 读取模型。
type LlmCallItem struct {
	ID                    int            `json:"id"`
	RequestID             string         `json:"requestId"`
	Seq                   int            `json:"seq"`
	Provider              string         `json:"provider"`
	Model                 string         `json:"model"`
	Extension             map[string]any `json:"extension"`
	Status                string         `json:"status"`
	RequestPayload        map[string]any `json:"requestPayload"`
	ResponsePayload       map[string]any `json:"responsePayload"`
	NativeRequestPayload  map[string]any `json:"nativeRequestPayload"`
	NativeResponsePayload map[string]any `json:"nativeResponsePayload"`
	Error                 map[string]any `json:"error"`
	NativeError           map[string]any `json:"nativeError"`
	LatencyMs             *int           `json:"latencyMs"`
	CreatedAt             time.Time      `json:"createdAt"`
}

// NapcatEventItem 对应持久化的 NapCat 原始 post-type 事件。
type NapcatEventItem struct {
	ID          int            `json:"id"`
	PostType    string         `json:"postType"`
	MessageType *string        `json:"messageType"`
	SubType     *string        `json:"subType"`
	UserID      *string        `json:"userId"`
	GroupID     *string        `json:"groupId"`
	EventTime   *time.Time     `json:"eventTime"`
	Payload     map[string]any `json:"payload"`
	CreatedAt   time.Time      `json:"createdAt"`
}

// NapcatMessageItem 对应标准化后的 QQ 群聊/私聊消息。
type NapcatMessageItem struct {
	ID          int            `json:"id"`
	MessageType string         `json:"messageType"`
	SubType     string         `json:"subType"`
	GroupID     *string        `json:"groupId"`
	UserID      *string        `json:"userId"`
	Nickname    *string        `json:"nickname"`
	MessageID   *int           `json:"messageId"`
	Message     any            `json:"message"`
	EventTime   *time.Time     `json:"eventTime"`
	Payload     map[string]any `json:"payload"`
	CreatedAt   time.Time      `json:"createdAt"`
}

// StoryLedgerItem 保存 Root 运行时写给 Story Agent 的线性消息账本。
type StoryLedgerItem struct {
	Seq        int       `json:"seq"`
	RuntimeKey string    `json:"runtimeKey"`
	Role       string    `json:"role"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"createdAt"`
}

// StoryItem 是 Story 记忆在根包中的 JSON 表示。
type StoryItem struct {
	ID                    string    `json:"id"`
	Markdown              string    `json:"markdown"`
	Title                 string    `json:"title"`
	Time                  string    `json:"time"`
	Scene                 string    `json:"scene"`
	People                []string  `json:"people"`
	Impact                string    `json:"impact"`
	SourceMessageSeqStart int       `json:"sourceMessageSeqStart"`
	SourceMessageSeqEnd   int       `json:"sourceMessageSeqEnd"`
	CreatedAt             time.Time `json:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt"`
	Score                 *float64  `json:"score"`
	MatchedKinds          []string  `json:"matchedKinds"`
}

// StoryMemoryDocument 是 Story 面向 RAG/召回的向量化投影。
type StoryMemoryDocument struct {
	ID             int       `json:"id"`
	StoryID        string    `json:"storyId"`
	Kind           string    `json:"kind"`
	Content        string    `json:"content"`
	EmbeddingModel string    `json:"embeddingModel"`
	EmbeddingDim   int       `json:"embeddingDim"`
	Embedding      []float64 `json:"embedding"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// EmbeddingCacheItem 缓存 embedding 请求结果，避免重复调用供应商。
type EmbeddingCacheItem struct {
	ID                   int       `json:"id"`
	Provider             string    `json:"provider"`
	Model                string    `json:"model"`
	TaskType             string    `json:"taskType"`
	OutputDimensionality int       `json:"outputDimensionality"`
	TextHash             string    `json:"textHash"`
	Text                 string    `json:"text"`
	Embedding            []float64 `json:"embedding"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

// MetricItem 保存一条带字符串标签的数值观测。
type MetricItem struct {
	ID         int               `json:"id"`
	MetricName string            `json:"metricName"`
	Value      float64           `json:"value"`
	Tags       map[string]string `json:"tags"`
	OccurredAt time.Time         `json:"occurredAt"`
	CreatedAt  time.Time         `json:"createdAt"`
}

// MetricChart 定义指标观测应如何聚合成图表。
type MetricChart struct {
	ChartName  string            `json:"chartName"`
	MetricName string            `json:"metricName"`
	Aggregator string            `json:"aggregator"`
	TagFilters map[string]string `json:"tagFilters"`
	GroupByTag string            `json:"groupByTag"`
	CreatedAt  time.Time         `json:"createdAt"`
	UpdatedAt  time.Time         `json:"updatedAt"`
}

// NewsArticle 保存一篇已入库的 IThome 文章。
type NewsArticle struct {
	ID          int       `json:"id"`
	SourceKey   string    `json:"sourceKey"`
	UpstreamID  string    `json:"upstreamId"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"publishedAt"`
	RSSSummary  string    `json:"rssSummary"`
	Content     string    `json:"articleContent"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// NewsFeedCursor 记录某个新闻源进入后推进到的最新游标。
type NewsFeedCursor struct {
	SourceKey           string    `json:"sourceKey"`
	LastSeenArticleID   int       `json:"lastSeenArticleId"`
	LastSeenPublishedAt time.Time `json:"lastSeenPublishedAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

// OpenStore 加载或创建 SQLite 持久化文件。
func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &Store{path: path, db: db}
	if err := store.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.maintainStorage(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) initSchema() error {
	stmts := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS app_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, item TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS llm_calls (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, item TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS napcat_events (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, item TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS napcat_messages (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, item TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS story_ledger (seq INTEGER PRIMARY KEY AUTOINCREMENT, runtime_key TEXT NOT NULL, created_at TEXT NOT NULL, item TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_story_ledger_runtime_seq ON story_ledger(runtime_key, seq)`,
		`CREATE TABLE IF NOT EXISTS stories (id TEXT PRIMARY KEY, updated_at TEXT NOT NULL, item TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS story_documents (id INTEGER PRIMARY KEY AUTOINCREMENT, story_id TEXT NOT NULL, item TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_story_documents_story_id ON story_documents(story_id)`,
		`CREATE TABLE IF NOT EXISTS embedding_cache (id INTEGER PRIMARY KEY AUTOINCREMENT, provider TEXT NOT NULL, model TEXT NOT NULL, task_type TEXT NOT NULL, output_dimensionality INTEGER NOT NULL, text_hash TEXT NOT NULL, item TEXT NOT NULL, UNIQUE(provider, model, task_type, output_dimensionality, text_hash))`,
		`CREATE TABLE IF NOT EXISTS metrics (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, item TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS metric_charts (chart_name TEXT PRIMARY KEY, item TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS news_articles (id INTEGER PRIMARY KEY AUTOINCREMENT, source_key TEXT NOT NULL, upstream_id TEXT NOT NULL, item TEXT NOT NULL, UNIQUE(source_key, upstream_id))`,
		`CREATE TABLE IF NOT EXISTS news_feed_cursors (source_key TEXT PRIMARY KEY, item TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS agent_snapshots (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) maintainStorage() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	compacted, err := s.compactOversizedLlmCallsLocked()
	if err != nil {
		return err
	}
	s.pruneTable("llm_calls", maxStoredLlmCalls)
	s.pruneTable("app_logs", maxStoredAppLogs)
	s.pruneTable("napcat_events", maxStoredNapcatEvents)
	s.pruneTable("napcat_messages", maxStoredNapcatMessages)
	s.pruneTable("metrics", maxStoredMetrics)
	if compacted > 0 {
		if _, err := s.db.Exec(`VACUUM`); err != nil {
			return err
		}
	}
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return nil
}

func (s *Store) compactOversizedLlmCallsLocked() (int, error) {
	rows, err := s.db.Query(`SELECT id, item FROM llm_calls WHERE length(item) > ?`, maxStoredLlmPayload)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type update struct {
		id  int
		raw string
	}
	updates := []update{}
	for rows.Next() {
		var id int
		var raw string
		if rows.Scan(&id, &raw) != nil {
			continue
		}
		var item LlmCallItem
		if json.Unmarshal([]byte(raw), &item) != nil {
			continue
		}
		item = compactLlmCall(item)
		updates = append(updates, update{id: id, raw: mustJSON(item)})
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, item := range updates {
		if _, err := s.db.Exec(`UPDATE llm_calls SET item = ? WHERE id = ?`, item.raw, item.id); err != nil {
			return len(updates), err
		}
	}
	return len(updates), nil
}

// SaveAgentSnapshot 保存 root/story 运行时快照，等价 TS 的 runtime snapshot 表。
func (s *Store) SaveAgentSnapshot(key string, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	s.mu.Lock()
	_, _ = s.db.Exec(`INSERT OR REPLACE INTO agent_snapshots(key, value, updated_at) VALUES(?, ?, ?)`, key, string(data), formatTime(time.Now()))
	s.mu.Unlock()
}

// LoadAgentSnapshot 读取指定运行时快照。
func (s *Store) LoadAgentSnapshot(key string, out any) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	var data string
	err := s.db.QueryRow(`SELECT value FROM agent_snapshots WHERE key = ?`, key).Scan(&data)
	return err == nil && json.Unmarshal([]byte(data), out) == nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Flush() error {
	return nil
}

func (s *Store) Log(level, message string, metadata map[string]any) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	item := AppLogItem{TraceID: common.NewID(), Level: level, Message: message, Metadata: metadata, CreatedAt: time.Now()}
	s.mu.Lock()
	id, _ := s.insertJSONAutoID("app_logs", item.CreatedAt, &item)
	s.pruneTable("app_logs", maxStoredAppLogs)
	s.mu.Unlock()
	item.ID = id
	common.LogLine(level, message, metadata)
}

func (s *Store) AddLlmCall(item LlmCallItem) {
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	item = compactLlmCall(item)
	s.mu.Lock()
	_, _ = s.insertJSONAutoID("llm_calls", item.CreatedAt, &item)
	s.pruneTable("llm_calls", maxStoredLlmCalls)
	s.mu.Unlock()
}

func (s *Store) AddNapcatEvent(item NapcatEventItem) {
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	s.mu.Lock()
	_, _ = s.insertJSONAutoID("napcat_events", item.CreatedAt, &item)
	s.pruneTable("napcat_events", maxStoredNapcatEvents)
	s.mu.Unlock()
}

func (s *Store) AddNapcatMessage(item NapcatMessageItem) int {
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	s.mu.Lock()
	id, _ := s.insertJSONAutoID("napcat_messages", item.CreatedAt, &item)
	s.pruneTable("napcat_messages", maxStoredNapcatMessages)
	s.mu.Unlock()
	return id
}

func (s *Store) insertJSONAutoID(table string, createdAt time.Time, item any) (int, error) {
	raw := mustJSON(item)
	result, err := s.db.Exec(`INSERT INTO `+table+`(created_at, item) VALUES(?, ?)`, formatTime(createdAt), raw)
	if err != nil {
		return 0, err
	}
	id64, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	setItemID(item, int(id64))
	_, err = s.db.Exec(`UPDATE `+table+` SET item = ? WHERE id = ?`, mustJSON(item), id64)
	return int(id64), err
}

func (s *Store) pruneTable(table string, max int) {
	if max <= 0 {
		return
	}
	_, _ = s.db.Exec(`DELETE FROM `+table+` WHERE id NOT IN (SELECT id FROM `+table+` ORDER BY id DESC LIMIT ?)`, max)
}

// AddStoryLedger 追加一条 Story Agent 可按 seq 消费的线性消息。
func (s *Store) AddStoryLedger(runtimeKey, role, content string) int {
	item := StoryLedgerItem{RuntimeKey: runtimeKey, Role: role, Content: content, CreatedAt: time.Now()}
	s.mu.Lock()
	result, err := s.db.Exec(`INSERT INTO story_ledger(runtime_key, created_at, item) VALUES(?, ?, ?)`, runtimeKey, formatTime(item.CreatedAt), mustJSON(item))
	if err == nil {
		if seq, idErr := result.LastInsertId(); idErr == nil {
			item.Seq = int(seq)
			_, _ = s.db.Exec(`UPDATE story_ledger SET item = ? WHERE seq = ?`, mustJSON(item), item.Seq)
		}
	}
	s.mu.Unlock()
	return item.Seq
}

func (s *Store) CountStoryLedgerAfter(runtimeKey string, afterSeq int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM story_ledger WHERE runtime_key = ? AND seq > ?`, runtimeKey, afterSeq).Scan(&count)
	return count
}

func (s *Store) LatestStoryLedger(runtimeKey string) (StoryLedgerItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	err := s.db.QueryRow(`SELECT item FROM story_ledger WHERE runtime_key = ? ORDER BY seq DESC LIMIT 1`, runtimeKey).Scan(&raw)
	var item StoryLedgerItem
	return item, err == nil && json.Unmarshal([]byte(raw), &item) == nil
}

func (s *Store) ListStoryLedgerAfter(runtimeKey string, afterSeq, limit int) []StoryLedgerItem {
	query := `SELECT item FROM story_ledger WHERE runtime_key = ? AND seq > ? ORDER BY seq ASC`
	args := []any{runtimeKey, afterSeq}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return queryJSONRows[StoryLedgerItem](s.db, query, args...)
}

func (s *Store) AddMetric(name string, value float64, tags map[string]string) {
	now := time.Now()
	item := MetricItem{MetricName: name, Value: value, Tags: tags, OccurredAt: now, CreatedAt: now}
	s.mu.Lock()
	_, _ = s.insertJSONAutoID("metrics", item.CreatedAt, &item)
	s.pruneTable("metrics", maxStoredMetrics)
	s.mu.Unlock()
}

// AddStory 追加或替换一条 Story 记忆。
func (s *Store) AddStory(item StoryItem) {
	s.mu.Lock()
	_, _ = s.db.Exec(`INSERT OR REPLACE INTO stories(id, updated_at, item) VALUES(?, ?, ?)`, item.ID, formatTime(item.UpdatedAt), mustJSON(item))
	s.mu.Unlock()
}

// DeleteStory 删除指定 Story 记忆。
func (s *Store) DeleteStory(id string) {
	s.mu.Lock()
	_, _ = s.db.Exec(`DELETE FROM stories WHERE id = ?`, id)
	_, _ = s.db.Exec(`DELETE FROM story_documents WHERE story_id = ?`, id)
	s.mu.Unlock()
}

func (s *Store) ReplaceStoryMemoryDocuments(storyID string, docs []StoryMemoryDocument) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	_, _ = tx.Exec(`DELETE FROM story_documents WHERE story_id = ?`, storyID)
	now := time.Now()
	for i := range docs {
		docs[i].ID = 0
		docs[i].StoryID = storyID
		docs[i].CreatedAt = now
		docs[i].UpdatedAt = now
		result, err := tx.Exec(`INSERT INTO story_documents(story_id, item) VALUES(?, ?)`, storyID, mustJSON(docs[i]))
		if err != nil {
			return
		}
		if id, err := result.LastInsertId(); err == nil {
			docs[i].ID = int(id)
			_, _ = tx.Exec(`UPDATE story_documents SET item = ? WHERE id = ?`, mustJSON(docs[i]), id)
		}
	}
	_ = tx.Commit()
}

func (s *Store) FindEmbedding(key EmbeddingCacheKey) ([]float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	err := s.db.QueryRow(`SELECT item FROM embedding_cache WHERE provider = ? AND model = ? AND task_type = ? AND output_dimensionality = ? AND text_hash = ?`, key.Provider, key.Model, key.TaskType, key.OutputDimensionality, key.TextHash).Scan(&raw)
	if err != nil {
		return nil, false
	}
	var item EmbeddingCacheItem
	if json.Unmarshal([]byte(raw), &item) != nil {
		return nil, false
	}
	return append([]float64(nil), item.Embedding...), true
}

func (s *Store) SaveEmbedding(key EmbeddingCacheKey, text string, values []float64) {
	now := time.Now()
	item := EmbeddingCacheItem{
		Provider:             key.Provider,
		Model:                key.Model,
		TaskType:             key.TaskType,
		OutputDimensionality: key.OutputDimensionality,
		TextHash:             key.TextHash,
		Text:                 text,
		Embedding:            append([]float64(nil), values...),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	s.mu.Lock()
	var id int
	var raw string
	if err := s.db.QueryRow(`SELECT id, item FROM embedding_cache WHERE provider = ? AND model = ? AND task_type = ? AND output_dimensionality = ? AND text_hash = ?`, key.Provider, key.Model, key.TaskType, key.OutputDimensionality, key.TextHash).Scan(&id, &raw); err == nil {
		var existing EmbeddingCacheItem
		_ = json.Unmarshal([]byte(raw), &existing)
		item.ID = id
		item.CreatedAt = existing.CreatedAt
		_, _ = s.db.Exec(`UPDATE embedding_cache SET item = ? WHERE id = ?`, mustJSON(item), id)
	} else {
		result, err := s.db.Exec(`INSERT INTO embedding_cache(provider, model, task_type, output_dimensionality, text_hash, item) VALUES(?, ?, ?, ?, ?, ?)`, item.Provider, item.Model, item.TaskType, item.OutputDimensionality, item.TextHash, mustJSON(item))
		if err == nil {
			if id64, idErr := result.LastInsertId(); idErr == nil {
				item.ID = int(id64)
				_, _ = s.db.Exec(`UPDATE embedding_cache SET item = ? WHERE id = ?`, mustJSON(item), id64)
			}
		}
	}
	s.mu.Unlock()
}

type EmbeddingCacheKey struct {
	Provider             string
	Model                string
	TaskType             string
	OutputDimensionality int
	TextHash             string
}

// UpsertMetricChart 按图表名称创建或替换指标图表。
func (s *Store) UpsertMetricChart(chart MetricChart) MetricChart {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	if err := s.db.QueryRow(`SELECT item FROM metric_charts WHERE chart_name = ?`, chart.ChartName).Scan(&raw); err == nil {
		var existing MetricChart
		if json.Unmarshal([]byte(raw), &existing) == nil {
			chart.CreatedAt = existing.CreatedAt
		}
	}
	if chart.CreatedAt.IsZero() {
		chart.CreatedAt = time.Now()
	}
	chart.UpdatedAt = time.Now()
	_, _ = s.db.Exec(`INSERT OR REPLACE INTO metric_charts(chart_name, item) VALUES(?, ?)`, chart.ChartName, mustJSON(chart))
	return chart
}

// DeleteMetricChart 按名称删除图表。
func (s *Store) DeleteMetricChart(name string) {
	s.mu.Lock()
	_, _ = s.db.Exec(`DELETE FROM metric_charts WHERE chart_name = ?`, name)
	s.mu.Unlock()
}

// AddNewsArticle 追加一篇已入库新闻文章。
func (s *Store) AddNewsArticle(article NewsArticle) {
	s.mu.Lock()
	result, err := s.db.Exec(`INSERT INTO news_articles(source_key, upstream_id, item) VALUES(?, ?, ?)`, article.SourceKey, article.UpstreamID, mustJSON(article))
	if err == nil {
		if id, idErr := result.LastInsertId(); idErr == nil {
			article.ID = int(id)
			_, _ = s.db.Exec(`UPDATE news_articles SET item = ? WHERE id = ?`, mustJSON(article), id)
		}
	}
	s.mu.Unlock()
}

// UpsertNewsArticle 按 sourceKey/upstreamId 创建或更新新闻文章。
func (s *Store) UpsertNewsArticle(article NewsArticle) (NewsArticle, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var id int
	var raw string
	if err := s.db.QueryRow(`SELECT id, item FROM news_articles WHERE source_key = ? AND upstream_id = ?`, article.SourceKey, article.UpstreamID).Scan(&id, &raw); err == nil {
		var existing NewsArticle
		_ = json.Unmarshal([]byte(raw), &existing)
		article.ID = id
		article.CreatedAt = existing.CreatedAt
		article.UpdatedAt = now
		_, _ = s.db.Exec(`UPDATE news_articles SET item = ? WHERE id = ?`, mustJSON(article), id)
		return article, false
	}
	if article.CreatedAt.IsZero() {
		article.CreatedAt = now
	}
	article.UpdatedAt = now
	result, err := s.db.Exec(`INSERT INTO news_articles(source_key, upstream_id, item) VALUES(?, ?, ?)`, article.SourceKey, article.UpstreamID, mustJSON(article))
	if err == nil {
		if id64, idErr := result.LastInsertId(); idErr == nil {
			article.ID = int(id64)
			_, _ = s.db.Exec(`UPDATE news_articles SET item = ? WHERE id = ?`, mustJSON(article), id64)
		}
	}
	return article, true
}

func (s *Store) FindNewsArticleBySource(sourceKey, upstreamID string) (NewsArticle, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	err := s.db.QueryRow(`SELECT item FROM news_articles WHERE source_key = ? AND upstream_id = ?`, sourceKey, upstreamID).Scan(&raw)
	var article NewsArticle
	return article, err == nil && json.Unmarshal([]byte(raw), &article) == nil
}

func (s *Store) NewsFeedCursor(sourceKey string) (NewsFeedCursor, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	err := s.db.QueryRow(`SELECT item FROM news_feed_cursors WHERE source_key = ?`, sourceKey).Scan(&raw)
	var cursor NewsFeedCursor
	return cursor, err == nil && json.Unmarshal([]byte(raw), &cursor) == nil
}

func (s *Store) UpsertNewsFeedCursor(cursor NewsFeedCursor) {
	cursor.UpdatedAt = time.Now()
	s.mu.Lock()
	_, _ = s.db.Exec(`INSERT OR REPLACE INTO news_feed_cursors(source_key, item) VALUES(?, ?)`, cursor.SourceKey, mustJSON(cursor))
	s.mu.Unlock()
}

// NextID 返回 Store 内唯一的整数 ID。
func (s *Store) NextID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec(`INSERT INTO metrics(created_at, item) VALUES(?, ?)`, formatTime(time.Now()), `{}`)
	if err != nil {
		return 0
	}
	id, _ := result.LastInsertId()
	_, _ = s.db.Exec(`DELETE FROM metrics WHERE id = ?`, id)
	return int(id)
}

func (s *Store) Snapshot() StoreData {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := StoreData{AgentSnapshots: map[string]json.RawMessage{}}
	out.AppLogs = queryJSONRows[AppLogItem](s.db, `SELECT item FROM app_logs ORDER BY id ASC`)
	out.LlmCalls = queryJSONRows[LlmCallItem](s.db, `SELECT item FROM llm_calls ORDER BY id ASC`)
	out.NapcatEvents = queryJSONRows[NapcatEventItem](s.db, `SELECT item FROM napcat_events ORDER BY id ASC`)
	out.NapcatMessages = queryJSONRows[NapcatMessageItem](s.db, `SELECT item FROM napcat_messages ORDER BY id ASC`)
	out.StoryLedger = queryJSONRows[StoryLedgerItem](s.db, `SELECT item FROM story_ledger ORDER BY seq ASC`)
	out.Stories = queryJSONRows[StoryItem](s.db, `SELECT item FROM stories ORDER BY updated_at ASC`)
	out.StoryDocuments = queryJSONRows[StoryMemoryDocument](s.db, `SELECT item FROM story_documents ORDER BY id ASC`)
	out.EmbeddingCache = queryJSONRows[EmbeddingCacheItem](s.db, `SELECT item FROM embedding_cache ORDER BY id ASC`)
	out.Metrics = queryJSONRows[MetricItem](s.db, `SELECT item FROM metrics WHERE item != '{}' ORDER BY id ASC`)
	out.MetricCharts = queryJSONRows[MetricChart](s.db, `SELECT item FROM metric_charts ORDER BY chart_name ASC`)
	out.NewsArticles = queryJSONRows[NewsArticle](s.db, `SELECT item FROM news_articles ORDER BY id ASC`)
	out.NewsFeedCursors = queryJSONRows[NewsFeedCursor](s.db, `SELECT item FROM news_feed_cursors ORDER BY source_key ASC`)
	rows, err := s.db.Query(`SELECT key, value FROM agent_snapshots`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var key, value string
			if rows.Scan(&key, &value) == nil {
				out.AgentSnapshots[key] = json.RawMessage(value)
			}
		}
	}
	return out
}

func queryJSONRows[T any](db *sql.DB, query string, args ...any) []T {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var raw string
		var item T
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &item) == nil {
			out = append(out, item)
		}
	}
	return out
}

func mustJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.Format(time.RFC3339Nano)
}

func keepLast[T any](items []T, max int) []T {
	if max <= 0 || len(items) <= max {
		return items
	}
	return append([]T(nil), items[len(items)-max:]...)
}

func compactLlmCall(item LlmCallItem) LlmCallItem {
	item.RequestPayload = compactMapPayload(item.RequestPayload)
	item.ResponsePayload = compactMapPayload(item.ResponsePayload)
	item.NativeRequestPayload = compactMapPayload(item.NativeRequestPayload)
	item.NativeResponsePayload = compactMapPayload(item.NativeResponsePayload)
	item.Error = compactMapPayload(item.Error)
	item.NativeError = compactMapPayload(item.NativeError)
	return item
}

func compactMapPayload(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) <= maxStoredLlmPayload {
		return value
	}
	out := map[string]any{
		"compacted":   true,
		"jsonBytes":   len(raw),
		"jsonPreview": trimRunes(string(raw), maxStoredLlmPreview),
	}
	for _, key := range []string{"provider", "model", "status", "id", "type", "messageCount", "toolCount", "toolNames", "usage"} {
		if v, ok := value[key]; ok {
			out[key] = v
		}
	}
	return out
}

func trimRunes(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "..."
}

func setItemID(item any, id int) {
	switch v := item.(type) {
	case *AppLogItem:
		v.ID = id
	case *LlmCallItem:
		v.ID = id
	case *NapcatEventItem:
		v.ID = id
	case *NapcatMessageItem:
		v.ID = id
	case *MetricItem:
		v.ID = id
	}
}

// Paginate 对项目切片，并返回兼容前端的分页元数据。
func Paginate[T any](items []T, page, pageSize int) ([]T, map[string]int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	total := len(items)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return items[start:end], map[string]int{"page": page, "pageSize": pageSize, "total": total}
}

// NewestFirst 使用给定降序比较器返回排序后的副本。
func NewestFirst[T any](items []T, less func(a, b T) bool) []T {
	out := append([]T(nil), items...)
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

// StringPtr 将 JSON 标量值规范化为可选字符串。
func StringPtr(v any) *string {
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
		return &x
	case float64:
		return new(common.JSONNumber(x))
	default:
		return nil
	}
}

// IntPtr 将 JSON 标量值规范化为可选整数。
func IntPtr(v any) *int {
	switch x := v.(type) {
	case int:
		return &x
	case float64:
		return new(int(x))
	default:
		return nil
	}
}
