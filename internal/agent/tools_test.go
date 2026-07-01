package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"QqBot/internal/agentruntime"
	"QqBot/internal/capabilities/airadar"
	"QqBot/internal/capabilities/vision"
	"QqBot/internal/db"
)

type recordingMessageSender struct {
	target  string
	message string
}

func (s *recordingMessageSender) SendGroupMessage(groupID, message string) (int, error) {
	s.target = "group:" + groupID
	s.message = message
	return 1, nil
}

func (s *recordingMessageSender) SendPrivateMessage(userID, message string) (int, error) {
	s.target = "private:" + userID
	s.message = message
	return 2, nil
}

type fakeVisionClient struct {
	called   bool
	prompt   string
	filename string
	mimeType string
}

func (c *fakeVisionClient) Describe(_ context.Context, prompt string, images []vision.ImagePart) (string, error) {
	c.called = true
	c.prompt = prompt
	if len(images) > 0 {
		c.filename = images[0].Filename
		c.mimeType = images[0].MimeType
	}
	return "图里有一只测试猫", nil
}

type fakeNapcatImageRequester struct {
	*recordingMessageSender
}

func (r fakeNapcatImageRequester) Request(action string, params map[string]any) (any, error) {
	switch action {
	case "get_msg":
		return map[string]any{
			"message": []any{
				map[string]any{"type": "image", "data": map[string]any{"file": "qq-image-id"}},
			},
		}, nil
	case "get_image":
		return map[string]any{
			"base64":   base64.StdEncoding.EncodeToString([]byte{1, 2, 3}),
			"mimeType": "image/png",
			"file":     "qq.png",
		}, nil
	default:
		return nil, nil
	}
}

