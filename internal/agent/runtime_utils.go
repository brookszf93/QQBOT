package agent

import (
	"encoding/json"
	"fmt"
	"qqbot-ai/internal/agentruntime"
	"strings"
	"time"
)

func trimPreview(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func runtimeToolCallNames(calls []agentruntime.ToolCall) string {
	if len(calls) == 0 {
		return "[]"
	}
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		names = append(names, call.Name)
	}
	data, _ := json.Marshal(names)
	return string(data)
}

func mustCompactJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(data)
}

func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func sameBeijingMinute(a, b time.Time) bool {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.Local
	}
	aa := a.In(loc)
	bb := b.In(loc)
	return aa.Year() == bb.Year() &&
		aa.Month() == bb.Month() &&
		aa.Day() == bb.Day() &&
		aa.Hour() == bb.Hour() &&
		aa.Minute() == bb.Minute()
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}

func intValue(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case string:
		var n int
		_, _ = fmt.Sscanf(x, "%d", &n)
		return n
	default:
		return 0
	}
}

func intSlice(v any) []int {
	switch items := v.(type) {
	case []int:
		return append([]int(nil), items...)
	case []any:
		out := []int{}
		for _, item := range items {
			if id := intValue(item); id > 0 {
				out = append(out, id)
			}
		}
		return out
	case []float64:
		out := make([]int, 0, len(items))
		for _, item := range items {
			out = append(out, int(item))
		}
		return out
	default:
		if id := intValue(v); id > 0 {
			return []int{id}
		}
		return nil
	}
}

func boolValue(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(x, "true") || x == "1"
	default:
		return false
	}
}
