package vision

import (
	"context"

	"qqbot-ai/internal/prompts"
)

// ImagePart 是传给视觉模型的一份图片载荷。
type ImagePart struct {
	MimeType string
	Data     []byte
	Filename string
}

// Client 由能够描述图片的 LLM 适配器实现。
type Client interface {
	Describe(context.Context, string, []ImagePart) (string, error)
}

// Agent 封装 NapCat 图片消息分析所需的图片描述行为。
type Agent struct {
	Client Client
}

// Analyze 使用可选提示词描述一张或多张图片。
func (a Agent) Analyze(ctx context.Context, prompt string, images []ImagePart) (string, error) {
	if a.Client == nil {
		return "图片分析能力未配置", nil
	}
	if prompt == "" {
		prompt = prompts.VisionSystemPrompt()
	}
	return a.Client.Describe(ctx, prompt, images)
}
