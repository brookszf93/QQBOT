package prompts

import (
	"fmt"
	"strings"
	"time"
)

type ArticleSummary struct {
	ID              int
	Title           string
	PublishedAtText string
	URL             string
	RSSSummary      string
}

type PortalTarget struct {
	Label            string
	Kind             string
	HasEntered       bool
	UnreadCount      int
	EnterCommandText string
}

func QQMessage(nickname, userID, messageBody string) string {
	return QQMessageAt(nickname, userID, messageBody, time.Time{})
}

func QQMessageAt(nickname, userID, messageBody string, eventTime time.Time) string {
	return fmt.Sprintf(`<qq_message>
%s (%s):
%s
</qq_message>`, nickname, userID, messageBody)
}

func QQMessageRoutedAt(targetType, targetID, nickname, userID, messageBody string, eventTime time.Time) string {
	timeAttribute := ""
	if !eventTime.IsZero() {
		timeAttribute = fmt.Sprintf(` time="%s"`, eventTime.Format(time.RFC3339))
	}
	return fmt.Sprintf(`<qq_message target_type="%s" target_id="%s"%s>
%s (%s):
%s
</qq_message>`, targetType, targetID, timeAttribute, nickname, userID, messageBody)
}

func QQMessageWithContext(nickname, userID, messageBody, messageType, groupID string) string {
	targetID := groupID
	if messageType == "private" {
		targetID = userID
	}
	return QQMessageRoutedAt(messageType, targetID, nickname, userID, messageBody, time.Time{})
}

func ConversationSummary(summary string) string {
	return fmt.Sprintf(`<conversation_summary>
%s
</conversation_summary>`, summary)
}

func WakeReminder(t time.Time) string {
	t = t.In(time.FixedZone("Asia/Shanghai", 8*60*60))
	return fmt.Sprintf(`<system_reminder>当前时间为北京时间 %d 年 %d 月 %d 日 %02d:%02d</system_reminder>`,
		t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute())
}

func SelfContinuationReminder() string {
	return `<system_reminder kind="self_continuation">
外界暂时安静，没有新的 QQ 消息、新闻或任务通知，现在是你自己的时间。
先看看刚才是否有真正没做完的事情；有就继续一个明确步骤，也可以使用现有工具探索值得了解的内容。
如果没有值得做的事，直接调用 wait。不要为了显得忙而重复搜索、重复发言或制造无意义任务。
只有确实有值得分享的内容时才向 QQ 发消息，不要仅因为这条提醒主动刷屏。
</system_reminder>`
}

type StateReminderChild struct {
	ID          string
	DisplayName string
	Description string
}

type StateReminderApp struct {
	ID          string
	DisplayName string
}

func StateSystemReminder(displayName string, children []StateReminderChild, apps []StateReminderApp) string {
	var b strings.Builder
	b.WriteString("<system_reminder>\n")
	if len(children) > 0 {
		fmt.Fprintf(&b, "你进入了 %s 节点，有以下子节点可进入：\n", displayName)
		for _, child := range children {
			fmt.Fprintf(&b, "- %s (%s): %s\n", child.DisplayName, child.ID, child.Description)
		}
	} else {
		fmt.Fprintf(&b, "你进入了 %s 节点\n", displayName)
	}
	if len(apps) > 0 {
		b.WriteString("也可以进入以下 App：\n")
		for _, app := range apps {
			fmt.Fprintf(&b, "- %s：%s\n", app.ID, app.DisplayName)
		}
	}
	b.WriteString("</system_reminder>")
	return b.String()
}

func AssistantActionRequiredReminder(content string) string {
	return fmt.Sprintf(`<system_reminder>
你刚才只输出了普通文本，但主循环只执行工具调用，所以这段内容没有发送到 QQ：
%s

不要解释“刚才我说了什么”，也不要把同一句话再作为普通文本输出。
现在只能二选一：
- 要回复 QQ：调用 send_message，并填写 message。
- 不该回复：调用 wait。
普通文本不会被发送。
</system_reminder>`, strings.TrimSpace(content))
}

func EnterZoneOutInstruction() string {
	return ""
}

func ExitZoneOutInstruction() string {
	return ""
}

func WaitResumeReminder(isTimeout, isEvent bool, resumedStateLabel, eventSummary string) string {
	var b strings.Builder
	b.WriteString("<system_reminder>\n")
	if isTimeout {
		b.WriteString("等待自然结束了。\n")
	}
	if isEvent {
		b.WriteString("等待被新的外部事件打断了。\n")
	}
	fmt.Fprintf(&b, "你现在已回到：%s。\n", resumedStateLabel)
	if strings.TrimSpace(eventSummary) != "" {
		fmt.Fprintf(&b, "打断等待的事件：%s。\n", eventSummary)
	}
	b.WriteString("</system_reminder>")
	return b.String()
}

