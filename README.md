# qqbot-ai Go

qqbot-ai Go 不是一个单纯的“群聊回复机器人”。它是一个事件驱动的长期 Agent 运行时：QQ 群聊、私聊、新闻、定时唤醒、工具结果和长期记忆都会进入同一套生活流，由 Root Agent 判断要不要看、要不要说、要不要等待。

项目当前以 Go 版本为主，默认使用本地 SQLite 存储，不再依赖旧的 JSON store。

## 当前能力

- NapCat WebSocket 接入 QQ 群聊和私聊事件
- Root Agent 主循环：消费事件、维护上下文、调用工具、进入等待
- 门户/群聊/私聊/IT 之家/终端/神游等状态化上下文
- Story Agent：把线性聊天消息整理成长期叙事记忆
- Story RAG：支持 Gemini/Google embedding、TEI embedding 和关键词 fallback
- 记忆查询改写：`memoryQuery` 会把最近上下文改写为检索 query
- 图片摘要：收到图片时可调用 vision usage 生成中文描述
- 网页搜索：配置 Tavily 后可通过 `search_web` 搜索并摘要
- Terminal 能力：受限 shell 命令执行和输出读取
- LLM provider：DeepSeek、OpenAI Chat Completions、OpenAI Codex Responses、Claude Code
- HTTP 管理台和查询接口，默认端口 `20003`
- SQLite 运行数据、LLM 调用记录、NapCat 原始事件、群聊消息、Story、embedding cache 和指标图表

## 目录结构

```text
main.go                         程序入口
config.yaml                     运行配置
data/                           SQLite 数据库和运行数据
internal/agent                  Root/Story/WebSearch 运行链路
internal/agentruntime           ReAct kernel、通用工具定义、事件队列
internal/capabilities           messaging/news/story/terminal/vision/websearch 能力
internal/config                 YAML 配置结构和默认值
internal/db                     SQLite store、裁剪、压缩和快照
internal/embedding              Gemini/TEI embedding client 与向量工具
internal/llm                    LLM provider 适配、请求摘要、调用日志
internal/napcat                 NapCat WebSocket gateway
internal/news                   IT 之家 RSS 轮询
internal/ops                    HTTP 管理台和查询接口
internal/prompts                Root/Story prompt 和上下文渲染
internal/metric                 指标和图表数据
```

## 环境要求

- Go `1.26.2` 或相近版本
- 已运行 NapCat，并打开 WebSocket 服务
- 至少一个可用 LLM provider，或一个本地 OpenAI-compatible/Codex bridge
- 可选：Gemini API key，用于 Story embedding
- 可选：Tavily API key，用于网页搜索

## 快速启动

1. 修改 [config.yaml](config.yaml)。

最小关键配置大概长这样：

```yaml
server:
  port: 20003

  bot:
    qq: "机器人 QQ"
    creator:
      name: "创造者"
      qq: "你的 QQ"

  napcat:
    wsUrl: "ws://127.0.0.1:3001/ws"
    listenGroupIds:
      - "要监听的群号"

  agent:
    story:
      memory:
        embedding:
          provider: google
          apiKey: "你的 Gemini API Key"
          baseUrl: https://generativelanguage.googleapis.com
          model: gemini-embedding-2
          outputDimensionality: 768

  llm:
    timeoutMs: 45000
    providers:
      openaiCodex:
        apiKey: "sk-dummy"
        baseUrl: http://127.0.0.1:8317/v1/responses
        models:
          - gpt-5.4
    usages:
      agent:
        attempts:
          - provider: openai-codex
            model: gpt-5.4
      storyAgent:
        attempts:
          - provider: openai-codex
            model: gpt-5.4
      memoryQuery:
        attempts:
          - provider: openai-codex
            model: gpt-5.4
```

2. 启动：

```powershell
go run main.go
```

3. 打开管理台：

```text
http://localhost:20003
```

## 运行模型

主循环在 `internal/agent/runtime_root_loop.go`：

1. 从事件队列取出所有待处理事件。
2. 将 NapCat 消息、wake、跨状态通知等渲染成上下文。
3. 必要时触发 Root Agent 调用。
4. Root Agent 只能通过工具行动：`enter`、`back`、`wait`、`invoke` 等。
5. 成功发送消息、进入状态或等待后，当前轮结束，回到事件驱动等待。
6. 群聊/私聊消息会同步进入 Story ledger，Story Agent 按 batch 或 idle flush 处理长期记忆。

这套设计的目标是避免“定时空转式聊天”。没有新事件、没有自然切口时，模型应该选择 `wait`。

## 配置说明

配置文件启动时读取一次，修改后需要重启。

常用配置项：

