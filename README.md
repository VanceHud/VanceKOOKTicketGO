# KOOK Ticket · Go + WebUI

KOOK 工单（Ticket）机器人，带自托管 WebUI 与 SQLite 数据库，**单二进制 / 单容器部署**。

参考并重写了 [musnows/Kook-Ticket-Bot](https://github.com/musnows/Kook-Ticket-Bot)（Python + 配置散落在多个 JSON 文件）的能力，
把配置、工单状态与聊天记录全部收敛到 SQLite，并补齐了 WebUI、实时推送、审计日志与一套可验证的安全基线。

> **当前进度：里程碑 1–6 已完成**：骨架与安全基线、WebUI 全部页面、Docker 交付、离线演示模式、
> 自研 KOOK 客户端（REST + WebSocket 网关）、真实工单流程（按钮开单 → 建频道 → 权限下发 → 关闭通知 → 删除频道）、
> `/login` 一次性登录码与 `/bind` 账号绑定、表情上角色与在玩状态命令，
> 以及**面板与表情规则的 WebUI 增删改**和**统计看板**（分位时长、时段分布、客服处理量、来源分析）。
> 所有时间默认按**北京时间（Asia/Shanghai）**展示与统计。剩余路线图见文末。

---

## 1. 功能一览

### KOOK 接入与工单流程

| 模块 | 说明 |
|---|---|
| KOOK 客户端 | 自研 REST 封装 + 令牌桶限速（默认 4 req/s）+ 429 退避重试 + 动态读取平台限流响应头 |
| WebSocket 网关 | zlib 解压、30s 心跳（6s 未收到 PONG 判定断线）、session resume 续传、sn 序号、指数退避重连 |
| 按钮开单 | 校验面板 → 一人一单 → 私信可用性探测 → 分配编号 → 在隐藏分组建频道 → 下发权限（全局管理员角色 + 面板角色 + 开单人）→ 发送含「关闭/锁定」按钮的卡片 |
| 关闭工单 | 管理员鉴权 → 日志频道卡片 + 私聊开单人 → 删除工单频道 → 落库并记录日志卡片消息 ID |
| 锁定 / 重新激活 | 通过频道权限位（2048 查看 / 4096 发言）实现“可看不可发”，并发送带「重新激活」按钮的提示卡片 |
| 超时自动锁定 | 每 10 分钟扫描超过 `outdateHours` 无活动的工单并锁定，同样发送提示卡片 |
| 消息归档 | 工单频道内的文本/图片/视频/文件/语音/卡片消息全部入库（机器人消息不污染记录） |
| 命令 | `/ticket` `/tkcm` `/aar` `/tkhelp` `/hello` `/gaming` `/singing` `/sleeping` `/kill` `/login` `/bind` |
| 一次性码 | `/login` 签发 6 位登录码（Crockford Base32，5 分钟、一次性、按用户限频）；`/bind` 签发绑定码，在 WebUI「我的账号」中输入即可关联身份 |
| 表情上角色 | 对配置的消息回应表情即发放对应角色，换表情先撤销旧角色；失败会提示用户检查机器人角色位置 |
| 在玩状态 | `/gaming 游戏ID`、`/singing 歌名 歌手`、`/sleeping 1|2`（仅管理员可用） |
| 运维 | `/kill @机器人` 触发优雅退出（容器自动重启）；WebUI 可一键「重新连接」重载配置 |
| 面板管理（WebUI） | 新建面板（选频道 → 机器人发卡片）、重建卡片（自动删除旧卡片）、启停、删除；面板级管理员角色可在界面增删 |
| 表情规则（WebUI） | 「消息 ID + 表情 → 角色」规则的增删改与启停，无需改动配置文件 |
| 统计看板 | 区间工单量与关闭率、首次响应与处理时长的**平均 / P50 / P90**、24 小时时段分布、客服处理量排行、来源面板分布、今日 vs 昨日对比、归档消息量与单均消息数 |

### 数据与 WebUI

| 模块 | 说明 |
|---|---|
| 工单数据模型 | 工单 / 聊天记录 / 备注 / 面板 / 表情规则 / 审计日志全部入库，重启不丢状态 |
| 工单编号 | `TK-YYMMDD-XXXX`，Crockford Base32 随机段（去掉 I/L/O/U），按 `TICKET_TZ` 计算日期段，唯一索引 + 冲突自动加宽 |
| 工单状态机 | 进行中 / 已锁定 / 已关闭 / 创建中 / 创建失败，非法流转返回 409 并说明原因 |
| WebUI | 仪表盘（KPI + 趋势图 + 状态分布）、工单列表（筛选 + 分页）、工单详情（时间线 + 备注 + 操作 + 导出）、面板、表情上角色、账号管理、角色与权限映射、机器人状态（含重新连接）、系统设置、审计日志、我的账号 |
| 配置管理 | KOOK Token（AES-GCM 加密存储、只写不回显）、服务器 / 分组 / 日志 / 调试频道、超时锁定小时数；保存后自动重连 |
| 账号与权限 | 管理员 / 客服 / 只读三种角色；KOOK 角色 → WebUI 权限映射表；未命中映射的用户无法登录 |
| 实时推送 | SSE 推送工单事件，列表与详情自动刷新，顶栏显示连接状态 |
| 导出 | 聊天记录导出 JSON / CSV（含 BOM）/ HTML（内容全部转义） |
| 审计日志 | 登录、改密、配置变更、账号管理、工单操作、导出、机器人重连，全部只追加 |
| 离线演示 | `KOOK_DRYRUN=1` 写入演示工单，无需 KOOK Token 即可完整验收界面 |
| 检索 | 关键词同时匹配工单编号前缀、用户昵称、用户 ID 与聊天内容；通配符已转义 |
| 时区 | 业务时区默认 `Asia/Shanghai`：工单编号日期段、统计分日/分时都在北京时间下计算；前端所有时间也按该时区渲染（而不是浏览器本地时区），并在列表/审计/详情页标注「北京时间 (UTC+8)」 |

### 计划中

| 里程碑 | 内容 |
|---|---|
| 7 | TOTP 二步验证（数据库字段与登录态已预留） |
| 8 | 客服指派/抢单、开单表单模板 |
| 9 | 报表定时导出（CSV 邮件/Webhook 推送）、按客服的自定义工作量统计 |
| 10 | 多服务器（多 guild）支持、账号解绑接口 |

> 📘 **部署到服务器请直接看 [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)**：
> 从 KOOK 应用申请、权限与事件订阅，到 Docker Compose / 二进制 + systemd 部署、
> 反向代理与 HTTPS、首次配置顺序、备份恢复、忘记密码救援与排错速查表。

---

## 2. 快速开始

### 方式一：Docker（推荐）

```bash
git clone <repo> kook-ticket && cd kook-ticket

cp .env.example .env
# 生成加密密钥（用于加密存储 KOOK Token）
echo "APP_SECRET=$(openssl rand -hex 32)" >> .env

docker compose up -d --build
docker compose logs -f kook-ticket
```

启动后访问 `http://127.0.0.1:8080`。

初始管理员账号：
* 未设置 `ADMIN_PASSWORD` 时，会生成随机密码并打印在日志中（搜索 `initial_password`），**首次登录强制改密**；
* 设置了 `ADMIN_PASSWORD` 时使用该密码（需满足强度要求，否则启动失败）。

```bash
docker compose logs kook-ticket | grep initial_password
```

### 方式二：单二进制

```bash
make build                 # 构建前端 + 编译 bin/kook-ticket
APP_SECRET=$(openssl rand -hex 32) ./bin/kook-ticket
```

`make build` 会把前端产物同步到 `web/dist` 并被 `//go:embed` 打进二进制，因此最终只需分发一个文件。

### 方式三：离线演示模式（不需要 KOOK）

```bash
KOOK_DRYRUN=1 ADMIN_PASSWORD='DemoTicket@2026' go run ./cmd/server
```

会写入 30 条演示工单（含聊天记录、备注、面板、角色映射、表情规则与审计记录），
所有界面与操作都能跑通，但不产生任何 KOOK 侧副作用。

### 开发模式

```bash
make dev-backend     # 后端跑在 :8080（DryRun）
make dev-frontend    # Vite 开发服务器 :5173，自动代理 /api 到后端
```

---

## 3. 配置项

全部通过环境变量提供（`.env.example` 有完整说明）。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `PORT` | `8080` | WebUI 监听端口 |
| `DATA_DIR` | `./data` | 数据目录（SQLite 与自动生成的密钥） |
| `DB_PATH` | `$DATA_DIR/ticket.db` | 数据库文件路径 |
| `TICKET_TZ` | `Asia/Shanghai` | 工单编号日期段与「今日」统计口径 |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `KOOK_DRYRUN` | `0` | `1` = 离线演示模式 |
| `KOOK_TOKEN` | 空 | 机器人 Token；也可稍后在 WebUI 填写（两者都加密入库） |
| `KOOK_GUILD_ID` | 空 | 服务器 ID |
| `KOOK_API_BASE` | 官方地址 | 覆盖 KOOK API 地址（自建代理/本地模拟平台） |
| `APP_SECRET` | 自动生成 | 加密密钥；生成后写入 `$DATA_DIR/app_secret`（0600） |
| `COOKIE_SECURE` | `auto` | 会话 Cookie 的 Secure 策略：`auto` / `always` / `never` |
| `TRUSTED_PROXIES` | 空 | 可信反代地址（支持 CIDR），决定是否采信 `X-Forwarded-For` |
| `ADMIN_USERNAME` | `admin` | 初始管理员用户名 |
| `ADMIN_PASSWORD` | 空 | 初始管理员密码（留空则随机生成并强制首登改密） |
| `SESSION_IDLE_HOURS` | `12` | 会话空闲过期 |
| `SESSION_MAX_DAYS` | `7` | 会话绝对有效期 |
| `LOGIN_MAX_FAILS` | `5` | 窗口内登录失败上限，达到即锁定 |
| `LOGIN_WINDOW_MINUTES` | `15` | 失败计数窗口 |
| `LOGIN_LOCK_MINUTES` | `15` | 基础锁定时长（持续失败会指数延长） |

---

## 4. 安全设计

安全不是"以后再加"，以下每条都有实现与测试覆盖——`go test ./...` 共 80+ 个用例，
其中包含 HTTP 层集成测试，以及用**进程内模拟 KOOK 平台**（REST + WebSocket，含 zlib 压缩与网关握手）
跑通的完整工单链路测试。

| # | 设计 | 验证方式 |
|---|---|---|
| 1 | bcrypt(cost=12) 哈希密码；强度校验（≥12 位、≥2 类字符、拒绝常见弱口令、拒绝纯重复） | `internal/auth` 单元测试 |
| 2 | 首次生成的随机密码强制改密，未改密前其它接口一律 403 | `TestForcedPasswordChangeBlocksOtherEndpoints` |
| 3 | 会话 token 256 位随机，数据库只存 SHA-256；空闲 + 绝对双过期；并发会话上限 5 | `internal/store` / `internal/auth` 测试 |
| 4 | 改密 / 改角色 / 禁用账号会立刻吊销该账号全部会话 | `TestForcedPasswordChangeBlocksOtherEndpoints` |
| 5 | CSRF 令牌由 `HMAC(APP_SECRET, 会话token)` 派生：不落库、会话内稳定（多标签页不互踢）、恒定时间比较 | `TestWritesRequireCSRFToken`、`TestCSRFTokenIsStableWithinSession` |
| 6 | 登录失败按 IP + 账号双维度限流，达阈值锁定并指数退避，全部写审计 | `TestLoginIsRateLimitedAfterRepeatedFailures` |
| 7 | 账号枚举防护：账号不存在与密码错误返回完全一致的响应，且都对占位哈希做一次 bcrypt 比对以抹平耗时 | `TestLoginFailureIsUniformAndAudited` |
| 8 | 服务端强制 RBAC：只读账号不能执行任何工单写操作，也不能访问管理接口 | `TestReadonlyRoleCannotOperateTickets`、`TestStaffCannotManageAccounts` |
| 9 | 管理员保护：不能修改/禁用/删除自己，且系统必须保留至少一个可用管理员 | `TestAdminCannotLockSelfOut`、`TestCannotRemoveLastActiveAdmin` |
| 10 | 安全响应头：`nosniff`、`X-Frame-Options: DENY`、`Referrer-Policy`、`Permissions-Policy`、CSP（`script-src 'self'`，无 inline script）、HTTPS 下 HSTS | `TestHealthzIsPublicAndSetsSecurityHeaders` |
| 11 | XSS 防护：前端不使用 `dangerouslySetInnerHTML`；HTML 导出对聊天内容做转义 | `TestTicketExportFormats` |
| 12 | panic 统一 recover：堆栈只进服务端日志，响应仅返回错误码 + 请求 ID | `TestPanicIsRecoveredWithoutLeakingDetails` |
| 13 | 反代可信：未配置 `TRUSTED_PROXIES` 时忽略 `X-Forwarded-For`，限流与审计拿不到伪造 IP | `internal/auth` 中间件实现 |
| 14 | KOOK Token 以 AES-GCM 加密入库，接口只返回掩码（`****a1b2`），审计不含内容 | `TestKookTokenIsWriteOnly` |
| 15 | SQL 注入与 LIKE 通配符注入防护（转义 `%` `_` `\`），关键词搜索已覆盖测试 | `TestSearchEscapesLikeWildcards` |
| 16 | 容器非 root（uid 10001）运行，支持 `read_only` 根文件系统 + `no-new-privileges` | `docker-compose.yml` |
| 16b | **按钮回调不可信**：value 只携带「动作 + 工单编号」，且经 `HMAC(APP_SECRET, 动作\|编号\|频道)` 签名；频道与用户一律以服务端字段和数据库记录为准 | `TestForgedButtonValueIsRejected` |
| 16c | 单服务器白名单：非配置服务器的频道事件直接丢弃 | `TestMessagesFromOtherGuildAreIgnored` |
| 16d | 一次性登录码只存哈希、一次性使用（`used_at IS NULL` 条件更新防并发重放）、按 IP 限流、失败原因统一提示 | `TestLoginCodeRejectsInvalidExpiredAndReused`、`TestLoginCodeIsRateLimited` |
| 16e | KOOK 身份唯一绑定：同一 KOOK 账号不能绑定多个控制台账号，同一账号不能绑定多个 KOOK 身份 | `TestBindCodeLinksKookIdentity` |
| 16f | 用户内容中的 KMarkdown 提及语法会被转义，避免通过备注/消息伪造 @全体成员 | `internal/kook` 卡片构造 |
| 17 | 数据文件权限：目录 0700、数据库与密钥 0600 | `internal/store` 测试 + 容器验证 |
| 18 | 依赖供应链：前端仅从官方 shadcn registry 取组件、`npm ci` 可复现、`npm audit` 为 0 漏洞，运行镜像不含 Node/npm | `docker history` 可见 |

注意事项：
* 暴露到公网请务必放在反向代理之后启用 HTTPS，并把代理地址写入 `TRUSTED_PROXIES`；
* 请设置 `APP_SECRET` 并妥善备份——更换该值后已存储的 KOOK Token 无法解密，需要重新填写。

---

## 5. 数据与备份

* SQLite（WAL 模式）保存在 `DATA_DIR`：`ticket.db`、`ticket.db-wal`、`ticket.db-shm`、`app_secret`。
* 所有时间戳以 UTC 存库（驱动的时间文本格式定宽，因此文本比较等价于时间比较），界面按浏览器本地时区渲染。
* 备份（两种方式，详见 [部署教程](docs/DEPLOYMENT.md#36-备份与恢复)）：

```bash
# 方式一：停服后整目录打包（最稳，包含 WAL；容器内没有 sqlite3 命令，所以不要在容器里做）
docker compose stop && tar czf backup-$(date +%F).tar.gz data/ && docker compose start

# 方式二：宿主机装有 sqlite3 时在线备份（不中断服务）
sqlite3 ./data/ticket.db ".backup './data/backup-$(date +%F).db'"
```

* 别忘了备份 `data/app_secret`：它是加密 KOOK Token 的密钥。
* 恢复：停止服务 → 删除 `ticket.db*` → 从备份恢复 → 启动。

---

## 6. 目录结构

```
.
├─ cmd/server/            # 程序入口：装配 config → store → ticket → api
├─ internal/
│  ├─ config/             # 环境变量装载与校验（密钥、时区、Cookie 策略）
│  ├─ secure/             # 随机数、SHA-256、AES-GCM、恒定时间比较
│  ├─ store/              # GORM 模型、迁移、仓储与统计查询
│  ├─ auth/               # 密码策略、会话、CSRF、限流、鉴权中间件
│  ├─ ticket/             # 工单状态机 + Platform 接口（KOOK 侧动作抽象）
│  ├─ ticketno/           # 工单编号生成与校验
│  ├─ api/                # gin 路由与 handler
│  ├─ eventbus/           # 进程内事件总线（SSE 数据源）
│  ├─ bootstrap/          # 首启初始化：管理员账号、环境变量入库
│  └─ dryrun/             # 演示数据
├─ web/
│  ├─ embed.go            # go:embed all:dist + SPA 兜底路由
│  ├─ dist/               # 前端构建产物（仅 .gitkeep 入库）
│  └─ frontend/           # React 19 + Vite + TS + Tailwind v4 + shadcn/ui
├─ Dockerfile             # 三阶段：Node 构建 → Go 编译 → alpine 运行
├─ docker-compose.yml     # 单容器部署（卷、健康检查、加固选项）
└─ Makefile               # dev / build / check / test / docker
```

---

## 7. 开发

```bash
make check      # gofmt 检查 + go vet + go build + 前端类型检查与构建
make test       # Go 测试（含 HTTP 层集成测试与模拟 KOOK 平台的端到端链路测试）
make docker     # 构建镜像
make dist       # 交叉编译发布包（linux/amd64、linux/arm64、darwin/arm64）
```

运维常用命令：

```bash
./kook-ticket -version                       # 查看版本
./kook-ticket -reset-password admin          # 忘记密码：随机生成新密码并打印
./kook-ticket -reset-password admin -password 'NewPass@2026x'
```

前端：

```bash
cd web/frontend
npm ci
npm run dev     # 开发服务器
npm run build   # 产物输出到 dist（再由 make build 同步到 web/dist 供 embed）
```

技术栈：React 19 · Vite · TypeScript · Tailwind v4 · shadcn/ui（官方 registry，Radix 基座）·
React Router · TanStack Query · TanStack Table · Recharts · react-hook-form + zod · react-i18next（中文 / English）。

后端：Go 1.26 · gin · GORM · `glebarez/sqlite`（纯 Go，无 CGO）· 唯一外部依赖仅 gin/gorm/sqlite 与 `golang.org/x/crypto`。

---

## 8. 已知限制

* 面板与表情规则的增删改目前通过 KOOK 命令（`/ticket`、`/aar`）或数据库预置完成，WebUI 侧只读。
* WebUI 不提供"以机器人身份发言"，对话仍在 KOOK 内进行（按需求约定）。
* 单服务器（单 guild）设计；多服务器支持在路线图中。
* 未提供账号解绑接口：重新绑定需管理员直接修改数据库或后续版本补充。
* KOOK 平台接口若调整（权限位、事件结构），需要同步更新 `internal/kook` 与对应测试。

---

## 9. 许可与致谢

* 本项目参考 [musnows/Kook-Ticket-Bot](https://github.com/musnows/Kook-Ticket-Bot) 的产品形态与流程设计，代码为独立实现。
* 前端组件来自 [shadcn/ui](https://ui.shadcn.com/)（MIT），详见 `THIRD-PARTY-NOTICES.md`。