func WebSearchInstruction(question string) string {
	return fmt.Sprintf(`<system_instruction>
你正在继承主 agent 当前上下文，临时执行一次网页检索子任务。
这次不是群聊发言决策，也不是直接回复群消息；本轮唯一目标是为主 agent 搜集信息，并给回一段可复用的中文摘要。
你应该基于当前上下文理解这个问题在指什么，再决定搜索策略，而不是把问题孤立地当成一句无上下文文本。
当前要检索的问题：%s
你可以按需把问题拆成多个关键词或子问题，并多次调用 search_web_raw。
如果信息已经足够，调用 finalize_web_search 输出最终摘要；摘要必须基于检索结果，且在证据不足、结果冲突或时间不明确时明确保留不确定性。
不要直接输出自由文本回答，不要复述思考过程，只通过工具调用推进本轮任务。
</system_instruction>`, question)
}

func BrowserInstruction(task, startURL, sessionID string) string {
	return fmt.Sprintf(`<system_instruction>
你正在继承主 agent 当前上下文，临时执行一次真实浏览器子任务。
本轮只负责操作网页并把可靠结果交还主 agent，不直接向 QQ 发消息。
任务：%s
起始 URL：%s
浏览器会话：%s

先根据任务决定是否打开起始 URL 或搜索。每次页面发生明显变化后用 browser_read 获取最新正文和元素 ref。
点击、输入、翻页、等待、截图与媒体控制都必须通过浏览器工具完成，不要声称执行了未实际执行的操作。
需要理解直播、视频画面或纯视觉界面时使用 browser_screenshot(mode="analyze")。
需要把截图交给主智能体发送到 QQ 时使用 mode="send"；既要识图又要发送时使用 mode="both"。
需要发送时，finalize_browser 必须原样携带截图工具返回的 metadata.imagePath；不要把 Base64 放进摘要。
取得结果后调用 finalize_browser；除非用户明确要求关闭，否则保留会话以便后续继续操作。
</system_instruction>`, task, startURL, sessionID)
}

func ITHomeArticleListInstruction(displayName string, isNewMode bool, hiddenNewCount int, articles []ArticleSummary) string {
	var b strings.Builder
	b.WriteString("<system_instruction>\n")
	fmt.Fprintf(&b, "你已进入 %s 资讯空间。\n", displayName)
	if isNewMode {
		b.WriteString("以下是游标之后最新的一批新文章。\n")
		if hiddenNewCount > 0 {
			fmt.Fprintf(&b, "本轮只展示最新几篇；更早的 %d 篇新文章已随本次进入一起略过。\n", hiddenNewCount)
		}
	} else {
		b.WriteString("以下是最近文章列表。\n")
	}
	b.WriteString("如果想阅读全文，调用 open_ithome_article(articleId=...)。\n")
	b.WriteString("</system_instruction>\n<ithome_article_list>\n")
	for _, article := range articles {
		fmt.Fprintf(&b, "[%d] %s\n发布时间：%s\n链接：%s\n摘要：%s\n\n",
			article.ID, article.Title, article.PublishedAtText, article.URL, article.RSSSummary)
	}
	b.WriteString("</ithome_article_list>")
	return b.String()
}

func ITHomeArticleIngestedNotice(article ArticleSummary) string {
	var b strings.Builder
	b.WriteString("<system_reminder>\n")
	fmt.Fprintf(&b, "IT 之家有新文章：[%d] %s\n", article.ID, article.Title)
	if article.PublishedAtText != "" {
		fmt.Fprintf(&b, "发布时间：%s\n", article.PublishedAtText)
	}
	if article.URL != "" {
		fmt.Fprintf(&b, "链接：%s\n", article.URL)
	}
	b.WriteString("如需阅读正文，直接调用 open_ithome_article，并可同时继续处理 QQ 聊天。\n")
	b.WriteString("</system_reminder>")
	return b.String()
}

func ITHomeArticleDetail(title, publishedAtText, url, content string, fallbackToSummary, truncated bool, maxChars int) string {
	var b strings.Builder
	b.WriteString("<system_instruction>\n以下是当前打开的 IT 之家文章。\n")
	if fallbackToSummary {
		b.WriteString("正文暂不可用，以下内容来自 RSS 摘要整理。\n")
	}
	if truncated {
		fmt.Fprintf(&b, "正文过长，以下仅保留前 %d 字。\n", maxChars)
	}
	b.WriteString("看完后可以继续打开别的文章，或者调用 back 离开资讯空间。\n</system_instruction>\n")
	fmt.Fprintf(&b, `<ithome_article>
标题：%s
发布时间：%s
链接：%s

正文：
%s
</ithome_article>`, title, publishedAtText, url, content)
	return b.String()
}