- `server.port`：HTTP 服务端口。
- `server.databaseUrl`：目前保留字段；实际固定使用 `data/qqbot-ai-store.sqlite`。
- `server.bot.qq`：机器人自己的 QQ，用于过滤自己发出的消息和 prompt 身份。
- `server.bot.creator.qq`：创造者 QQ，Root Agent 会优先关注。
- `server.napcat.wsUrl`：NapCat WebSocket 地址。
- `server.napcat.listenGroupIds`：允许监听的群号白名单。
- `server.napcat.startupContextRecentMessageCount`：首次进入会话时补多少条最近消息。
- `server.agent.contextCompactionTotalTokenThreshold`：Root 上下文压缩阈值。
- `server.agent.llmRetryBackoffMs`：LLM 调用失败后的重试等待。
- `server.agent.waitToolMaxWaitMs`：模型请求等待时的最大等待时间。
- `server.agent.notificationBatchWindowMs`：跨状态通知聚合窗口。
- `server.agent.story.batchSize`：Story Agent 单批处理消息数。
- `server.agent.story.idleFlushMs`：Story Agent 空闲 flush 时间。
- `server.agent.story.memory.embedding`：Story embedding provider 配置。
- `server.agent.story.recall.topK`：`search_memory` 返回的 Story 数。
- `server.agent.story.recall.scoreThreshold`：向量召回最低相似度。
- `server.agent.terminal.*`：终端工具限制。
- `server.news.ithome.*`：IT 之家 RSS 轮询和正文长度限制。
- `server.llm.timeoutMs`：单次 LLM HTTP 请求超时。
- `server.llm.debugReasoning`：是否打印 provider 返回的 reasoning/summary 摘要。
- `server.llm.providers.*`：各 provider 的 key、base URL 和模型白名单。
- `server.llm.usages.*`：不同用途使用的 provider/model 调用链。
- `server.tavily.apiKey`：网页搜索 API key。

## LLM Provider

Go 版支持四类 provider：

- `deepseek`：OpenAI-compatible Chat Completions。
- `openai`：OpenAI-compatible Chat Completions，代码会拼接 `/chat/completions`。
- `openai-codex`：Responses API 风格，面向 Codex bridge，要求 SSE 中出现 `response.completed`。
- `claude-code`：Claude Code bridge。

`server.llm.usages` 按用途分配模型：

- `agent`：主 Root Agent，负责状态切换、群聊回复和工具决策。
- `storyAgent`：长期 Story 归并和改写。
- `contextSummarizer`：上下文压缩。
- `memoryQuery`：长期记忆检索前的 query 改写。
- `vision`：图片摘要。
- `webSearchAgent`：搜索子任务。当前直接搜索路径通常不触发它。

如果 provider 报错，可以从管理台或接口查看 `llm_calls`，其中会保存压缩后的请求摘要、原生请求摘要、响应摘要和错误信息。

## Story 和 RAG

Story Agent 不是复述聊天记录，而是把消息归并成长期叙事对象。它从 `story_ledger` 读取新消息，调用 `storyAgent` usage，使用工具创建或重写 Story。

每条 Story 会生成多种 memory document：

- `overview`
- `people_scene`
- `process`

召回流程：

1. `memoryQuery` 根据最近上下文生成检索 query。
2. 优先使用 embedding 做向量召回。
3. embedding 不可用或失败时，回退到关键词匹配。
4. 召回结果作为 `search_memory` 工具结果进入 Root 上下文。

当前推荐 Gemini embedding：

```yaml
server:
  agent:
    story:
      memory:
        embedding:
          provider: google
          apiKey: "你的 Gemini API Key"
          baseUrl: https://generativelanguage.googleapis.com
          model: gemini-embedding-2
          outputDimensionality: 768
```

`gemini-embedding-2` 不发送旧的 `taskType` 字段；代码会按用途给文本加前缀，例如检索 query 使用 `task: search result | query: ...`，文档使用 `title: none | text: ...`。

仍保留 TEI provider：

```yaml
provider: tei-embedding-gemma
baseUrl: http://127.0.0.1:20008
model: google/embeddinggemma-300m
outputDimensionality: 768
```

TEI 路径会调用 `POST /embed`，请求体为 `{"inputs":"文本"}`。

## SQLite 数据

默认数据库：

```text
data/qqbot-ai-store.sqlite
```

主要表：

- `app_logs`：应用日志。
- `llm_calls`：LLM 调用记录。
- `napcat_events`：NapCat 原始事件。
- `napcat_messages`：标准化后的 QQ 消息。
- `story_ledger`：Story Agent 消费的线性消息账本。
- `stories`：长期 Story。
- `story_documents`：Story 的向量化文档。
- `embedding_cache`：embedding 结果缓存。
- `metrics` / `metric_charts`：指标和图表。
- `news_articles` / `news_feed_cursors`：新闻文章和游标。
- `agent_snapshots`：Root/Story 运行时快照。

Store 会在启动时执行维护：

- SQLite WAL checkpoint/truncate。
- 裁剪 LLM、NapCat、日志、指标等表的历史数量。
- 压缩过大的 LLM payload。
- 必要时执行 `VACUUM`。

## NapCat

连接成功会看到：

```text
NapCat websocket connected
```

被白名单接收的消息会看到：

```text
NapCat message accepted
```

常见排查：

- `wsUrl` 是否和 NapCat 实际 WebSocket 地址一致。
- NapCat 是否启用了事件上报。
- 机器人 QQ 是否在群里。
- 群号是否在 `listenGroupIds`。
- 图片摘要是否过于频繁触发 vision usage。

## HTTP 接口

常用接口：

```text
GET  /health
GET  /agent-dashboard/current
GET  /llm/providers
GET  /llm/playground-tools
POST /llm/chat
POST /napcat/group/send
POST /napcat/private/send
GET  /app-log/query
GET  /llm-chat-call/query
GET  /napcat-event/query
GET  /napcat-group-message/query
GET  /story/query
POST /story/reindex
GET  /metric-chart/list
GET  /metric-chart/data
POST /metric-chart/create
POST /metric-chart/delete
```

## 验证

```powershell
go test ./...
```

如果 Windows 默认 Go cache 没权限，可以临时指定到项目内：

```powershell
$env:GOCACHE='D:\goGroup\workspace\qqbot-ai - ai\.gocache'
$env:GOMODCACHE='D:\goGroup\workspace\qqbot-ai - ai\.gomodcache'
go test ./...
```
