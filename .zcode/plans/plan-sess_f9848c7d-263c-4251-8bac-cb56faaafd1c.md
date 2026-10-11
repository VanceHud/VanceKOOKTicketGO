# VanceKOOKTicketGO AI 化改造方案

**目标能力**（一步到位）：AI 自动应答、工单智能摘要与分类、从人工客服回复中持续学习的知识闭环。LLM 接入采用 **OpenAI 兼容 API**（一套协议通吃 DeepSeek / GLM / Kimi / Qwen / OpenAI，BaseURL + Key + 模型名可随时在 WebUI 切换）。

## 总体架构

```
internal/llm/      新增：OpenAI 兼容客户端（chat/completions + embeddings），限速/重试/超时
internal/llm/llmtest/  新增：httptest 模拟 LLM 服务（对标 internal/kook/kooktest 的用法）
internal/ai/       新增：AI 业务编排（自动应答防抖、摘要、分类、知识捕获与检索、工人池）
internal/store/    扩展：AI 配置仓储、知识库表、工单/消息新增字段、调用量记录
internal/bot/      扩展：消息钩子、AI 发送通道、/tkai 命令、关单时触发摘要
internal/api/      扩展：AI 设置/知识库/摘要/分类路由
web/frontend       扩展：AI 设置页、知识库页、工单详情 AI 区块、类型页开关
```

依赖方向：`bot → ai → llm / store`，`ai` 不直接依赖 `kook`（通过注入的 Sender 接口发消息），保持与现有 `ticket.Platform` 抽象一致的分层风格。

## 1. 数据模型（internal/store/models.go + 新仓储）

- **Ticket 新增字段**：`AIState`（`""` 未启用 / `active` AI 应答中 / `takeover` 人工已接管）、`AISummary text`、`AISummaryAt *time.Time`、`AIClassification text`（JSON：建议类型 + 理由）。GORM AutoMigrate 自动加列，存量数据零影响。
- **TicketMessage 新增字段**：`IsAI bool`。AI 回复由机器人发出但**显式入库**（复用 `AddBotMessage` 模式，flows.go:340），时间线可渲染「AI」徽章。
- **新表 AIKnowledge**（学习闭环的存储）：`Question text`、`Answer text`、`Source`（ticket/manual）、`TicketNo`、`AnswerMsgID`（去重）、`IsCorrection bool`（该条是对 AI 回复的人工纠正，检索时加权）、`Embedding blob`（可选）、`Enabled`、时间戳。
- **新表 AICall**（用量与排障）：`TicketNo`、`Kind`（reply/summary/classify/test）、`Model`、`PromptTokens`、`CompletionTokens`、`DurationMs`、`OK`、`CreatedAt`；随现有每日清理任务按保留期删除（默认 90 天）。
- **TicketType 新增字段**：`AIAutoReply bool`（按类型开关自动应答，默认关闭）。

## 2. LLM 客户端与配置（internal/llm + repo_settings 模式）

- 客户端：`Chat(ctx, ChatRequest)` / `Embed(ctx, []string)` / `Test(ctx)`；默认 30s 超时、429/5xx 指数退避重试 2 次、内置令牌桶限速（默认 1 req/s 可调）；日志风格对齐 `internal/kook`（**API Key 绝不进日志**）。
- 配置存储沿用 KOOK Token 的成熟模式：**API Key 用 AES-GCM 加密入库**（`SetSecret`，repo_settings.go:158），接口只回掩码 `****a1b2`；其余项（BaseURL、模型名、温度、系统提示词模板、防抖窗口、每单回复上限、上下文条数、知识条数 TopK、关单自动摘要开关）存 settings 键值。
- 环境变量首启装载（对齐 `KOOK_TOKEN` 的 bootstrap 模式）：`LLM_BASE_URL`、`LLM_API_KEY`、`LLM_MODEL`、`LLM_EMBEDDING_MODEL`（可选，留空则知识检索退化为关键词匹配）。
- **全局总开关默认关闭** + 每类型默认关闭 → 灰度安全上线。

## 3. AI 自动应答（核心流程）

钩子点：`archiveMessage`（flows.go:486）归档成功后调用 `ai.OnUserMessage / ai.OnStaffMessage`：

- **防抖**：开单人发消息后启动静默窗口计时器（默认 5s，可配），窗口内续发消息则重置；静默后把「工单信息 + 最近 N 条消息（默认 20）+ 检索到的知识示例」组装进提示词，经工人池（有界队列 + 固定 worker，复用 enrichJobs 模式，**绝不阻塞事件分片协程**）调用 LLM。
- **发送**：经注入的 Sender 接口发 KMarkdown 到工单频道（带可配置「AI 自动回复」尾注），同时以 `IsAI=true` 入库并走 eventbus 推送 WebUI。
- **人工接管**：管理员（`isTicketAdmin` 命中且非开单人）在频道发言 → 取消待发计时器、`AIState=takeover`，AI 不再自动回复该单；`/tkai on|off` 命令（commands.go 的 handleCommand 分发中新增，仅管理员）可手动控制；重新激活工单时按类型开关重置。
- **护栏**：每单回复上限（默认 10）、LLM 全局限速、超时、失败仅记日志不骚扰用户、上下文截断。

