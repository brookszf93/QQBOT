# QQBOT Go

QQBOT Go 是一个事件驱动的长期 Agent 运行时。它不只是 QQ 自动回复：QQ 群聊、私聊、新闻、节奏唤醒、工具结果、Story 召回和个人工作台都会进入同一条生活流，由 Root Agent 判断要不要看、要不要说、要不要写点自己的东西，或者安静等待。

当前版本以 Go 项目为准，默认使用本地 SQLite 存储，运行数据落在 `data/` 下。

## 当前能力

- NapCat WebSocket 接入 QQ 群聊和私聊，支持图片、语音、视频等消息理解入口。
- Root Agent 直接暴露具体工具，不再依赖旧版多层代理工具包装；每轮只执行一个工具。
- `wait`、`send_message`、`search_web`、`browser`、`analyze_image`、`search_memory` 等工具按需使用。
- ToolGuard 会拦住无信息增量的重复 screen/list/search/browser、重复错误调用和个人工作台空转。
- AI 腔调检测：`send_message` 前会记录概率，超过阈值时不发送并要求重写。
- RhythmScheduler：空闲时稳定触发 `continue`、`creative`、`review`、`news`、`quiet` 等节奏信号，让 Agent 有机会继续未完成事务、写随笔、整理项目或看新闻。
- Story Agent：按批次把 QQ 消息整理成长期叙事记忆，并支持向量召回和关键词 fallback。
- 个人工作台：todo、小说/随笔、项目、音乐、新闻、活动记录、文件工作区都作为 Agent 自己的地方。
- 真实浏览器：通过 CloakBrowser sidecar 完成搜索、打开网页、点击、输入、翻页、截图和登录态复用。
- 网页搜索：`search_web` 会优先直接读取完整 URL，失败后再走 Tavily 搜索。
- 磁力搜索：当前只保留 TokyoLib 直接搜索。
- LLM provider：DeepSeek、OpenAI、LongCat、OpenAI Codex Responses、Claude Code、Google Gemini。
- HTTP 管理台：查看上下文、日志、LLM 调用、Story、指标、调度任务、认证额度、个人随笔和工作台。

## 目录结构

```text
main.go                         程序入口
config.yaml                     本地运行配置，包含密钥，不提交
config.yaml.example             可公开的配置模板
data/                           SQLite 数据库、浏览器截图、个人工作台文件
tools/cloakbrowser-sidecar      浏览器 sidecar
internal/agent                  Root Agent、Story Agent、节奏唤醒、ToolGuard
internal/agentruntime           工具定义、事件队列和通用运行结构
internal/capabilities           QQ、浏览器、搜索、Story、个人工作台、终端等能力
internal/config                 YAML 配置结构和默认值
internal/db                     SQLite store、上下文、日志、指标和 Story 存储
internal/embedding              Google/TEI embedding client
internal/llm                    LLM provider 适配、调用日志和失败重试
internal/napcat                 NapCat WebSocket gateway
internal/news                   IT 之家 RSS 轮询
internal/ops                    HTTP 管理台和查询接口
internal/prompts                Root/Story prompt 和上下文渲染
```

## 环境要求

- Go `1.24.3` 或相近版本。
- 已运行 NapCat，并打开 WebSocket。
- 至少配置一个可用 LLM provider。
- 可选：Google Gemini API key，用于图片/音频/视频理解和 Story embedding。
- 可选：Tavily API key，用于网页搜索。
- 可选：Node.js 20+，用于启动 CloakBrowser sidecar。

## 快速启动

1. 复制配置模板：

```powershell
Copy-Item config.yaml.example config.yaml
```

2. 修改 `config.yaml`：

- `server.bot.qq`：机器人 QQ。
- `server.bot.creator.qq`：创造者 QQ。
- `server.napcat.wsUrl`：NapCat WebSocket 地址。
- `server.napcat.listenGroupIds`：允许监听的群号。
- `server.llm.providers.*.apiKey`：至少填一个可用 provider。
- `server.llm.usages.agent`、`storyAgent`、`contextSummarizer`：按你实际可用模型选择。

3. 可选启动浏览器 sidecar：

```powershell
cd tools\cloakbrowser-sidecar
npm.cmd install
npm.cmd start
```

PowerShell 如果拦截 `npm.ps1`，使用 `npm.cmd`。

4. 启动 Go 服务：

```powershell
go run main.go
```

5. 打开管理台：

```text
http://localhost:20003
```

## 运行模型

主循环会把 QQ、新闻、Story 召回、工具结果和节奏唤醒整理成“本轮信号”，再决定是否调用 Root Agent。Root Agent 看到的是当前最重要的上下文，而不是把所有事件一股脑塞进去。

Root Agent 当前直接使用具体工具：

- `wait`：沉默并等待新事件。
- `send_message`：发送 QQ 消息。回复最新 QQ 会话时可省略目标，跨会话必须填 `targetType` 和 `targetId`。
- `search_web`：补充事实、搜索或读取 URL。
- `browser`：需要真实网页交互、登录态、点击、翻页、截图时使用。
- `analyze_image`：理解 QQ 图片、浏览器截图或本地受控图片。
- `search_memory`：主动召回长期 Story 记忆。
- `open_ithome_article`：阅读 IT 之家文章。
- `searchMagnetFromWeb`：用户明确请求磁力、种子或下载资源时使用。
- `personal_screen`、`workspace_app`、`activity_app`、`todo_app`、`novel_app`、`project_app`、`music_app`、`news_app`：个人工作台。
- `bash`、`read_bash_output`：终端能力，按配置启用。

