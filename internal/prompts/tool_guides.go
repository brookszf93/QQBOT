package prompts

import "strings"

// RootToolGuide returns the extra action guide injected into the main system prompt.
func RootToolGuide() string {
	return strings.Join([]string{
		"- 主循环直接暴露具体工具；每轮只调用一个工具。",
		"- 控制工具：wait 表示沉默并等待新事件；enter/back_to_portal/help 只用于进入、退出或查看个人工作台/工具环境，不用于 QQ 聊天和新闻。",
		"- QQ 群/私聊：决定发言时直接调用 send_message。message 必须非空；回复最新 QQ 消息所在会话时可省略 targetType/targetId，跨会话或回复非最新消息时必须显式填写。",
		"- 没有要发的内容、话题已经被别人说完、只是想总结或点评时，直接调用 wait，不要把 wait 写成普通文本。",
		"- 网页事实与链接读取：需要补充外部事实、读取网页链接或搜索资料时调用 search_web，参数 query；完整 URL 会优先直接读取页面，失败后再搜索。",
		"- 真实浏览器：需要动态网页、点击、输入、翻页、登录态复用、看直播或查看媒体状态时调用 browser，参数 task/url/sessionId。",
		"- 图片理解：需要看 QQ 图片、浏览器截图或本地受控图片时调用 analyze_image；它只返回识别结果，不会自动发消息。",
		"- 长期记忆：需要主动查找叙事记忆时调用 search_memory，参数 query；召回结果只作参考，不要当成刚发生的新消息复述。",
		"- IT之家：要阅读全文时调用 open_ithome_article，参数 articleId；看完想分享再调用 send_message。",
		"- 磁力搜索：只有用户明确请求磁力、种子或下载资源时才调用 searchMagnetFromWeb。",
		"- 做自己的事情时不必每一步都私聊汇报；只有用户正在等结果、动作完成值得说明，或确实有内容想分享时才 send_message。",
		"- 终端工具：bash/read_bash_output 只在终端能力可用且确实需要执行命令时使用。",
		"- 工具因为参数错误失败时，修正参数或调用 wait；不要原样重复同一个失败调用。",
	}, "\n")
}
