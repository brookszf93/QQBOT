package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"QqBot/internal/agentruntime"
	"QqBot/internal/capabilities/vision"
	"QqBot/internal/common"
)

type napcatRequester interface {
	Request(action string, params map[string]any) (any, error)
}

type analyzeImageTool struct {
	vision        vision.Agent
	requester     napcatRequester
	screenshotDir string
	httpClient    *http.Client
}

func (t analyzeImageTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "analyze_image", Description: "按 QQ messageId、图片 URL 或受控本地截图路径识别图片内容；只返回识别结果，不会发送消息。", Parameters: agentruntime.ObjectSchema(map[string]any{
		"messageId": map[string]any{"type": "integer", "description": "可选 QQ 消息 ID；用于重新获取该消息里的第一张图片并识别。"},
		"imageUrl":  map[string]any{"type": "string", "description": "可选 http/https 图片 URL。"},
		"imagePath": map[string]any{"type": "string", "description": "可选本地图片路径；仅允许 browser_screenshot 返回的受控截图路径。"},
		"prompt":    map[string]any{"type": "string", "description": "可选识别重点，例如“看图中文字”或“概括表情包含义”。"},
	})}
}

func (t analyzeImageTool) Kind() string { return "business" }

func (t analyzeImageTool) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	prompt := strings.TrimSpace(common.AsString(call.Arguments["prompt"]))
	source := ""
	var part vision.ImagePart
	var err error
	if messageID := intValue(call.Arguments["messageId"]); messageID > 0 {
		part, source, err = t.imageFromMessage(ctx, messageID)
	} else if imagePath := strings.TrimSpace(common.AsString(call.Arguments["imagePath"])); imagePath != "" {
		var safePath string
		safePath, err = allowedScreenshotPath(t.screenshotDir, imagePath)
		if err == nil {
			part, err = loadImagePart(ctx, t.httpClient, safePath)
			source = safePath
		}
	} else if imageURL := strings.TrimSpace(common.AsString(call.Arguments["imageUrl"])); imageURL != "" {
		if !strings.HasPrefix(imageURL, "http://") && !strings.HasPrefix(imageURL, "https://") {
			err = fmt.Errorf("imageUrl 仅支持 http/https")
		} else {
			part, err = loadImagePart(ctx, t.httpClient, imageURL)
			source = imageURL
		}
	} else {
		err = fmt.Errorf("需要提供 messageId、imageUrl 或 imagePath 之一")
	}
	if err != nil {
		return jsonToolResult(map[string]any{"ok": false, "error": "IMAGE_UNAVAILABLE", "message": err.Error()}), nil
	}
	description, err := t.vision.Analyze(ctx, prompt, []vision.ImagePart{part})
	if err != nil {
		return jsonToolResult(map[string]any{"ok": false, "error": "IMAGE_ANALYSIS_FAILED", "message": err.Error(), "source": source}), nil
	}
	description = strings.TrimSpace(description)
	return jsonToolResult(map[string]any{
		"ok":          true,
		"description": description,
		"source":      source,
		"mimeType":    part.MimeType,
		"filename":    part.Filename,
	}), nil
}

func (t analyzeImageTool) imageFromMessage(ctx context.Context, messageID int) (vision.ImagePart, string, error) {
	if t.requester == nil {
		return vision.ImagePart{}, "", fmt.Errorf("NapCat 请求能力不可用")
	}
	data, err := t.requester.Request("get_msg", map[string]any{"message_id": messageID})
	if err != nil {
		return vision.ImagePart{}, "", err
	}
	message, _ := data.(map[string]any)
	segments := normalizeImageToolSegments(message["message"])
	if len(segments) == 0 {
		return vision.ImagePart{}, "", fmt.Errorf("消息 %d 中没有可识别的图片段", messageID)
	}
	errs := []string{}
	for _, segment := range segments {
		if common.AsString(segment["type"]) != "image" {
			continue
		}
		imageData, _ := segment["data"].(map[string]any)
		if imageData == nil {
			imageData = map[string]any{}
		}
		t.resolveNapcatImagePayload(imageData)
		part, source, err := imagePartFromSegment(ctx, t.httpClient, imageData)
		if err == nil {
			return part, source, nil
		}
		errs = append(errs, err.Error())
	}
	if len(errs) == 0 {
		return vision.ImagePart{}, "", fmt.Errorf("消息 %d 中没有 image 类型图片段", messageID)
	}
	return vision.ImagePart{}, "", errors.New(strings.Join(errs, " | "))
}

func (t analyzeImageTool) resolveNapcatImagePayload(data map[string]any) {
	file := strings.TrimSpace(common.AsString(data["file"]))
	if file == "" || strings.HasPrefix(file, "http://") || strings.HasPrefix(file, "https://") || strings.HasPrefix(file, "file://") {
		return
	}
	if _, err := os.Stat(file); err == nil {
		data["localFile"] = file
		return
	}
	result, err := t.requester.Request("get_image", map[string]any{"file": file})
	if err != nil {
		return
	}
	payload, _ := result.(map[string]any)
	for _, key := range []string{"base64", "imageBase64", "mimeType", "contentType"} {
		if value := strings.TrimSpace(common.AsString(payload[key])); value != "" {
			data[key] = value
		}
	}
	if filename := firstNonEmpty(common.AsString(payload["file"]), common.AsString(payload["path"])); filename != "" {
		data["resolvedFilename"] = filepath.Base(filename)
	}
	if localFile := existingToolImagePath(payload); localFile != "" {
		data["localFile"] = localFile
	}
	if imageURL := strings.TrimSpace(common.AsString(payload["url"])); strings.HasPrefix(imageURL, "http://") || strings.HasPrefix(imageURL, "https://") {
		data["url"] = imageURL
	}
}

