package embedding

import (
	"QqBot/internal/config"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Request struct {
	Content              string
	TaskType             string
	Model                string
	OutputDimensionality int
}

type Response struct {
	Provider  string
	Model     string
	Embedding []float64
}

type Cache interface {
	FindEmbedding(key CacheKey) ([]float64, bool)
	SaveEmbedding(key CacheKey, text string, embedding []float64)
}

type CacheKey struct {
	Provider             string
	Model                string
	TaskType             string
	OutputDimensionality int
	TextHash             string
}

type Client struct {
	cfg    config.EmbeddingConfig
	cache  Cache
	client *http.Client
}

func NewClient(cfg config.EmbeddingConfig, cache Cache) *Client {
	return &Client{cfg: cfg, cache: cache, client: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) Embed(ctx context.Context, req Request) (Response, error) {
	model := valueOr(req.Model, c.cfg.Model)
	dim := req.OutputDimensionality
	if dim == 0 {
		dim = c.cfg.OutputDimensionality
	}
	taskType := valueOr(req.TaskType, "RETRIEVAL_DOCUMENT")
	key := CacheKey{
		Provider:             c.cfg.Provider,
		Model:                model,
		TaskType:             taskType,
		OutputDimensionality: dim,
		TextHash:             hashText(req.Content),
	}
	if c.cache != nil {
		if cached, ok := c.cache.FindEmbedding(key); ok {
			return Response{Provider: key.Provider, Model: key.Model, Embedding: cached}, nil
		}
	}

	var resp Response
	var err error
	switch c.cfg.Provider {
	case "google":
		resp, err = c.embedGoogle(ctx, req.Content, taskType, model, dim)
	default:
		err = fmt.Errorf("unsupported embedding provider: %s", c.cfg.Provider)
	}
	if err != nil {
		return Response{}, err
	}
	if c.cache != nil {
		c.cache.SaveEmbedding(key, req.Content, resp.Embedding)
	}
	return resp, nil
}

func (c *Client) embedGoogle(ctx context.Context, content, taskType, model string, dim int) (Response, error) {
	if strings.TrimSpace(c.cfg.APIKey) == "" {
		return Response{}, fmt.Errorf("google embedding apiKey is empty")
	}
	baseURL := strings.TrimRight(valueOr(c.cfg.BaseURL, "https://generativelanguage.googleapis.com"), "/")
	modelPath, modelName := googleModelPath(model)
	bodyPayload := map[string]any{
		"model":   modelPath,
		"content": map[string]any{"parts": []map[string]any{{"text": googleEmbeddingText(content, taskType, modelName)}}},
	}
	if dim > 0 {
		bodyPayload["outputDimensionality"] = dim
	}
	if !isGeminiEmbedding2(modelName) && strings.TrimSpace(taskType) != "" {
		bodyPayload["taskType"] = taskType
	}
	endpoint := fmt.Sprintf("%s/v1beta/%s:embedContent", baseURL, modelPath)
	body, _ := json.Marshal(bodyPayload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", c.cfg.APIKey)
	res, err := c.client.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Response{}, fmt.Errorf("google embedding request failed: %s", res.Status)
	}
	var payload struct {
		Embedding struct {
			Values []float64 `json:"values"`
		} `json:"embedding"`
		Embeddings []struct {
			Values []float64 `json:"values"`
		} `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Response{}, err
	}
	values := payload.Embedding.Values
	if len(values) == 0 && len(payload.Embeddings) > 0 {
		values = payload.Embeddings[0].Values
	}
	if len(values) == 0 {
		return Response{}, fmt.Errorf("google embedding response is invalid")
	}
	return Response{Provider: "google", Model: modelName, Embedding: values}, nil
}

func googleModelPath(model string) (path, name string) {
	name = strings.TrimSpace(model)
	name = strings.TrimPrefix(name, "models/")
	if name == "" {
		name = "gemini-embedding-2"
	}
	return "models/" + url.PathEscape(name), name
}

func isGeminiEmbedding2(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), "gemini-embedding-2")
}

func googleEmbeddingText(content, taskType, model string) string {
	text := strings.TrimSpace(content)
	if !isGeminiEmbedding2(model) {
		return text
	}
	switch strings.ToUpper(strings.TrimSpace(taskType)) {
	case "RETRIEVAL_QUERY":
		return "task: search result | query: " + text
	case "RETRIEVAL_DOCUMENT":
		return "title: none | text: " + text
	case "QUESTION_ANSWERING":
		return "task: question answering | query: " + text
	case "FACT_VERIFICATION":
		return "task: fact checking | query: " + text
	case "CODE_RETRIEVAL_QUERY":
		return "task: code retrieval | query: " + text
	case "CLASSIFICATION":
		return "task: classification | query: " + text
	case "CLUSTERING":
		return "task: clustering | query: " + text
	case "SEMANTIC_SIMILARITY":
		return "task: sentence similarity | query: " + text
	default:
		return text
	}
}

func Normalize(values []float64) []float64 {
	sum := 0.0
	for _, value := range values {
		sum += value * value
	}
	norm := math.Sqrt(sum)
	out := append([]float64(nil), values...)
	if norm == 0 {
		return out
	}
	for i := range out {
		out[i] = out[i] / norm
	}
	return out
}

func Dot(left, right []float64) float64 {
	n := len(left)
	if len(right) < n {
		n = len(right)
	}
	score := 0.0
	for i := 0; i < n; i++ {
		score += left[i] * right[i]
	}
	return score
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func firstEmbedding(payload any) ([]float64, bool) {
	items, ok := payload.([]any)
	if !ok || len(items) == 0 {
		return nil, false
	}
	values, ok := items[0].([]any)
	if !ok {
		return nil, false
	}
	out := make([]float64, 0, len(values))
	for _, value := range values {
		number, ok := value.(float64)
		if !ok {
			return nil, false
		}
		out = append(out, number)
	}
	return out, len(out) > 0
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
