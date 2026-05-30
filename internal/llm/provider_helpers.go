package llm

import (
	"encoding/json"
	"qqbot-ai/internal/common"
	"strings"
)

func firstStringValue(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := common.AsString(m[key]); strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func parseJSONMap(text string) map[string]any {
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil
	}
	return out
}

func numberAny(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	default:
		return 0
	}
}

func anySlice(items []map[string]any) []any {
	out := make([]any, len(items))
	for i := range items {
		out[i] = items[i]
	}
	return out
}

func dataURLPayload(value string) string {
	if idx := strings.Index(value, ","); idx >= 0 {
		return value[idx+1:]
	}
	return value
}

func trimLog(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "..."
}

func toolCallNames(value any) string {
	items, _ := value.([]any)
	if len(items) == 0 {
		return "[]"
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		call, _ := item.(map[string]any)
		if call == nil {
			continue
		}
		names = append(names, common.AsString(call["name"]))
	}
	data, _ := json.Marshal(names)
	return string(data)
}
