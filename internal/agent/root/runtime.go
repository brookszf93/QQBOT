package root

import (
	"context"
	"sync"
	"time"

	"qqbot-ai/internal/agent/context"
	"qqbot-ai/internal/agentruntime"
)

// 它会把事件写入上下文，执行一轮 ReAct，保存助手和工具
// 消息，并向运维页面暴露仪表盘快照。
type Runtime struct {
	mu           sync.Mutex
	Context      *contextstore.Context
	Session      *Session
	Queue        *agentruntime.EventQueue[any]
	Kernel       agentruntime.ReActKernel
	Tools        *agentruntime.ToolCatalog
	LoopState    string
	Initialized  bool
	LastError    string
	LastActivity time.Time
}

// NewRuntime 组装根上下文、会话、事件队列、模型和工具。
func NewRuntime(ctx *contextstore.Context, session *Session, queue *agentruntime.EventQueue[any], model agentruntime.Model, tools *agentruntime.ToolCatalog) *Runtime {
	return &Runtime{Context: ctx, Session: session, Queue: queue, Kernel: agentruntime.ReActKernel{Model: model}, Tools: tools, LoopState: "starting"}
}

// Initialize 标记运行时已就绪，并重置可见循环状态。
func (r *Runtime) Initialize(context.Context) error {
	r.mu.Lock()
	r.Initialized = true
	r.LoopState = "idle"
	r.LastActivity = time.Now()
	r.mu.Unlock()
	return nil
}

// RunOnce 执行一次根 Agent 循环。
//
// 这里刻意保持确定性：先消费事件，再调用模型，
// 最后把助手回复和工具观测写回上下文。
func (r *Runtime) RunOnce(ctx context.Context) error {
	r.mu.Lock()
	r.LoopState = "consuming_events"
	r.mu.Unlock()
	for {
		event, ok := r.Queue.Dequeue()
		if !ok {
			break
		}
		r.Context.AppendEvent(event)
	}
	system, messages, _ := r.Context.Snapshot()
	r.mu.Lock()
	r.LoopState = "calling_llm"
	r.mu.Unlock()
	result, err := r.Kernel.RunRound(ctx, agentruntime.RoundInput{SystemPrompt: system, Messages: messages, Tools: r.Tools, ToolChoice: "auto"})
	if err != nil {
		r.mu.Lock()
		r.LastError = err.Error()
		r.LoopState = "idle"
		r.mu.Unlock()
		return err
	}
	r.Context.AppendMessages(result.Assistant)
	for _, execution := range result.ToolExecutions {
		r.Context.AppendMessages(agentruntime.Message{Role: "tool", ToolCallID: execution.Call.ID, Content: execution.Result.Content})
	}
	r.mu.Lock()
	r.LoopState = "idle"
	r.LastActivity = time.Now()
	r.mu.Unlock()
	return nil
}

// Dashboard 返回供管理界面使用的精简运行时快照。
func (r *Runtime) Dashboard() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, messages, events := r.Context.Snapshot()
	return map[string]any{
		"initialized":    r.Initialized,
		"loopState":      r.LoopState,
		"lastError":      r.LastError,
		"lastActivityAt": r.LastActivity,
		"messageCount":   len(messages),
		"eventCount":     len(events),
		"session":        r.Session.Snapshot(),
	}
}
