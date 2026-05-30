package story

import (
	"qqbot-ai/internal/db"
	"strings"
)

type storyContent struct {
	Title   string
	Time    string
	Scene   string
	People  []string
	Impact  string
	Cause   string
	Process []string
	Result  string
}

type memoryDocument struct {
	Kind    string
	Content string
}

func buildStoryMemoryDocuments(content storyContent) []memoryDocument {
	docs := []memoryDocument{
		{
			Kind: "overview",
			Content: joinNonEmpty([]string{
				"标题：" + content.Title,
				optionalLine("时间", content.Time),
				optionalLine("场景", content.Scene),
				optionalLine("起因", content.Cause),
				optionalLine("结果", content.Result),
				optionalLine("影响", content.Impact),
			}),
		},
		{
			Kind: "people_scene",
			Content: joinNonEmpty([]string{
				"标题：" + content.Title,
				optionalLine("时间", content.Time),
				optionalLine("场景", content.Scene),
				optionalLine("人物", strings.Join(content.People, "、")),
			}),
		},
		{
			Kind:    "process",
			Content: "标题：" + content.Title + "\n经过：\n- " + strings.Join(content.Process, "\n- "),
		},
	}
	out := docs[:0]
	for _, doc := range docs {
		if strings.TrimSpace(doc.Content) != "" {
			out = append(out, doc)
		}
	}
	return out
}

func parseStoryContent(item db.StoryItem) storyContent {
	content := storyContent{
		Title:  item.Title,
		Time:   item.Time,
		Scene:  item.Scene,
		People: item.People,
		Impact: item.Impact,
	}
	lines := strings.Split(strings.ReplaceAll(item.Markdown, "\r\n", "\n"), "\n")
	if content.Title == "" && len(lines) > 0 && strings.HasPrefix(lines[0], "# ") {
		content.Title = strings.TrimSpace(strings.TrimPrefix(lines[0], "# "))
	}
	inProcess := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "- 时间："):
			content.Time = strings.TrimSpace(strings.TrimPrefix(trimmed, "- 时间："))
		case strings.HasPrefix(trimmed, "- 场景："):
			content.Scene = strings.TrimSpace(strings.TrimPrefix(trimmed, "- 场景："))
		case strings.HasPrefix(trimmed, "- 人物："):
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "- 人物："))
			if value != "" {
				content.People = splitPeople(value)
			}
		case strings.HasPrefix(trimmed, "- 影响："):
			content.Impact = strings.TrimSpace(strings.TrimPrefix(trimmed, "- 影响："))
		case strings.HasPrefix(trimmed, "起因："):
			content.Cause = strings.TrimSpace(strings.TrimPrefix(trimmed, "起因："))
			inProcess = false
		case trimmed == "经过：":
			inProcess = true
		case strings.HasPrefix(trimmed, "结果："):
			content.Result = strings.TrimSpace(strings.TrimPrefix(trimmed, "结果："))
			inProcess = false
		case inProcess:
			if idx := strings.Index(trimmed, ". "); idx > 0 {
				content.Process = append(content.Process, strings.TrimSpace(trimmed[idx+2:]))
			}
		}
	}
	if content.Title == "" {
		content.Title = ExtractTitle(item.Markdown)
	}
	if len(content.Process) == 0 && strings.TrimSpace(item.Markdown) != "" {
		content.Process = []string{strings.TrimSpace(item.Markdown)}
	}
	return content
}

func optionalLine(label, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return label + "：" + value
}

func joinNonEmpty(lines []string) string {
	out := []string{}
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func splitPeople(value string) []string {
	parts := strings.Split(value, "、")
	out := []string{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