func TestSendMessageAcceptsControlledBrowserScreenshot(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "browser.png")
	if err := os.WriteFile(imagePath, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	sender := &recordingMessageSender{}
	tool := sendMessageTool{sender: sender, screenshotDir: dir}
	result, err := tool.Execute(context.Background(), agentruntime.ToolCall{
		Name: "send_message",
		Arguments: map[string]any{
			"targetType": "group",
			"targetId":   "1001",
			"message":    "页面截图",
			"imagePath":  imagePath,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sender.target != "group:1001" || !strings.Contains(sender.message, "[CQ:image,file=file:///") {
		t.Fatalf("image was not sent as a CQ segment: target=%s message=%s result=%s", sender.target, sender.message, result.Content)
	}
}

func TestSendMessageRejectsImageOutsideScreenshotDirectory(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	sender := &recordingMessageSender{}
	tool := sendMessageTool{sender: sender, screenshotDir: root}
	result, err := tool.Execute(context.Background(), agentruntime.ToolCall{
		Name: "send_message",
		Arguments: map[string]any{
			"targetType": "private",
			"targetId":   "2001",
			"imagePath":  outside,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sender.message != "" || !strings.Contains(result.Content, "IMAGE_PATH_NOT_ALLOWED") {
		t.Fatalf("outside image should be rejected: sent=%q result=%s", sender.message, result.Content)
	}
}

func TestSendMessageRejectsHighAIToneText(t *testing.T) {
	classifier, err := airadar.NewDefaultClassifier()
	if err != nil {
		t.Fatal(err)
	}
	sender := &recordingMessageSender{}
	tool := sendMessageTool{sender: sender, aiToneClassifier: classifier, aiToneThreshold: 0.65}
	result, err := tool.Execute(context.Background(), agentruntime.ToolCall{
		Name: "send_message",
		Arguments: map[string]any{
			"targetType": "private",
			"targetId":   "2001",
			"message":    "刚看到一个 有人用二战侦察相机拍了四十年冰川 十万张照片 相机63磅 从一万英尺高空拍的 照片边缘有物理仪器记录的时间和高度 比数码相机早几十年",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sender.message != "" || !strings.Contains(result.Content, "AI_TONE_TOO_HIGH") {
		t.Fatalf("high AI-tone message should be blocked before sending: sent=%q result=%s", sender.message, result.Content)
	}
}

func TestSendMessageAllowsLowAIToneText(t *testing.T) {
	classifier, err := airadar.NewDefaultClassifier()
	if err != nil {
		t.Fatal(err)
	}
	sender := &recordingMessageSender{}
	tool := sendMessageTool{sender: sender, aiToneClassifier: classifier, aiToneThreshold: 0.65}
	result, err := tool.Execute(context.Background(), agentruntime.ToolCall{
		Name: "send_message",
		Arguments: map[string]any{
			"targetType": "private",
			"targetId":   "2001",
			"message":    "运动戴耳机 日常戴表 苹果：两个都买 谢谢🤝",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sender.target != "private:2001" || sender.message == "" || strings.Contains(result.Content, "AI_TONE_TOO_HIGH") {
		t.Fatalf("low AI-tone message should be sent: target=%s message=%q result=%s", sender.target, sender.message, result.Content)
	}
}

func TestSendMessageLogsAIToneScore(t *testing.T) {
	classifier, err := airadar.NewDefaultClassifier()
	if err != nil {
		t.Fatal(err)
	}
	store, err := db.OpenStore(filepath.Join(t.TempDir(), "store.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sender := &recordingMessageSender{}
	tool := sendMessageTool{sender: sender, store: store, aiToneClassifier: classifier, aiToneThreshold: 0.65}
	_, err = tool.Execute(context.Background(), agentruntime.ToolCall{
		Name: "send_message",
		Arguments: map[string]any{
			"targetType": "private",
			"targetId":   "2001",
			"message":    "运动戴耳机 日常戴表 苹果：两个都买 谢谢🤝",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	logs := store.Snapshot().AppLogs
	if len(logs) == 0 {
		t.Fatal("expected AI tone log")
	}
	last := logs[len(logs)-1]
	if last.Metadata["event"] != "agent.send_message.ai_tone_checked" || last.Metadata["prob"] == nil || last.Metadata["message"] == "" {
		t.Fatalf("unexpected AI tone log: %#v", last)
	}
}

func TestAnalyzeImageAcceptsControlledScreenshotPath(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "browser.png")
	if err := os.WriteFile(imagePath, []byte{1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeVisionClient{}
	tool := analyzeImageTool{vision: vision.Agent{Client: client}, screenshotDir: dir}
	result, err := tool.Execute(context.Background(), agentruntime.ToolCall{
		Name: "analyze_image",
		Arguments: map[string]any{
			"imagePath": imagePath,
			"prompt":    "看文字",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !client.called || client.prompt != "看文字" || !strings.Contains(result.Content, `"ok":true`) {
		t.Fatalf("image path should be analyzed: called=%v prompt=%q result=%s", client.called, client.prompt, result.Content)
	}
}

func TestAnalyzeImageRejectsPathOutsideScreenshotDirectory(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte{1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeVisionClient{}
	tool := analyzeImageTool{vision: vision.Agent{Client: client}, screenshotDir: root}
	result, err := tool.Execute(context.Background(), agentruntime.ToolCall{
		Name:      "analyze_image",
		Arguments: map[string]any{"imagePath": outside},
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.called || !strings.Contains(result.Content, "IMAGE_UNAVAILABLE") {
		t.Fatalf("outside image should be rejected before vision: called=%v result=%s", client.called, result.Content)
	}
}

func TestAnalyzeImageDownloadsURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{1, 2, 3})
	}))
	defer server.Close()
	client := &fakeVisionClient{}
	tool := analyzeImageTool{vision: vision.Agent{Client: client}, httpClient: server.Client()}
	result, err := tool.Execute(context.Background(), agentruntime.ToolCall{
		Name:      "analyze_image",
		Arguments: map[string]any{"imageUrl": server.URL + "/cat.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ok"] != true || client.mimeType != "image/png" {
		t.Fatalf("url image should be analyzed: mime=%s result=%s", client.mimeType, result.Content)
	}
}

func TestAnalyzeImageCanResolveQQMessageImageByMessageID(t *testing.T) {
	client := &fakeVisionClient{}
	tool := analyzeImageTool{
		vision:    vision.Agent{Client: client},
		requester: fakeNapcatImageRequester{recordingMessageSender: &recordingMessageSender{}},
	}
	result, err := tool.Execute(context.Background(), agentruntime.ToolCall{
		Name:      "analyze_image",
		Arguments: map[string]any{"messageId": 123},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !client.called || client.filename != "qq.png" || !strings.Contains(result.Content, "图里有一只测试猫") {
		t.Fatalf("message image should be analyzed: called=%v filename=%s result=%s", client.called, client.filename, result.Content)
	}
}
