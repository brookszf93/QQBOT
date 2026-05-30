package logger

import "qqbot-ai/internal/db"

// Logger 是一个由 Store 支撑的小型结构化日志器。
//
// 元数据写日志，接收端决定记录去向；当前接收端是 db.Store。
type Logger struct {
	Store  *db.Store
	Source string
}

// New 创建绑定到指定来源名称的日志器。
func New(store *db.Store, source string) Logger {
	return Logger{Store: store, Source: source}
}

// Log 记录一条应用日志。
func (l Logger) Log(level, message string, metadata map[string]any) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	if l.Source != "" {
		metadata["source"] = l.Source
	}
	if l.Store != nil {
		l.Store.Log(level, message, metadata)
	}
}

// Info 记录一条信息消息。
func (l Logger) Info(message string, metadata map[string]any) { l.Log("info", message, metadata) }

// Warn 记录一条警告消息。
func (l Logger) Warn(message string, metadata map[string]any) { l.Log("warn", message, metadata) }

// Error 记录一条错误消息。
func (l Logger) Error(message string, metadata map[string]any) { l.Log("error", message, metadata) }
