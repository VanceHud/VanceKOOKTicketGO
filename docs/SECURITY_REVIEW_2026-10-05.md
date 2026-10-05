# 安全与效率审查记录

日期：2026-10-05。范围：Go API、账号/会话/一次性码、SQLite 仓储、KOOK 机器人与网关、React 实时刷新与富媒体渲染、导出、部署脚本和依赖。

本次已直接修复下列源码中可确认的问题。验证使用临时数据库和本机模拟 KOOK，不连接真实服务器、不修改运行数据，不执行部署。

## 安全与一致性问题

| 问题 | 风险与触发方式 | 修复 | 主要位置 |
| --- | --- | --- | --- |
| 一次性码登录回退历史角色 | 当前角色被移除、映射被删除或上游查询失败时，仍可能使用签发时的管理员快照登录；正常角色缓存也有 30 秒延迟 | 生产登录必须直接查询当前 KOOK 角色，绕过缓存；查询不可用返回 503、无映射返回 403；失败不消费码。仅显式 DryRun 且无机器人可使用测试快照 | `internal/api/auth.go`、`internal/bot/commands.go` |
| 安全字段更新与会话吊销分开提交 | 吊销失败后密码/角色已变更，旧会话仍存在 | 密码、角色、禁用、强制改密状态更新与会话删除在同一事务提交；吊销失败回滚变更 | `internal/store/repo_users.go` |
| 在途登录与改密竞态 | bcrypt 已校验旧密码后，管理员改密/禁用；旧请求随后创建新会话。用户改密也可能覆盖管理员刚重置的密码 | 创建会话和用户改密时，在写事务内重新核对经过认证的安全字段；已变更则拒绝旧请求 | `internal/auth/session.go`、`internal/store/repo_users.go` |
| 最后管理员保护仅在 API 提前检查 | 两个管理员同时被删除、禁用或降级时，两个检查都可能放行 | 在 SQLite 写事务中检查至少保留一个可用管理员 | `internal/store/repo_users.go` |
| 身份绑定检查与消费分离 | 同一账号并发绑定两个不同 KOOK 身份可能覆盖关联；未完成强制改密也能绑定 | 绑定检查、更新和消费码合并事务；更新条件再次检查禁用/强制改密/现有关联；冲突不消费码 | `internal/api/router.go`、`internal/store/repo_users.go` |
| 一次性码消费未再次检查有效期 | 验证后的慢请求可能在码过期后才消费 | 原子更新同时要求未使用、未过期 | `internal/store/repo_users.go` |
| SSE 只在建立连接时认证 | 改密、禁用或吊销后已有连接仍可接收敏感事件；长连接缺少账号限额和写入截止时间 | 每次推送与心跳重新校验会话；失效发送 `auth.expired` 后关闭；每账号最多 5 条，单次写入最多 10 秒；推送不续期 | `internal/api/admin.go`、`internal/eventbus/bus.go` |
| 请求体与密码比对缺少资源边界 | 公开 API 可发送大 JSON；同时到达的登录请求能在失败计数生效前占满 CPU | API 完整请求体最多 64 KiB，分块及合法 JSON 后附加的大数据也拒绝；非空请求体仅接受 JSON，阻断简单跨站登录提交；密码登录每 IP 每分钟 20 次、bcrypt 并发最多 4 个 | `internal/api/request.go`、`internal/api/auth.go` |
| 原始密钥校验无效 | 先 SHA-256 再检查长度，任意短输入及空密钥文件都得到 32 字节，校验形同虚设 | 哈希前校验原始输入至少 32 字节；已有密钥文件收紧至 0600；弱文件拒绝启动且不覆盖；登录限流窗口与锁定时长也必须为正数 | `internal/config/config.go` |
| 导出与卡片链接协议未限制 | HTML 属性转义不能阻止 `javascript:` 等主动协议；用户消息可成为 CSV 公式 | 附件、卡片图片/音视频/按钮链接只接受绝对 HTTP(S) URL；拒绝含凭据的地址；CSV 潜在公式前置文本标记，覆盖空白/控制符绕过 | `internal/api/tickets.go`、`web/frontend/src/lib/media.ts`、`KookCardView.tsx` |
| 生产平台使用空实现 | 未配置机器人或机器人退出时，关闭/锁定等操作仍可能只改变数据库并显示成功 | 空平台仅用于显式 DryRun；生产无平台返回 503，保留工单状态；平台切换加读写锁 | `cmd/server/main.go`、`internal/bot/bot.go`、`internal/ticket/service.go` |
| WebSocket 原始帧未限长 | 压缩前的数据可无界分配；解压上限静默截断；握手等待无期限 | 原始帧与解压后内容均最多 8 MiB；超过即失败；HELLO 握手设置读取截止时间 | `internal/kook/gateway.go` |
| 日志与文件保护不完整 | GORM 异常 SQL 含绑定参数，可能泄露哈希/内容；迁移收紧权限误用了驱动名；备份含密钥 | SQL 日志改为参数化；使用真实数据库路径收紧权限；部署脚本 `umask 077`；Docker 构建上下文排除备份及 `.env.*`；API 响应 `no-store` | `internal/store/db.go`、`deploy.sh`、`.dockerignore` |

