package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"QqBot/internal/config"
)

func TestVisionAttemptSupportsImages(t *testing.T) {
	if visionAttemptSupportsImages("deepseek", "deepseek-v4-pro") {
		t.Fatal("deepseek reasoner should not be treated as image-capable")
	}
	if !visionAttemptSupportsImages("openai", "gpt-4o-mini") {
		t.Fatal("gpt-4o-mini should be treated as image-capable")
	}
	if !visionAttemptSupportsImages("claude-code", "claude-sonnet-4.6") {
		t.Fatal("claude-code should be treated as image-capable")
	}
	if !visionAttemptSupportsImages("google", "gemini-3.5-flash") {
		t.Fatal("gemini should be treated as image-capable")
	}
}

func TestOpenAIChatPayloadIncludesMaxTokens(t *testing.T) {
	payload := toOpenAIChatPayload(LLMChatRequest{Provider: "deepseek", Model: "deepseek-v4-flash", MaxTokens: 2048})
	if payload["max_tokens"] != 2048 {
		t.Fatalf("max_tokens missing from payload: %#v", payload)
	}
}

func TestDeepSeekRequiredToolChoiceUsesAutoForThinkingMode(t *testing.T) {
	payload := toOpenAIChatPayload(LLMChatRequest{
		Provider:   "deepseek",
		Model:      "deepseek-v4-flash",
		ToolChoice: "required",
		Tools: []LLMTool{{
			Name:       "act",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		}},
	})
	if payload["tool_choice"] != "auto" {
		t.Fatalf("deepseek thinking mode does not support required tool_choice, got %#v", payload["tool_choice"])
	}
}

func TestDeepSeekNamedToolChoiceUsesAutoForThinkingMode(t *testing.T) {
	if choice := openAIChatToolChoice("deepseek", map[string]any{"tool_name": "act"}); choice != "auto" {
		t.Fatalf("deepseek thinking mode does not support named tool_choice, got %#v", choice)
	}
}

func TestLongCatUsesOpenAICompatibleChatCompletions(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","model":"LongCat-2.0","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	client := &LLMClient{
		cfg: &config.Config{Server: config.ServerConfig{LLM: config.LLMConfig{Providers: config.LLMProvidersConfig{
			LongCat: config.LLMProviderConfig{APIKey: "longcat-key", BaseURL: server.URL + "/openai/v1", Models: []string{"LongCat-2.0"}},
		}}}},
		http: server.Client(),
	}
	_, response, _, err := client.callProvider(context.Background(), LLMChatRequest{
		Provider:   "longcat",
		Model:      "LongCat-2.0",
		Messages:   []LLMMessage{{Role: "user", Content: "你好"}},
		ToolChoice: "required",
		Tools:      []LLMTool{{Name: "wait", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/openai/v1/chat/completions" {
		t.Fatalf("unexpected LongCat path: %s", gotPath)
	}
	if gotAuth != "Bearer longcat-key" {
		t.Fatalf("unexpected auth header: %q", gotAuth)
	}
	if gotPayload["model"] != "LongCat-2.0" || gotPayload["tool_choice"] != "required" {
		t.Fatalf("unexpected payload: %#v", gotPayload)
	}
	if response["provider"] != "longcat" {
		t.Fatalf("unexpected response provider: %#v", response)
	}
}

func TestLongCatChatCompletionsURLAcceptsOfficialSDKBaseURL(t *testing.T) {
	got := chatCompletionsURL("longcat", "https://api.longcat.chat/openai")
	want := "https://api.longcat.chat/openai/v1/chat/completions"
	if got != want {
		t.Fatalf("unexpected LongCat URL: got %q want %q", got, want)
	}
}

func TestRequiredSingleActDoesNotCoercePlainTextToSendMessage(t *testing.T) {
	resp := coerceRequiredSingleActResponse(LLMChatRequest{
		ToolChoice: "required",
		Tools:      []LLMTool{{Name: "act"}},
	}, map[string]any{"message": map[string]any{"role": "assistant", "content": "来一张帕秋莉版的"}})
	message := resp["message"].(map[string]any)
	if message["content"] != "来一张帕秋莉版的" {
		t.Fatalf("plain text should remain unsent content, got %#v", message)
	}
	if calls, ok := message["toolCalls"].([]any); ok && len(calls) > 0 {
		t.Fatalf("plain text must not be coerced into send_message: %#v", calls)
	}
}

func TestRequiredSingleActCoercesPlainWaitToWaitAction(t *testing.T) {
	resp := coerceRequiredSingleActResponse(LLMChatRequest{
		ToolChoice: "required",
		Tools:      []LLMTool{{Name: "act"}},
	}, map[string]any{"message": map[string]any{"role": "assistant", "content": " wait. "}})
	message := resp["message"].(map[string]any)
	calls := message["toolCalls"].([]any)
	args := calls[0].(map[string]any)["arguments"].(map[string]any)
	if args["action"] != "wait" {
		t.Fatalf("plain wait should become act wait, got %#v", args)
	}
}

func TestGoogleInteractionInputConvertsInlineImage(t *testing.T) {
	data := []byte("image")
	input, summary, err := googleInteractionInput([]LLMMessage{{
		Role: "user",
		Content: []any{
			map[string]any{"type": "text", "text": "describe"},
			map[string]any{"type": "image", "mimeType": "image/png", "dataUrl": "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(input) != 2 {
		t.Fatalf("unexpected google interaction input: %#v", input)
	}
	image, _ := input[1].(map[string]any)
	decoded, err := base64.StdEncoding.DecodeString(image["data"].(string))
	if err != nil || string(decoded) != string(data) {
		t.Fatalf("unexpected image bytes: %q err=%v", decoded, err)
	}
	if len(summary) != 1 {
		t.Fatalf("unexpected request summary: %#v", summary)
	}
}
