package agent

import (
	"encoding/json"
	"qqbot-ai/internal/agentruntime"
	"time"
)

func (a *AgentRuntime) loadPersistedSnapshot() (agentRuntimeSnapshot, bool) {
	var snapshot agentRuntimeSnapshot
	if a.store == nil {
		return snapshot, false
	}
	return snapshot, a.store.LoadAgentSnapshot("root", &snapshot)
}

func (a *AgentRuntime) persistSnapshot() {
	if a.store == nil || a.session == nil {
		return
	}
	a.mu.Lock()
	snapshot := agentRuntimeSnapshot{
		RootMessages:     append([]agentruntime.Message(nil), a.rootMessages...),
		StoryMessages:    append([]agentruntime.Message(nil), a.storyMessages...),
		StoryLastSeq:     a.storyLastSeq,
		LastRecallCount:  a.lastRecallCount,
		RecalledStoryIDs: cloneBoolMap(a.recalledStoryIDs),
		Session:          a.session.export(),
		UpdatedAt:        time.Now(),
	}
	fingerprint := snapshotFingerprint(snapshot)
	if fingerprint != "" && fingerprint == a.lastPersistedSnapshotFingerprint {
		a.mu.Unlock()
		return
	}
	a.lastPersistedSnapshotFingerprint = fingerprint
	a.mu.Unlock()
	a.store.SaveAgentSnapshot("root", snapshot)
}

func snapshotFingerprint(snapshot agentRuntimeSnapshot) string {
	payload := struct {
		RootMessages     []agentruntime.Message `json:"rootMessages"`
		StoryMessages    []agentruntime.Message `json:"storyMessages"`
		StoryLastSeq     int                    `json:"storyLastSeq"`
		LastRecallCount  int                    `json:"lastRecallCount"`
		RecalledStoryIDs map[string]bool        `json:"recalledStoryIds"`
		Session          rootSessionSnapshot    `json:"session"`
	}{
		RootMessages:     snapshot.RootMessages,
		StoryMessages:    snapshot.StoryMessages,
		StoryLastSeq:     snapshot.StoryLastSeq,
		LastRecallCount:  snapshot.LastRecallCount,
		RecalledStoryIDs: snapshot.RecalledStoryIDs,
		Session:          snapshot.Session,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(data)
}
