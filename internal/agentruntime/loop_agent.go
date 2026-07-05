package agentruntime

import (
	"context"
	"sync"
	"time"
)

// LoopAgent 是 root 和任务 Agent 共用的生命周期约定。
type LoopAgent interface {
	Initialize(context.Context) error
	Run(context.Context) error
	Stop()
}

// BaseLoopAgent 提供可取消的周期性 RunOnce 循环。
//
// 更具体的 Agent 复用该行为，并提供自己的 RunOnce。
type BaseLoopAgent struct {
	Interval        time.Duration
	RunOnce         func(context.Context) error
	OnStopRequested func()

	mu      sync.Mutex
	running bool
	stop    context.CancelFunc
}

// Initialize 准备循环宿主；基础实现不执行任何操作。
func (a *BaseLoopAgent) Initialize(context.Context) error { return nil }

// Run 启动循环，直到调用 Stop 或上下文被取消。
func (a *BaseLoopAgent) Run(ctx context.Context) error {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	a.stop = cancel
	a.running = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.running = false
		a.mu.Unlock()
	}()

	var ticker *time.Ticker
	if a.Interval > 0 {
		ticker = time.NewTicker(a.Interval)
		defer ticker.Stop()
	}
	for {
		if a.RunOnce != nil {
			if err := a.RunOnce(ctx); err != nil {
				return err
			}
		}
		if ticker == nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				continue
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Stop 取消当前正在运行的 Run 调用。
func (a *BaseLoopAgent) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.OnStopRequested != nil {
		a.OnStopRequested()
	}
	if a.stop != nil {
		a.stop()
	}
}
