# KOOK Ticket · Go + WebUI

KOOK 工单（Ticket）机器人，带自托管 WebUI 与 SQLite 数据库，**单二进制 / 单容器部署**。

参考并重写了 [musnows/Kook-Ticket-Bot](https://github.com/musnows/Kook-Ticket-Bot)（Python + 配置散落在多个 JSON 文件）的能力，
把配置、工单状态与聊天记录全部收敛到 SQLite，并补齐了 WebUI、实时推送、审计日志与一套可验证的安全基线。

> **当前进度：里程碑 1 已完成**（骨架、数据层、安全基线、WebUI 全部页面、Docker 交付、离线演示模式）。
> KOOK 接入层（REST + WebSocket 网关）在里程碑 2/3 落地，详见文末路线图。

---

## 1. 功能一览

### 已实现（里程碑 1）

| 模块 | 说明 |
|---|---|
| 工单数据模型 | 工单 / 聊天记录 / 备注 / 面板 / 表情规则 / 审计日志全部入库，重启不丢状态 |
| 工单编号 | `TK-YYMMDD-XXXX`，Crockford Base32 随机段（去掉 I/L/O/U），按 `TICKET_TZ` 计算日期段，唯一索引 + 冲突自动加宽 |
| 工单状态机 | 进行中 / 已锁定 / 已关闭 / 创建中 / 创建失败，非法流转返回 409 并说明原因 |
| WebUI | 仪表盘（KPI + 趋势图 + 状态分布）、工单列表（筛选 + 分页）、工单详情（时间线 + 备注 + 操作 + 导出） |
| 配置管理 | KOOK Token（AES-GCM 加密存储、只写不回显）、服务器 / 分组 / 日志 / 调试频道、超时锁定小时数 |
| 账号与权限 | 管理员 / 客服 / 只读三种角色；KOOK 角色 → WebUI 权限映射表；未命中映射的用户无法登录 |
| 实时推送 | SSE 推送工单事件，列表与详情自动刷新，顶栏显示连接状态 |
| 导出 | 聊天记录导出 JSON / CSV（含 BOM）/ HTML（内容全部转义） |
| 审计日志 | 登录、改密、配置变更、账号管理、工单操作、导出，全部只追加 |
| 离线演示 | `KOOK_DRYRUN=1` 写入演示工单，无需 KOOK Token 即可完整验收界面 |
| 检索 | 关键词同时匹配工单编号前缀、用户昵称、用户 ID 与聊天内容；通配符已转义 |

### 计划中

| 里程碑 | 内容 |
|---|---|
| 2 | 自研 KOOK 客户端：REST 封装 + 令牌桶限速 |
| 3 | WebSocket 网关（zlib 解压、30s 心跳、session resume、sn 序号重放）+ 工单核心流程（按钮开单、建频道、权限下发、关闭删频道、通知卡片）|
| 4 | `/login` 一次性登录码、`/bind` 绑定码、角色映射自动登录 |
| 5 | 表情回应上角色、在玩状态（游戏/听歌）管理 |
| 6 | 面板卡片创建/重建、表情规则增删改、统计看板细化 |
| 7 | TOTP 二步验证（数据库字段与登录状态机已预留）、客服指派/抢单、开单表单模板、多服务器支持 |

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

安全不是"以后再加"，以下每条都有实现与测试覆盖（`go test ./...` 共 51 个用例，其中包含完整的 HTTP 层集成测试）。

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
| 17 | 数据文件权限：目录 0700、数据库与密钥 0600 | `internal/store` 测试 + 容器验证 |
| 18 | 依赖供应链：前端仅从官方 shadcn registry 取组件、`npm ci` 可复现、`npm audit` 为 0 漏洞，运行镜像不含 Node/npm | `docker history` 可见 |

注意事项：
* 暴露到公网请务必放在反向代理之后启用 HTTPS，并把代理地址写入 `TRUSTED_PROXIES`；
* 请设置 `APP_SECRET` 并妥善备份——更换该值后已存储的 KOOK Token 无法解密，需要重新填写。

---

## 5. 数据与备份

* SQLite（WAL 模式）保存在 `DATA_DIR`：`ticket.db`、`ticket.db-wal`、`ticket.db-shm`、`app_secret`。
* 所有时间戳以 UTC 存库（驱动的时间文本格式定宽，因此文本比较等价于时间比较），界面按浏览器本地时区渲染。
* 在线备份（不中断服务）：

```bash
# 宿主机未安装 sqlite3 时可通过容器执行
docker compose exec kook-ticket sh -c 'ls -l /app/data'
sqlite3 ./data/ticket.db ".backup './data/backup-$(date +%F).db'"
```

* 恢复：停止服务 → 用备份文件替换 `ticket.db` → 启动（同时删除遗留的 `-wal` / `-shm`）。

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
make test       # Go 测试（51 个用例，含 HTTP 层集成测试）
make docker     # 构建镜像
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

* KOOK 接入层尚未实现（里程碑 2/3）：当前按钮事件、频道创建、通知卡片都不会真的发往 KOOK；
  `KOOK_DRYRUN=1` 或未配置 Token 时，工单操作只更新数据库并推送 WebUI 事件。
* 面板与表情规则的增删改仍在路线图上，当前版本只展示已入库配置。
* WebUI 不提供"以机器人身份发言"，对话仍在 KOOK 内进行（按需求约定）。
* 单服务器（单 guild）设计；多服务器支持在路线图中。

---

## 9. 许可与致谢

* 本项目参考 [musnows/Kook-Ticket-Bot](https://github.com/musnows/Kook-Ticket-Bot) 的产品形态与流程设计，代码为独立实现。
* 前端组件来自 [shadcn/ui](https://ui.shadcn.com/)（MIT），详见 `THIRD-PARTY-NOTICES.md`。
