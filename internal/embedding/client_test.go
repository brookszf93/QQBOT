package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"qqbot-ai/internal/config"
	"strings"
	"testing"
)

func TestGoogleEmbedding2UsesPromptPrefixWithoutTaskType(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-embedding-2:embedContent" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("x-goog-api-key") != "test-key" {
			t.Fatalf("missing api key header")
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"embedding":{"values":[0.1,0.2]}}`))
	}))
	defer server.Close()

	client := NewClient(config.EmbeddingConfig{
		Provider:             "google",
		APIKey:               "test-key",
		BaseURL:              server.URL,
		Model:                "gemini-embedding-2",
		OutputDimensionality: 2,
	}, nil)
	resp, err := client.Embed(context.Background(), Request{Content: "hello", TaskType: "RETRIEVAL_QUERY"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Model != "gemini-embedding-2" || len(resp.Embedding) != 2 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if _, ok := request["taskType"]; ok {
		t.Fatalf("gemini-embedding-2 request should not include taskType: %#v", request)
	}
	content := request["content"].(map[string]any)
	parts := content["parts"].([]any)
	text := parts[0].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "task: search result | query: ") {
		t.Fatalf("missing retrieval query prefix: %q", text)
	}
}

func TestGoogleEmbedding001KeepsTaskType(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"embedding":{"values":[0.1,0.2]}}`))
	}))
	defer server.Close()

	client := NewClient(config.EmbeddingConfig{
		Provider:             "google",
		APIKey:               "test-key",
		BaseURL:              server.URL,
		Model:                "gemini-embedding-001",
		OutputDimensionality: 2,
	}, nil)
	if _, err := client.Embed(context.Background(), Request{Content: "hello", TaskType: "RETRIEVAL_DOCUMENT"}); err != nil {
		t.Fatal(err)
	}
	if request["taskType"] != "RETRIEVAL_DOCUMENT" {
		t.Fatalf("expected taskType for embedding-001, got %#v", request["taskType"])
	}
	content := request["content"].(map[string]any)
	parts := content["parts"].([]any)
	text := parts[0].(map[string]any)["text"].(string)
	if text != "hello" {
		t.Fatalf("embedding-001 content should not be prefixed, got %q", text)
	}
}
