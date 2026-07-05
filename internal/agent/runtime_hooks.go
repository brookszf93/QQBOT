package agent

import (
	"QqBot/internal/agentruntime"
	"context"
)

type RuntimeHook interface {
	OnStart(ctx context.Context, runtime *AgentRuntime)
	OnBeforeRootRound(ctx context.Context, runtime *AgentRuntime)
	OnAfterRootRound(ctx context.Context, runtime *AgentRuntime, result agentruntime.RoundResult, err error)
	OnAfterRootCommit(ctx context.Context, runtime *AgentRuntime, result agentruntime.RoundResult)
	OnBeforeStoryBatch(ctx context.Context, runtime *AgentRuntime, messages []agentruntime.Message, startSeq, endSeq int)
	OnAfterStoryBatch(ctx context.Context, runtime *AgentRuntime, messages []agentruntime.Message, startSeq, endSeq int, err error)
	OnRuntimeError(ctx context.Context, runtime *AgentRuntime, err error)
}

type RuntimeHookSet []RuntimeHook

func (hooks RuntimeHookSet) OnStart(ctx context.Context, runtime *AgentRuntime) {
	for _, hook := range hooks {
		hook.OnStart(ctx, runtime)
	}
}

func (hooks RuntimeHookSet) OnBeforeRootRound(ctx context.Context, runtime *AgentRuntime) {
	for _, hook := range hooks {
		hook.OnBeforeRootRound(ctx, runtime)
	}
}

func (hooks RuntimeHookSet) OnAfterRootRound(ctx context.Context, runtime *AgentRuntime, result agentruntime.RoundResult, err error) {
	for _, hook := range hooks {
		hook.OnAfterRootRound(ctx, runtime, result, err)
	}
}

func (hooks RuntimeHookSet) OnAfterRootCommit(ctx context.Context, runtime *AgentRuntime, result agentruntime.RoundResult) {
	for _, hook := range hooks {
		hook.OnAfterRootCommit(ctx, runtime, result)
	}
}

func (hooks RuntimeHookSet) OnBeforeStoryBatch(ctx context.Context, runtime *AgentRuntime, messages []agentruntime.Message, startSeq, endSeq int) {
	for _, hook := range hooks {
		hook.OnBeforeStoryBatch(ctx, runtime, messages, startSeq, endSeq)
	}
}

func (hooks RuntimeHookSet) OnAfterStoryBatch(ctx context.Context, runtime *AgentRuntime, messages []agentruntime.Message, startSeq, endSeq int, err error) {
	for _, hook := range hooks {
		hook.OnAfterStoryBatch(ctx, runtime, messages, startSeq, endSeq, err)
	}
}

func (hooks RuntimeHookSet) OnRuntimeError(ctx context.Context, runtime *AgentRuntime, err error) {
	for _, hook := range hooks {
		hook.OnRuntimeError(ctx, runtime, err)
	}
}

type NoopRuntimeHook struct{}

func (NoopRuntimeHook) OnStart(context.Context, *AgentRuntime)           {}
func (NoopRuntimeHook) OnBeforeRootRound(context.Context, *AgentRuntime) {}
func (NoopRuntimeHook) OnAfterRootRound(context.Context, *AgentRuntime, agentruntime.RoundResult, error) {
}
func (NoopRuntimeHook) OnAfterRootCommit(context.Context, *AgentRuntime, agentruntime.RoundResult) {}
func (NoopRuntimeHook) OnBeforeStoryBatch(context.Context, *AgentRuntime, []agentruntime.Message, int, int) {
}
func (NoopRuntimeHook) OnAfterStoryBatch(context.Context, *AgentRuntime, []agentruntime.Message, int, int, error) {
}
func (NoopRuntimeHook) OnRuntimeError(context.Context, *AgentRuntime, error) {}
