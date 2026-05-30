package story

import (
	"qqbot-ai/internal/db"
	"qqbot-ai/internal/embedding"
)

// StoreEmbeddingCache 把 embedding 客户端缓存接到本地 Store。
type StoreEmbeddingCache struct {
	Store *db.Store
}

func (c StoreEmbeddingCache) FindEmbedding(key embedding.CacheKey) ([]float64, bool) {
	return c.Store.FindEmbedding(db.EmbeddingCacheKey(key))
}

func (c StoreEmbeddingCache) SaveEmbedding(key embedding.CacheKey, text string, values []float64) {
	c.Store.SaveEmbedding(db.EmbeddingCacheKey(key), text, values)
}
