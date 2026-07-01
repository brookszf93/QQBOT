package agent

import (
	storycap "QqBot/internal/capabilities/story"
	"QqBot/internal/config"
	"QqBot/internal/db"
	"context"
)

type StoryReindexResponse struct {
	Mode             string           `json:"mode"`
	TotalStories     int              `json:"totalStories"`
	TargetedStories  int              `json:"targetedStories"`
	ReindexedStories int              `json:"reindexedStories"`
	SkippedStories   int              `json:"skippedStories"`
	FailedStories    int              `json:"failedStories"`
	Failures         []map[string]any `json:"failures"`
}

func ReindexStories(ctx context.Context, cfg *config.Config, store *db.Store, mode string) StoryReindexResponse {
	if mode == "" {
		mode = "outdated"
	}
	data := store.Snapshot()
	resp := StoryReindexResponse{
		Mode:         mode,
		TotalStories: len(data.Stories),
		Failures:     []map[string]any{},
	}
	targets := storiesToReindex(data, mode, cfg)
	resp.TargetedStories = len(targets)
	resp.SkippedStories = len(data.Stories) - len(targets)
	if len(targets) == 0 {
		return resp
	}
	indexer := storycap.NewMemoryIndexer(cfg, store)
	if indexer == nil {
		resp.FailedStories = len(targets)
		for _, story := range targets {
			resp.Failures = append(resp.Failures, map[string]any{"storyId": story.ID, "message": "Story 记忆 embedding 未配置。"})
		}
		return resp
	}
	for _, story := range targets {
		if err := indexer.ReindexStory(ctx, story); err != nil {
			resp.FailedStories++
			resp.Failures = append(resp.Failures, map[string]any{"storyId": story.ID, "message": err.Error()})
			continue
		}
		resp.ReindexedStories++
	}
	return resp
}

func storiesToReindex(data db.StoreData, mode string, cfg *config.Config) []db.StoryItem {
	if mode == "all" {
		return append([]db.StoryItem(nil), data.Stories...)
	}
	docsByStory := map[string][]db.StoryMemoryDocument{}
	for _, doc := range data.StoryDocuments {
		docsByStory[doc.StoryID] = append(docsByStory[doc.StoryID], doc)
	}
	targets := []db.StoryItem{}
	for _, story := range data.Stories {
		if shouldReindexStory(story, docsByStory[story.ID], cfg) {
			targets = append(targets, story)
		}
	}
	return targets
}

func shouldReindexStory(story db.StoryItem, docs []db.StoryMemoryDocument, cfg *config.Config) bool {
	expected := storycap.BuildMemoryDocumentsForStory(story)
	if len(docs) != len(expected) {
		return true
	}
	embeddingCfg := cfg.Server.Agent.Story.Memory.Embedding
	expectedKinds := map[string]bool{}
	for _, doc := range expected {
		expectedKinds[doc.Kind] = true
	}
	seenKinds := map[string]bool{}
	for _, doc := range docs {
		if !expectedKinds[doc.Kind] {
			return true
		}
		seenKinds[doc.Kind] = true
		if embeddingCfg.Model != "" && doc.EmbeddingModel != embeddingCfg.Model {
			return true
		}
		if embeddingCfg.OutputDimensionality > 0 && doc.EmbeddingDim != embeddingCfg.OutputDimensionality {
			return true
		}
	}
	for kind := range expectedKinds {
		if !seenKinds[kind] {
			return true
		}
	}
	return false
}
