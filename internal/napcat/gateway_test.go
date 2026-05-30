package napcat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"qqbot-ai/internal/capabilities/vision"
	"testing"
)

type fakeVisionClient struct {
	calls int
	desc  string
}

func (c *fakeVisionClient) Describe(context.Context, string, []vision.ImagePart) (string, error) {
	c.calls++
	return c.desc, nil
}

func imageMessagePayload(url string) map[string]any {
	return map[string]any{
		"message": []any{
			map[string]any{
				"type": "image",
				"data": map[string]any{
					"url":  url,
					"file": "old.png",
				},
			},
		},
	}
}

func TestRenderIncomingMessageAnalyzesRealtimeImages(t *testing.T) {
	serverHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverHits++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("fake-png"))
	}))
	defer server.Close()

	client := &fakeVisionClient{desc: "一张测试图片"}
	gateway := &NapcatGateway{vision: vision.Agent{Client: client}}

	got := gateway.renderIncomingMessage(imageMessagePayload(server.URL))
	if got != "[图片:一张测试图片]" {
		t.Fatalf("unexpected rendered message: %q", got)
	}
	if serverHits != 1 {
		t.Fatalf("expected image URL to be fetched once, got %d", serverHits)
	}
	if client.calls != 1 {
		t.Fatalf("expected vision client to be called once, got %d", client.calls)
	}
}

func TestRenderIncomingMessageSkipsImageAnalysisForHistory(t *testing.T) {
	serverHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverHits++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("fake-png"))
	}))
	defer server.Close()

	client := &fakeVisionClient{desc: "不应该出现"}
	gateway := &NapcatGateway{vision: vision.Agent{Client: client}}

	got := gateway.renderIncomingMessageWithoutImageAnalysis(imageMessagePayload(server.URL))
	if got != "[图片:old.png]" {
		t.Fatalf("unexpected rendered message: %q", got)
	}
	if serverHits != 0 {
		t.Fatalf("history image should not be fetched, got %d hits", serverHits)
	}
	if client.calls != 0 {
		t.Fatalf("history image should not call vision client, got %d calls", client.calls)
	}
}
