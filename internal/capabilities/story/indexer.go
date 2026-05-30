package story

import (
	"context"
	"qqbot-ai/internal/config"
	"qqbot-ai/internal/db"
	"qqbot-ai/internal/embedding"
	"strings"
)

// MemoryIndexer 负责把 Story 拆成多个可向量化文档并写回 Store。
type MemoryIndexer struct {
	store  *db.Store
	client *embedding.Client
	cfg    config.EmbeddingConfig
}

// NewMemoryIndexer 根据配置创建 Story 向量索引器；配置为空时返回 nil。
func NewMemoryIndexer(cfg *config.Config, store *db.Store) *MemoryIndexer {
	embeddingCfg := cfg.Server.Agent.Story.Memory.Embedding
	if strings.TrimSpace(embeddingCfg.Provider) == "" || strings.TrimSpace(embeddingCfg.Model) == "" {
		return nil
	}
	return &MemoryIndexer{
		store:  store,
		client: embedding.NewClient(embeddingCfg, StoreEmbeddingCache{Store: store}),
		cfg:    embeddingCfg,
	}
}

func (i *MemoryIndexer) ReindexStory(ctx context.Context, item db.StoryItem) error {
	if i == nil || i.client == nil {
		return nil
	}
	content := parseStoryContent(item)
	docs := buildStoryMemoryDocuments(content)
	indexed := make([]db.StoryMemoryDocument, 0, len(docs))
	for _, doc := range docs {
		resp, err := i.client.Embed(ctx, embedding.Request{
			Content:              doc.Content,
			TaskType:             "RETRIEVAL_DOCUMENT",
			OutputDimensionality: i.cfg.OutputDimensionality,
		})
		if err != nil {
			return err
		}
		normalized := embedding.Normalize(resp.Embedding)
		indexed = append(indexed, db.StoryMemoryDocument{
			Kind:           doc.Kind,
			Content:        doc.Content,
			EmbeddingModel: resp.Model,
			EmbeddingDim:   len(normalized),
			Embedding:      normalized,
		})
	}
	i.store.ReplaceStoryMemoryDocuments(item.ID, indexed)
	return nil
}