## 4. 学习闭环（从人工回复中自我改进）

- **捕获**：管理员在工单内回复时，把「上一条管理员/AI 消息之后开单人的连续消息」作为 Question、该条回复作为 Answer，异步写入 AIKnowledge（按 TicketNo+AnswerMsgID 幂等去重）；若该回复出现在 AI 回复之后，标记 `IsCorrection=true`（纠正信号，检索加权）。
- **检索**：生成回复时用近期用户消息作为查询——配置了 Embedding 模型则对全部知识条目做余弦相似度（内存缓存，写入时刷新；条目量级小，暴力扫描足够），否则退化为 SQLite LIKE 关键词匹配（复用现有通配符转义工具）；TopK（默认 5）作为 few-shot 示例注入提示词。
- **WebUI 知识库页**：列表/编辑/启停/删除/手动新增 FAQ，附「检索测试」预览命中效果。

## 5. 智能摘要与分类

- **摘要**：工单详情页「生成/刷新摘要」按钮（客服及以上）→ 异步任务调 LLM → 存入 `AISummary` 并经 SSE 事件通知前端刷新；关单时按设置自动生成（关单流程异步触发，不阻塞关单卡片发送）；摘要展示于详情页并纳入 JSON/CSV/HTML 导出。
- **分类**：首次自动应答时让 LLM 单次调用同时返回 `{"reply", "suggested_type", "type_reason"}`（JSON 解析失败则整段视为回复，绝不影响应答主链路）；建议存入 `AIClassification`，详情页以建议条 + 「应用」按钮呈现——应用仅改工单的类型标签（TypeID/TypeName 快照），**不追溯变更频道权限**（权限以开单时下发为准，页面注明）；也提供手动触发分类的接口。

## 6. API 与 WebUI

- 路由（router.go，沿用现有 RBAC 分层）：
  - 管理员：`GET/PUT /ai/settings`、`POST /ai/test`（连通性测试）、`GET/POST/PATCH/DELETE /ai/knowledge[/:id]`
  - 客服及以上：`POST /tickets/:no/ai/summary`、`POST /tickets/:no/ai/classify`、`POST /tickets/:no/ai/classify/apply`、`POST /tickets/:no/ai/toggle`
  - 只读：摘要与 AI 状态随工单详情接口返回
  - 全部写操作进审计日志（不含 Key 与提示词全文）
- 前端（React 19 + shadcn/ui，沿用现有页面模式）：
  - 新页面 `AISettings.tsx`（连接配置 + 掩码 Key + 测试按钮 + 提示词模板 + 护栏参数 + 调用量概览）、`Knowledge.tsx`（知识库管理）
  - `TicketDetail.tsx`：摘要卡片、AI 状态徽章与暂停/恢复、分类建议条、时间线 AI 消息徽章
  - `TicketTypes.tsx`：每类型「AI 自动应答」开关；`Tickets.tsx`：AI 状态徽章
  - locale zh/en 补全；设置页显著位置放**隐私提示**（工单内容将发送至所配置的 LLM 服务商）

## 7. 测试与文档

- `internal/llm`：httptest 单测（成功 / 429 重试 / 超时 / 日志不含 Key）
- `internal/ai`：防抖与接管、知识去重、检索排序、提示词组装、JSON 容错解析
- `internal/bot`：kooktest + llmtest 双模拟的端到端链路（用户消息 → AI 回复入库并推送；管理员回复 → 接管 + 知识捕获；/tkai）
- `internal/api`：设置 CRUD 与掩码、RBAC（只读拒绝）、摘要/分类接口
- 文档：README 功能与配置表、CHANGELOG、docs/DEPLOYMENT.md（LLM 环境变量）、.env.example；deploy.sh 无需改动（单二进制形态不变）

## 实施顺序

1. `internal/llm` + llmtest + AI 配置仓储 + 环境变量装载
2. store 模型与迁移（新字段 + 两张新表）
3. `internal/ai` 服务（生成、防抖、工人池、知识捕获/检索）
4. bot 接线（消息钩子、Sender 实现、/tkai、关单摘要触发）
5. API 路由与 handler
6. 前端页面与详情集成、i18n
7. 全量测试补齐 + 文档更新

每步完成后跑 `make check` + `make test`（Go）与前端 `npm test`，保持 CI 全绿。