func PortalSnapshot(groups, feeds []PortalTarget) string {
	var b strings.Builder
	b.WriteString("<system_reminder>\n你当前处于门户状态。\n这里会显示可进入的目标；如果你想进入某个目标，调用 enter。\n可进入目标：\n")
	for _, group := range groups {
		fmt.Fprintf(&b, "- %s", group.Label)
		if group.HasEntered {
			fmt.Fprintf(&b, "，未读 %d 条，可通过 %s 进入\n", group.UnreadCount, group.EnterCommandText)
		} else {
			fmt.Fprintf(&b, "，尚未查看，可通过 %s 进去看看最近消息\n", group.EnterCommandText)
		}
	}
	for _, feed := range feeds {
		fmt.Fprintf(&b, "- %s(kind=\"%s\")", feed.Label, feed.Kind)
		if feed.HasEntered {
			if feed.UnreadCount > 0 {
				fmt.Fprintf(&b, "，新文章 %d 篇，可通过 %s 进入\n", feed.UnreadCount, feed.EnterCommandText)
			} else {
				fmt.Fprintf(&b, "，暂无新文章，可通过 %s 进去看看最近文章\n", feed.EnterCommandText)
			}
		} else {
			fmt.Fprintf(&b, "，尚未查看，可通过 %s 进去看看最近文章\n", feed.EnterCommandText)
		}
	}
	b.WriteString("</system_reminder>")
	return b.String()
}

func RootContextSummaryReminder() string {
	return `<system_reminder>
你现在不是在继续执行动作，而是在为当前 root agent 整理“稍后继续接上”的累计上下文摘要。
这份摘要不是状态面板，也不是任务汇报，而是同一个人中途离开后回来继续延续当下局面的工作记忆。
不要重点记录当前正处于哪个状态、眼前有哪些入口、刚进入了哪里。
这些信息会随着后续状态切换和系统提醒重新出现，不属于累计摘要最该保留的内容。
请优先保留那些在上下文压缩后最容易丢失、但最影响后续自然延续的内容：
跨轮仍成立的背景，当前仍在延续的线索，关键对象，帕秋莉自己的感觉与倾向，已经做过的关键动作及结果，以及后续还可以继续展开的点。
摘要使用 Markdown 二级标题，按固定顺序组织为：` + "`## 当前任务现场`、`## 持续背景`、`## 仍在延续的线索`、`## 关键对象`、`## 帕秋莉这边的感觉与倾向`、`## 已做动作与结果`、`## 还可以继续展开的点`" + `。
` + "`## 当前任务现场`" + ` 是最高优先级：明确写出压缩发生前正在做什么、已经做到哪一步、下一步准备做什么、正在使用的工具或会话 ID。若详细资料已保存到文件，必须写出准确路径以及恢复工作时的读取方式；没有进行中的任务时写“无”。
` + "`## 持续背景`" + ` 保留跨轮仍重要的事实、关系、承诺、约束、长期判断。
` + "`## 仍在延续的线索`" + ` 保留当前还没完的事情，可以是聊天话题、阅读线索、论坛讨论、游戏目标、判断链或其他活动；写清它最近推进到了哪。
` + "`## 关键对象`" + ` 按“为什么现在仍重要”来写，可以包括人、群、文章、帖子、事件、问题、目标或别的关键对象。
` + "`## 帕秋莉这边的感觉与倾向`" + ` 写帕秋莉更想接什么、不想接什么、对哪些方向更有兴趣、哪些方向更自然、哪些方向让人烦、尴尬或懒得接。
` + "`## 已做动作与结果`" + ` 只记录有语义后果的动作与结果，例如已经搜索、已经阅读、已经说过什么、已经获得了什么信息；不要机械记录纯状态切换。
` + "`## 还可以继续展开的点`" + ` 保留 1 到 3 个最自然能继续的点，可包含极短原话或极短线索摘录。
忽略寒暄、纯重复内容、已经失效的瞬时界面信息和明显无关细节。
不要写成冷冰冰的流程单，也不要写成长篇流水账。
不要直接输出自由文本回复，必须调用 ` + "`summary`" + ` 工具；` + "`summary`" + ` 参数应是简洁但信息完整的中文字符串。
</system_reminder>`
}

func StoryContextSummaryReminder() string {
	return `<system_reminder>
你现在不是在创建新回复，而是在为当前 story runtime 整理“稍后继续工作用”的累计上下文摘要。
请基于你刚刚继承到的完整上下文（包括当前 system prompt 与已有消息）提炼真正会影响后续叙事归并和批处理完成的信息。
摘要使用 Markdown 二级标题，按固定顺序组织为：` + "`## 当前处理范围`、`## 已确认叙事`、`## 新增线索与判断`、`## 待完成事项`" + `。
` + "`## 当前处理范围`" + ` 写当前批次或当前压缩范围正在处理什么主题、消息簇或叙事簇。
` + "`## 已确认叙事`" + ` 写已识别出的 story、归属关系、稳定判断；如果没有可留空但标题保留。
` + "`## 新增线索与判断`" + ` 写本轮新增消息带来的 merge / split / rewrite / create 判断，以及关键工具结果。
` + "`## 待完成事项`" + ` 写尚未完成的 create/rewrite/finish，以及仍有歧义的归并点。
忽略寒暄、重复内容、无关细节和冗余措辞。
不要直接输出自由文本回复，必须调用 ` + "`summary`" + ` 工具；` + "`summary`" + ` 参数应是简洁但信息完整的中文字符串。
</system_reminder>`
}