## 效率与数据完整性

| 发现 | 实施与可验证结果 |
| --- | --- |
| 每次认证都更新 SQLite 的 `last_seen_at`，首屏并发 GET 争抢单写锁 | 正常请求按最多一分钟的间隔刷新；短会话使用空闲 TTL 的 1/4。仍每次读库验证吊销。回归用例中连续 50 次认证的续期 UPDATE 为 0；超过间隔后为 1。延迟请求不能把时间写回过去 |
| 仪表盘重复查询总量与状态数 | 复用现有分组结果，Overview 从 10 次 SQL 减到 5 次；查询回调验证包括流式查询 |
| 消息时间线和超时扫描缺少匹配筛选/排序的复合索引 | 增加 `(ticket_no, created_at)` 和 `(status, updated_at)`，首次响应字段加索引；EXPLAIN 验证消息时间线使用复合索引并避免临时排序 |
| SSE 每条消息立即刷新多份相同查询，且漏刷新细化统计与机器人状态 | 250 ms 合并窗口按查询键去重；覆盖 analytics/runtime，重连补拉。测试中 100 条相同工单消息加统计/机器人/备注事件只刷新 7 个不同查询键，每键一次 |
| 缓存与限流器历史键积累；部分清理每次扫描全表 | 缓存过期删除、按周期清理，最多 2,048 项；限流器按周期清理，最多 16,384 个键，满额时拒绝新键；清理保留仍有效的账号锁定 |
| 导出要求 20,000 条却被普通分页的 2,000 条上限截断 | 独立导出查询返回完整上限内记录，超过 20,000 条返回明确 422；CSV 不额外查询不会用到的备注 |
| 统计最多加载 50,000 个工单，范围较大时无提示漏算 | 改为流式处理，不设静默条数上限；50,001 条回归用例验证 Overview 趋势与 Analytics 来源均完整；时长样本原地排序，减少复制 |
| 来源关闭率分子分母混用时间口径，旧工单的近期首次回复也被漏掉 | 总量、关闭率、来源及单均消息数统一使用“区间内开单”的同一批工单；首次回复、解决时长和关闭时段按区间内事件时间统计，包含历史开单；最近秩分位数使用向上取整 |

统计的关闭率为区间内新增工单目前已关闭的比例；关闭事件数量可能包含之前开单的工单，因此与新增工单的已关闭量不同。页面文案已同步，只有历史工单近期有响应/关闭时也会显示统计。

## 依赖检查

首次 `govulncheck` 在 Go 1.26.5 与 quic-go 0.59.0 下报告 6 个符号级命中。本次把 Go 最低版本及 Docker 构建阶段升级到 1.26.8，把 quic-go 升至 0.59.1，保留其它依赖版本。

扫描为静态可达性结果，不等于所有漏洞在本服务运行配置下均可利用；例如本项目没有启用 HTTP/3 服务监听。

漏洞库和补丁来源：

