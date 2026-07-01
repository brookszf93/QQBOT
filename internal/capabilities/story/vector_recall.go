package story

import (
	"QqBot/internal/config"
	"QqBot/internal/db"
	"QqBot/internal/embedding"
	"context"
	"fmt"
	"sort"
	"strings"
)

// VectorRecall 使用 Story 文档向量为根 Agent 提供长期记忆召回。
type VectorRecall struct {
	store       *db.Store
	client      *embedding.Client
	cfg         config.EmbeddingConfig
	defaultTopK int
}

// NewVectorRecall 根据配置创建 Story 向量召回器；配置为空时返回 nil。
func NewVectorRecall(cfg *config.Config, store *db.Store) *VectorRecall {
	embeddingCfg := cfg.Server.Agent.Story.Memory.Embedding
	if strings.TrimSpace(embeddingCfg.Provider) == "" || strings.TrimSpace(embeddingCfg.Model) == "" {
		return nil
	}
	topK := cfg.Server.Agent.Story.Memory.Retrieval.TopK
	if topK <= 0 {
		topK = 3
	}
	return &VectorRecall{
		store:       store,
		client:      embedding.NewClient(embeddingCfg, StoreEmbeddingCache{Store: store}),
		cfg:         embeddingCfg,
		defaultTopK: topK,
	}
}

func (r *VectorRecall) Search(ctx context.Context, query string, limit int) ([]Story, error) {
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("Story 向量召回未配置")
	}
	if limit <= 0 {
		limit = r.defaultTopK
	}
	if limit <= 0 {
		limit = 3
	}
	resp, err := r.client.Embed(ctx, embedding.Request{
		Content:              query,
		TaskType:             "RETRIEVAL_QUERY",
		OutputDimensionality: r.cfg.OutputDimensionality,
	})
	if err != nil {
		return nil, err
	}
	queryEmbedding := embedding.Normalize(resp.Embedding)
	data := r.store.Snapshot()
	type hit struct {
		StoryID string
		Kind    string
		Score   float64
	}
	hits := []hit{}
	for _, doc := range data.StoryDocuments {
		if doc.EmbeddingModel != r.cfg.Model || doc.EmbeddingDim != r.cfg.OutputDimensionality {
			continue
		}
		score := embedding.Dot(queryEmbedding, doc.Embedding)
		hits = append(hits, hit{StoryID: doc.StoryID, Kind: doc.Kind, Score: score})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	grouped := map[string]struct {
		Score float64
		Kinds map[string]bool
	}{}
	documentLimit := limit * 3
	if documentLimit < 1 {
		documentLimit = 1
	}
	for i, item := range hits {
		if i >= documentLimit {
			break
		}
		current := grouped[item.StoryID]
		if current.Kinds == nil {
			current.Kinds = map[string]bool{}
			current.Score = item.Score
		}
		if item.Score > current.Score {
			current.Score = item.Score
		}
		current.Kinds[item.Kind] = true
		grouped[item.StoryID] = current
	}
	orderedIDs := make([]string, 0, len(grouped))
	for id := range grouped {
		orderedIDs = append(orderedIDs, id)
	}
	sort.Slice(orderedIDs, func(i, j int) bool { return grouped[orderedIDs[i]].Score > grouped[orderedIDs[j]].Score })
	storyMap := map[string]db.StoryItem{}
	for _, item := range data.Stories {
		storyMap[item.ID] = item
	}
	out := []Story{}
	for i, id := range orderedIDs {
		if i >= limit {
			break
		}
		item, ok := storyMap[id]
		if !ok {
			continue
		}
		score := grouped[id].Score
		kinds := []string{}
		for kind := range grouped[id].Kinds {
			kinds = append(kinds, kind)
		}
		out = append(out, Story{
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
			Score:                 &score,
			MatchedKinds:          kinds,
		})
	}
	return out, nil
}
