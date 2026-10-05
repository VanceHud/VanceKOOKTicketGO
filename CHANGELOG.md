# 更新日志

本文件记录用户可见的变更。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## [未发布]

首次公开发布的准备：补齐 MIT 许可证与开源社区文件（`LICENSE`、`SECURITY.md`、`CONTRIBUTING.md`、
`.gitattributes`、GitHub Actions CI），Go module 路径改为 `github.com/VanceHud/VanceKOOKTicketGO`，
并把宿主机默认端口统一为 `9235`。

## [0.1.0] - 2026-10-05

首个可部署版本，覆盖里程碑 1–6。

### 新增

- **KOOK 接入**：自研 REST 客户端（按平台限流桶排队 + 自适应全局令牌桶 + 429 退避重试）、
  WebSocket 网关（zlib 解压、心跳与看门狗、`session_id` + `sn` 落库续传、指数退避重连）。
- **工单流程**：按钮开单 → 建隐藏频道 → 并发下发权限 → 发送含「关闭/锁定」按钮的卡片 →
  发送面板自定义提示 → 向开单人发送仅本人可见的完成提示；关闭、锁定、重新激活、
  超时自动锁定，以及超时扫描。
- **消息归档**：文本 / 图片 / 视频 / 文件 / 语音 / 卡片消息全部入库；卡片消息自动调用
  `message/view` 拉取原始 JSON 并生成文本摘要。
- **命令**：`/ticket` `/tkcm` `/aar` `/tkhelp` `/hello` `/kill` `/login` `/bind`。
- **一次性码**：`/login` 签发 6 位登录码（Crockford Base32、5 分钟、一次性、按用户限频）；
  `/bind` 签发绑定码，用于在 WebUI 关联 KOOK 身份。
- **表情上角色**：对配置的消息回应表情即发放对应角色，换表情先撤销旧角色。
- **在玩动态**：游戏库增删改，以及设置「在玩 / 在听」动态（支持网易云 / QQ 音乐 / 酷狗），
  机器人重连后可按开关自动恢复。
- **WebUI**：仪表盘、工单列表与详情（富消息时间线 + 备注 + 导出）、面板管理、表情规则、
  账号管理、角色映射、机器人状态、机器人动态、系统设置、审计日志、我的账号；
  SSE 实时推送与断线重连补拉。
- **统计看板**：区间工单量与关闭率、首次响应与处理时长的平均 / P50 / P90、24 小时时段分布、
  客服处理量排行、来源面板分布、今日 vs 昨日对比、归档消息量与单均消息数。
- **导出**：聊天记录导出 JSON / CSV（含 BOM 与媒体链接列）/ HTML（内容转义、图片内联、
  音视频与文件可播放下载）。
- **离线演示**：`KOOK_DRYRUN=1` 写入 30 条演示工单，无需 KOOK Token 即可完整验收界面。
- **部署**：三阶段 Dockerfile（运行镜像非 root、不含 Node 与源码）、`docker-compose.yml`
  （只读根文件系统 + `no-new-privileges` + 日志轮转）、`deploy.sh`
  （`up` / `upgrade` / `backup` / `restore` / `reset-password` / `status` / `logs` / `doctor` / 交互菜单），
  以及 `docs/DEPLOYMENT.md` 完整教程。

### 安全

- KOOK Token 以 AES-GCM 加密入库，密钥来自 `APP_SECRET` 或数据目录下 0600 的 `app_secret`。
- 密码使用 bcrypt；账号不存在时比对占位哈希以降低账号枚举风险。
- 会话 Cookie 为 HttpOnly + SameSite，配合 CSRF 令牌；改密 / 禁用 / 删号 / 角色变更时吊销会话。
- 密码、角色、禁用、强制改密状态与会话删除在同一事务提交。
- 登录失败按 IP + 账号双维度限流，达阈值锁定并指数退避。
- 一次性码只存哈希、一次性使用（条件更新防并发重放）、按 IP 限流。
- 请求体限长 64 KiB；公开接口非空请求体仅接受 JSON，阻断简单跨站登录提交。
- 最后管理员保护在 SQLite 写事务内校验，避免并发删除 / 禁用 / 降级绕过。
- SSE 每次推送重新校验会话，失效后发送 `auth.expired` 并关闭连接。
- 导出与卡片链接只接受绝对 `http(s)` URL；CSV 公式注入前置文本标记。
- 安全响应头（CSP / HSTS / `X-Content-Type-Options` / `X-Frame-Options` / Permissions-Policy）。
- `TRUSTED_PROXIES` 未配置时不采信 `X-Forwarded-For`，避免伪造头绕过限流与审计。
- WebSocket 原始帧与解压后内容均限长 8 MiB，握手设读取截止时间。
- 数据目录 0700、数据库与密钥文件 0600；GORM 日志参数化，避免泄露哈希与聊天内容。

### 性能

- 「发卡片 + 权限下发 + 面板提示」并发执行，开单主链路不再用固定 `sleep`。
- 网关读取协程与事件处理解耦（按频道分片，同频道保序、跨频道并行），避免长流程阻塞心跳。
- 增加 `(ticket_no, created_at)`、`(status, updated_at)` 复合索引。
- SSE 按 250 ms 窗口合并去重，100 条消息只触发 7 个不同查询键各一次刷新。
- 统计改为流式处理，取消 50,000 条静默上限；导出独立查询，上限 20,000 条。

[未发布]: https://github.com/VanceHud/VanceKOOKTicketGO/compare/main...HEAD
[0.1.0]: https://github.com/VanceHud/VanceKOOKTicketGO/releases/tag/v0.1.0