- [GO-2026-6218：net/url 路径处理](https://pkg.go.dev/vuln/GO-2026-6218)
- [GO-2026-6090：TLS 握手后的消息限制](https://pkg.go.dev/vuln/GO-2026-6090)
- [GO-2026-6089：HTTP/2 请求头读取截止时间](https://pkg.go.dev/vuln/GO-2026-6089)
- [GO-2026-5972：ASN.1 递归解析](https://pkg.go.dev/vuln/GO-2026-5972)
- [GO-2026-5026：HTTP IDNA 处理](https://pkg.go.dev/vuln/GO-2026-5026)
- [GO-2026-5676：quic-go HTTP/3 QPACK 内存分配](https://pkg.go.dev/vuln/GO-2026-5676)，[quic-go 0.59.1 发布记录](https://github.com/quic-go/quic-go/releases/tag/v0.59.1)
- [Go 官方下载与版本列表](https://go.dev/dl/)

升级后的 verbose 扫描报告：代码受影响漏洞 0，导入包其它漏洞 0；仍有 1 条模块级提示 [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932)，指向 `golang.org/x/crypto/openpgp`。项目使用该模块的 bcrypt，未导入 openpgp，因此未命中代码。前端 `npm audit` 报告 0 漏洞。

## 部署变化与验证边界

- 需要 Go 1.26.8 或更新工具链；Dockerfile 已同步。此次未构建/运行 Docker 镜像。
- 升级后自动迁移新增索引，不删除已有业务字段。只在临时数据库验证，未对生产数据库执行迁移或备份恢复。
- 已有短 `APP_SECRET` 或短/空 `app_secret` 文件会拒绝启动。更换密钥会使现有加密 KOOK Token 无法解密；先备份旧密钥和数据，再按维护流程更换并重新配置 Token。正常随机密钥的派生算法保持一致。
- API 有非空请求体时必须发送 `Content-Type: application/json`，最多 64 KiB。现有 WebUI 客户端已按此格式发送。
- 空闲会话时间最多比最近一次实际操作提前一个续期间隔到期，默认不超过一分钟；SSE 推送不计活跃。SSE 收到下一事件时会重新校验，安静时最迟下一次 25 秒心跳关闭失效连接。
- 本次未验证真实 KOOK 角色变更、网关重连、反代流式超时、浏览器多标签页或线上负载。性能证据来自查询/写入/刷新次数及查询计划，没有宣称线上吞吐或延迟改善比例。

## 最终验证结果

以下结果针对最终源码，工具链为 `go1.26.8 darwin/arm64`。

| 检查 | 结果 |
| --- | --- |
| `go test -race ./...` | 全部通过；涵盖 API 集成、模拟 KOOK、并发事务与新安全回归；无竞争报告 |
| `go vet ./...` | 通过 |
| `make build` | TypeScript/Vite 构建通过，已同步嵌入前端并生成 `bin/kook-ticket` |
| 前端 `npm test` | 2 个测试通过：附件 URL、实时事件批量刷新 |
| 前端 `npm run lint` | 退出码 0；保留 9 条原有 React effect/Fast Refresh 告警，无新增告警 |
| `go run golang.org/x/vuln/cmd/govulncheck@latest -show verbose ./...` | 0 个代码受影响漏洞，0 个导入包其它漏洞；1 个未导入 openpgp 的模块级提示，见依赖检查 |
| 前端 `npm audit --json` | 248 个依赖，0 漏洞 |
| `gofmt -l internal cmd`、`git diff --check`、`bash -n deploy.sh` | 通过 |

新增回归集中在 `internal/api/security_review_test.go`、`internal/auth/*review_test.go`、`internal/store/*review_test.go`、`internal/bot/security_review_test.go`、`internal/kook/gateway_review_test.go`、`internal/eventbus/bus_test.go`、`internal/ticket/service_test.go` 和 `web/frontend/tests/security-review.test.mjs`。

关键用例实际检查了：旧角色快照不可登录、查询故障不消费码、会话吊销失败回滚、在途登录/改密拒绝旧状态、并发保留管理员、并发绑定不覆盖、并发会话上限、SSE 吊销后不发送敏感事件、压缩数据超限、20,001 条导出明确报错、50,001 条工单统计完整、SQL 不输出敏感参数及时间线索引命中。
