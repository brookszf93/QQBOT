package story

import (
	"context"
	"sort"
	"strings"
	"time"
)

// Service 负责 Story 创建和召回行为。
type Service struct {
	Repo   Repository
	Recall interface {
		Search(context.Context, string, int) ([]Story, error)
	}
}

// Create 校验并持久化 Story，并尽量补齐派生字段。
func (s Service) Create(ctx context.Context, story Story) (Story, error) {
	now := time.Now()
	if story.ID == "" {
		story.ID = now.Format("20060102150405.000000000")
	}
	if story.Title == "" {
		story.Title = ExtractTitle(story.Markdown)
	}
	if story.CreatedAt.IsZero() {
		story.CreatedAt = now
	}
	story.UpdatedAt = now
	if err := s.Repo.Save(ctx, story); err != nil {
		return Story{}, err
	}
	return story, nil
}

// Search 优先使用配置好的召回器；没有召回器时退回轻量关键词排序。
func (s Service) Search(ctx context.Context, query string, limit int) ([]Story, error) {
	if s.Recall != nil {
		if items, err := s.Recall.Search(ctx, query, limit); err == nil {
			return items, nil
		}
	}
	items, err := s.Repo.List(ctx)
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 {
		limit = 20
	}
	type scored struct {
		Story
		score float64
	}
	scoredItems := []scored{}
	for _, item := range items {
		score := Score(item, query)
		if query == "" || score > 0 {
			cp := item
			cp.Score = &score
			scoredItems = append(scoredItems, scored{Story: cp, score: score})
		}
	}
	sort.Slice(scoredItems, func(i, j int) bool {
		if scoredItems[i].score == scoredItems[j].score {
			return scoredItems[i].UpdatedAt.After(scoredItems[j].UpdatedAt)
		}
		return scoredItems[i].score > scoredItems[j].score
	})
	out := []Story{}
	for i, item := range scoredItems {
		if i >= limit {
			break
		}
		out = append(out, item.Story)
	}
	return out, nil
}