普通文本输出不会自动发送到 QQ。要发消息必须调用 `send_message`。

## 自主节奏

`server.agent.autonomous` 控制空闲后的自驱行为。没有 QQ、新闻或任务事件时，系统会在 `idleDelayMs` 后触发节奏信号：

- `continue`：继续刚刚未完成的活动。
- `creative`：写随笔、灵感或长草稿。
- `review`：整理 todo、项目、音乐、新闻和文件工作台。
- `news`：有新闻可读时提醒阅读。
- `quiet`：没有自然动作时保持安静。

这不是死循环定时聊天。它只提供“可以做什么”的信号，最终仍由模型选择工具或 `wait`。

## 个人工作台

个人工作台默认目录：

```text
data/personal-apps
```

其中 `workspace_app` 会直接写入：

```text
data/personal-apps/journal
data/personal-apps/drafts
data/personal-apps/reading
data/personal-apps/music
data/personal-apps/scratchpad.md
```

推荐模型写随笔、灵感、阅读摘记、听歌记录时直接调用：

```text
workspace_app(action="write", kind="journal|drafts|reading|music", title="...", text="...")
```

管理台的“随笔/工作台”页面会读取 `/personal-apps/novel` 和 `/personal-apps/workspace`。

## 浏览器和网页搜索

`search_web` 用于事实搜索和 URL 直读。传入完整 URL 时会优先直接访问页面，失败后再搜索。

`browser` 用于需要真实浏览器状态的任务，例如：

- 打开网页并点击。
- 登录态复用。
- 查看下一页。
- 看直播或动态内容。
- 截图后让模型选择识图或发图。

浏览器截图会保存到 `data/browser-screenshots`，不会把 Base64 塞进上下文。

## Story 和 RAG

Story Agent 会从 QQ 消息 ledger 中按批处理消息，产出长期叙事记忆。召回时优先使用 embedding，相似度不足或 embedding 不可用时回退关键词。

推荐 embedding 配置：

```yaml
server:
  agent:
    story:
      memory:
        embedding:
          provider: google
          apiKey: ""
          baseUrl: https://generativelanguage.googleapis.com
          model: gemini-embedding-2
          outputDimensionality: 768
        retrieval:
          topK: 3
```

`gemini-embedding-2` 使用文本前缀表达检索任务，不再发送旧的 `taskType` 字段。

## 配置重点

配置启动时读取一次，修改后需要重启。

- `server.port`：HTTP 服务和管理台端口。
- `server.databaseUrl`：保留字段；当前实际使用本地 SQLite。
- `server.bot.*`：机器人身份和创造者信息。
- `server.napcat.*`：NapCat WebSocket 和监听群白名单。
- `server.browser.*`：CloakBrowser sidecar。
- `server.agent.contextCompactionTotalTokenThreshold`：Root 上下文压缩阈值。
- `server.agent.cacheKeepaliveEnabled`：是否额外续缓存；默认建议关闭。
- `server.agent.autonomous.*`：空闲自驱和节奏唤醒。
- `server.agent.story.*`：Story 批处理、压缩、embedding 和召回。
- `server.magnetSearch.*`：TokyoLib 磁力搜索。
- `server.llm.providers.*`：各 provider API key、base URL、模型白名单。
- `server.llm.usages.*`：不同用途的模型调用链。
- `server.tavily.apiKey`：网页搜索 API key。

## HTTP 接口

常用接口：

- `/`：管理台。
- `/health`：健康检查。
- `/agent-dashboard/current`：Root 当前状态、上下文、节奏信号和 ToolGuard。
- `/agent-dashboard/reset-persisted-state`：清除持久 Root 上下文状态，不删除 Story。
- `/app-log/query`：应用日志。
- `/llm-chat-call/query`：LLM 调用记录。
- `/napcat-event/query`、`/napcat-group-message/query`：NapCat 原始事件和消息。
- `/story/query`、`/story/reindex`：Story 查询和重建索引。
- `/personal-apps/novel`、`/personal-apps/workspace`：小说/随笔和文件工作台。
- `/scheduler/tasks`：调度任务状态。
- `/auth/{provider}/status`、`/auth/{provider}/usage-limits`：OAuth 和额度状态。
- `/metric-chart/list`、`/metric-chart/data`：指标图表。

## 常见问题

- `LongCat EOF` 或 `DeepSeek EOF/TLS timeout`：通常是网络或供应商连接问题，换 provider、重试或检查代理。
- `Google 503 high demand`：Gemini 模型高负载，vision usage 可配置 `gemini-2.5-flash` 兜底。
- 图片 URL 400：NapCat 临时 URL 失效，需要依赖 `get_image` 返回的本地文件路径。
- 浏览器不可用：确认 `tools\cloakbrowser-sidecar` 已启动，`server.browser.baseUrl` 指向 `http://127.0.0.1:20009`。
- 重复 screen/list/search：ToolGuard 会要求改参数、写入、结束活动或 `wait`。
- 清除上下文不会删除 Story；长期记忆仍在 SQLite 中。

## 验证

```powershell
go test ./...
```