func existingToolImagePath(payload map[string]any) string {
	for _, key := range []string{"file", "path"} {
		candidate := strings.TrimSpace(common.AsString(payload[key]))
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func imagePartFromSegment(ctx context.Context, client *http.Client, data map[string]any) (vision.ImagePart, string, error) {
	if part, err := imagePartFromBase64(data); err == nil {
		return part, "base64", nil
	}
	errs := []string{}
	for _, key := range []string{"localFile", "url", "file"} {
		ref := strings.TrimSpace(common.AsString(data[key]))
		if ref == "" {
			continue
		}
		part, err := loadImagePart(ctx, client, ref)
		if err == nil {
			return part, ref, nil
		}
		errs = append(errs, ref+": "+err.Error())
	}
	if len(errs) == 0 {
		return vision.ImagePart{}, "", fmt.Errorf("图片段没有 url/file/localFile/base64")
	}
	return vision.ImagePart{}, "", errors.New(strings.Join(errs, " | "))
}

func imagePartFromBase64(data map[string]any) (vision.ImagePart, error) {
	encoded := firstNonEmpty(common.AsString(data["base64"]), common.AsString(data["imageBase64"]))
	if strings.TrimSpace(encoded) == "" {
		return vision.ImagePart{}, fmt.Errorf("缺少 base64 图片数据")
	}
	mimeType := firstNonEmpty(common.AsString(data["mimeType"]), common.AsString(data["contentType"]))
	filename := firstNonEmpty(common.AsString(data["resolvedFilename"]), common.AsString(data["file"]), "image.png")
	if strings.HasPrefix(encoded, "data:") {
		prefix, payload, ok := strings.Cut(encoded, ",")
		if ok {
			encoded = payload
			if mimeType == "" {
				mimeType = strings.TrimPrefix(strings.TrimSuffix(prefix, ";base64"), "data:")
			}
		}
	}
	content, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return vision.ImagePart{}, err
	}
	if mimeType == "" {
		mimeType = inferToolImageMimeType(filename, "")
	}
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	return vision.ImagePart{MimeType: mimeType, Data: content, Filename: filename}, nil
}

func loadImagePart(ctx context.Context, client *http.Client, imageRef string) (vision.ImagePart, error) {
	parsed, err := url.Parse(imageRef)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		if client == nil {
			client = &http.Client{Timeout: 20 * time.Second}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageRef, nil)
		if err != nil {
			return vision.ImagePart{}, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return vision.ImagePart{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return vision.ImagePart{}, fmt.Errorf("下载图片返回 %d", resp.StatusCode)
		}
		mimeType := inferToolImageMimeType(imageRef, resp.Header.Get("Content-Type"))
		if mimeType == "" {
			return vision.ImagePart{}, fmt.Errorf("无法识别图片 MIME 类型")
		}
		content, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return vision.ImagePart{}, err
		}
		return vision.ImagePart{MimeType: mimeType, Data: content, Filename: inferToolFilename(imageRef)}, nil
	}
	filename := imageRef
	if filepath.VolumeName(filename) != "" {
		// Windows absolute path such as C:\tmp\a.png; url.Parse sees "c" as a scheme.
	} else if err == nil && parsed.Scheme == "file" {
		filename, err = url.PathUnescape(parsed.Path)
		if err != nil {
			return vision.ImagePart{}, err
		}
		if filepath.VolumeName(filename) == "" && len(filename) >= 3 && filename[0] == '/' && filename[2] == ':' {
			filename = filename[1:]
		}
	} else if err == nil && parsed.Scheme != "" {
		return vision.ImagePart{}, fmt.Errorf("不支持的图片引用协议 %q", parsed.Scheme)
	} else if !filepath.IsAbs(filename) && !strings.ContainsAny(filename, `/\`) {
		return vision.ImagePart{}, fmt.Errorf("裸图片 file id 不是本地路径")
	}
	content, err := os.ReadFile(filename)
	if err != nil {
		return vision.ImagePart{}, err
	}
	baseName := filepath.Base(filename)
	mimeType := inferToolImageMimeType(baseName, "")
	if mimeType == "" {
		return vision.ImagePart{}, fmt.Errorf("无法识别图片 MIME 类型")
	}
	return vision.ImagePart{MimeType: mimeType, Data: content, Filename: baseName}, nil
}

func normalizeImageToolSegments(value any) []map[string]any {
	switch x := value.(type) {
	case []any:
		out := []map[string]any{}
		for _, item := range x {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case []map[string]any:
		return x
	default:
		return nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func inferToolImageMimeType(rawURL, contentType string) string {
	if idx := strings.Index(contentType, ";"); idx >= 0 {
		contentType = contentType[:idx]
	}
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if strings.HasPrefix(contentType, "image/") {
		return contentType
	}
	switch strings.ToLower(path.Ext(inferToolFilename(rawURL))) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	default:
		return ""
	}
}

func inferToolFilename(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Path == "" {
		return filepath.Base(rawURL)
	}
	return strings.TrimSpace(path.Base(parsed.Path))
}
