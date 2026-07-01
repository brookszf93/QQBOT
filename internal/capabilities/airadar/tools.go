package airadar

import (
	"QqBot/internal/agentruntime"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type DetectTool struct {
	Classifier *Classifier
}

func (DetectTool) Definition() agentruntime.ToolDefinition {
	return agentruntime.ToolDefinition{Name: "detect_ai_tone", Description: "检测一段中文文本更像 AI 腔调还是真人聊天腔调；只判断文本风格，不判断作者身份。短文本结果仅供参考。", Parameters: agentruntime.ObjectSchema(map[string]any{
		"text":      map[string]any{"type": "string", "description": "要检测的中文文本。"},
		"threshold": map[string]any{"type": "number", "description": "可选判定阈值，默认使用模型阈值 0.6；提高到 0.7 可减少误伤。"},
	})}
}

func (DetectTool) Kind() string { return "business" }

func (t DetectTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	classifier := t.Classifier
	if classifier == nil {
		var err error
		classifier, err = NewDefaultClassifier()
		if err != nil {
			return jsonResult(map[string]any{"ok": false, "error": "MODEL_LOAD_FAILED", "message": err.Error()}), nil
		}
	}
	text := strings.TrimSpace(stringArg(call.Arguments["text"]))
	if text == "" {
		return jsonResult(map[string]any{"ok": false, "error": "EMPTY_TEXT", "message": "text 不能为空。"}), nil
	}
	threshold, ok := numberArg(call.Arguments["threshold"])
	var result Result
	if ok {
		result = classifier.Predict(text, threshold)
	} else {
		result = classifier.Predict(text)
	}
	return jsonResult(map[string]any{
		"ok":        true,
		"prob":      round(result.Prob, 6),
		"isAI":      result.IsAI,
		"label":     result.Label,
		"threshold": result.Threshold,
		"note":      "检测目标是文本 AI 腔调，不是作者身份；短文本和技术长文更容易误判。",
	}), nil
}

func stringArg(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func numberArg(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		n, err := v.Float64()
		return n, err == nil
	default:
		return 0, false
	}
}

func round(value float64, digits int) float64 {
	format := fmt.Sprintf("%%.%df", digits)
	var out float64
	_, _ = fmt.Sscanf(fmt.Sprintf(format, value), "%f", &out)
	return out
}

func jsonResult(value map[string]any) agentruntime.ToolResult {
	data, _ := json.Marshal(value)
	return agentruntime.ToolResult{Kind: "business", Content: string(data)}
}
