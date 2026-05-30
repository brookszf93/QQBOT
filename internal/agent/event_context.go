package agent

import (
	"fmt"
	"qqbot-ai/internal/common"
	"qqbot-ai/internal/prompts"
	"time"
)

func (a *AgentRuntime) renderEventContext(event AgentEvent) string {
	switch event.Type {
	case "wake":
		return prompts.WakeReminder(event.At)
	case "napcat_group_message", "napcat_private_message":
		nickname := common.AsString(event.Data["nickname"])
		if nickname == "" {
			nickname = "未知用户"
		}
		return prompts.QQMessageAt(nickname, common.AsString(event.Data["userId"]), common.AsString(event.Data["rawMessage"]), event.At)
	case "news_article_ingested":
		return fmt.Sprintf(`<system_reminder kind="news_event" time="%s">
source: ithome
new_articles: 1
</system_reminder>`, reminderTime(event.At))
	case "news_articles_ingested":
		count := len(intSlice(event.Data["articleIds"]))
		return fmt.Sprintf(`<system_reminder kind="news_event" time="%s">
source: ithome
new_articles: %d
</system_reminder>`, reminderTime(event.At), count)
	}
	return fmt.Sprintf(`<system_reminder kind="event" time="%s">
type: %s
data: %v
</system_reminder>`, reminderTime(event.At), event.Type, event.Data)
}

func reminderTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.Format("2006-01-02 15:04:05 -07:00")
}

func coalesceAndPrioritizeEvents(events []AgentEvent) []AgentEvent {
	if len(events) <= 1 {
		return events
	}
	privateEvents := []AgentEvent{}
	groupEvents := []AgentEvent{}
	otherEvents := []AgentEvent{}
	wakeEvents := []AgentEvent{}
	newsIDs := []int{}
	for _, event := range events {
		switch event.Type {
		case "napcat_private_message":
			privateEvents = append(privateEvents, event)
		case "napcat_group_message":
			groupEvents = append(groupEvents, event)
		case "wake":
			wakeEvents = append(wakeEvents, event)
		case "news_article_ingested":
			if id := intValue(event.Data["articleId"]); id > 0 {
				newsIDs = append(newsIDs, id)
			}
		case "news_articles_ingested":
			newsIDs = append(newsIDs, intSlice(event.Data["articleIds"])...)
		default:
			otherEvents = append(otherEvents, event)
		}
	}
	out := append([]AgentEvent{}, privateEvents...)
	out = append(out, groupEvents...)
	out = append(out, otherEvents...)
	if len(newsIDs) > 0 {
		out = append(out, AgentEvent{Type: "news_articles_ingested", Data: map[string]any{"sourceKey": "ithome", "articleIds": newsIDs}, At: time.Now()})
	}
	if len(wakeEvents) > 0 {
		out = append(out, selectWakeEvent(wakeEvents))
	}
	return out
}

func selectWakeEvent(events []AgentEvent) AgentEvent {
	selected := events[len(events)-1]
	bestPriority := wakePriority(selected)
	for _, event := range events {
		if priority := wakePriority(event); priority > bestPriority {
			selected = event
			bestPriority = priority
		}
	}
	return selected
}

func wakePriority(event AgentEvent) int {
	switch common.AsString(event.Data["reason"]) {
	case "private_unread":
		return 4
	case "group_unread":
		return 3
	case "portal_unread":
		return 2
	default:
		return 1
	}
}

func targetFromEvent(event AgentEvent) chatReplyTarget {
	switch event.Type {
	case "napcat_private_message":
		return chatReplyTarget{Type: "private", ID: common.AsString(event.Data["userId"])}
	case "napcat_group_message":
		return chatReplyTarget{Type: "group", ID: common.AsString(event.Data["groupId"])}
	default:
		return chatReplyTarget{}
	}
}
