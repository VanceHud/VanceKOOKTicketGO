# 贡献指南

感谢愿意花时间改进这个项目。这份文档说明本仓库的约定，让 PR 能顺利合入。

## 先聊再写

**改动较大时请先开 issue 说明意图**，避免你花几天写完却因为方向不同被拒。以下情况尤其建议先讨论：

- 新增依赖（尤其是后端 Go 依赖 —— 本项目刻意保持依赖很少）
- 修改数据模型或需要数据库迁移
- 改动 `deploy.sh`、`Dockerfile`、认证/加密相关代码
- 引入新的抽象层或大范围重命名

**不需要**先问的：修 bug、补测试、改文档、修错别字 —— 直接提 PR 就好。

## 开发环境

需要 Go 1.26.8+ 与 Node 22+。

```bash
make dev-backend     # DryRun 模式起后端 :9235，自动写入演示数据
make dev-frontend    # Vite :5173，自动代理 /api 到 :9235
```

演示模式不需要 KOOK Token，但界面与操作链路是完整的。想固定管理员密码：

```bash
ADMIN_PASSWORD='DemoTicket@2026' make dev-backend
```

## 提交前必须通过

```bash
make check    # gofmt 校验 + go vet + go build + 前端构建
make test     # go test ./...
cd web/frontend && npm run lint && npm run test
```

CI 会跑同样的东西（外加 `go test -race`）。**gofmt 未通过会直接失败** —— 提交前跑一次 `gofmt -w .`。

## 代码约定

### 通用

- **注释、日志、错误信息、文档统一用中文**（前端 i18n 词条除外）。
- 注释解释**为什么**，不是复述代码做了什么。已有的注释风格值得参考。
- **不要引入无必要的依赖。** 后端每个新依赖都要在 `THIRD-PARTY-NOTICES.md` 中登记；许可证必须是 MIT / BSD / Apache-2.0 / ISC 这类宽松协议。

### 后端（Go）

- 编译期不引入 CGO（`CGO_ENABLED=0` 必须能构建）。SQLite 用的是纯 Go 驱动，别换回 `mattn/go-sqlite3`。
- 错误用 `fmt.Errorf("...: %w", err)` 包装，不要把底层错误吞掉。
- 涉及**并发写数据库**的地方要清楚 SQLite 只有单写者，参考 `internal/keyedlock` 与 `store` 里已有的做法。
- 新增 HTTP 端点要放在 `internal/api/router.go` 中**正确的权限分组**里（`readonly` / `operator` / `admin`），不要自己写鉴权判断。
- 机密信息（Token、密码、一次性码）**只能存哈希或密文**，落库前请复用 `internal/secure`。
- 日志里不要出现 Token、密码明文、会话 ID；需要展示时用 `secure.MaskTail`。

### 前端（TypeScript / React）

- **时间一律按业务时区渲染**（默认 `Asia/Shanghai`），用 `src/lib/timezone.ts`，不要直接用 `date-fns` 的本地格式化，也不要依赖浏览器时区。
- 渲染用户可控内容时保持转义。外链必须经过 `src/lib/media.ts` 的协议白名单（只允许绝对 `http(s)`）。
- `src/components/ui/` 是从 shadcn/ui 官方 registry 生成的胶水组件，**尽量不做业务定制**；确实要改时同步更新 `THIRD-PARTY-NOTICES.md`。
- 新增文案必须同时补 `src/locale/zh-CN.json` 与 `src/locale/en-US.json`，缺一个就视为未完成。
- 数据请求走 TanStack Query，新增加查询键时要考虑 SSE 刷新（见 `src/lib/queries.ts` 与 `realtime-batch.ts`）。

## 测试

- **修 bug 请带上能复现该 bug 的回归用例。** 只改代码不补测试的 PR 通常会被要求补上。
- 涉及 KOOK 交互的测试请用 `internal/kook/kooktest` 里的进程内模拟平台，**不要在测试里连真实 KOOK**。
- 测试必须能在离线环境跑通，不能依赖公网。
- 测试里不要写真实 Token / 服务器 ID；用 `90xxxxxxxxxxxxxxx` 这类明显的假 ID。

## 提交与 PR

- 提交信息建议用 `type: 简述` 的格式（`feat` / `fix` / `perf` / `refactor` / `docs` / `chore` / `test`），说明用中文，例如
  `fix: 修复重启后收不到网关事件`。
- 一个 PR 做一件事。顺手重构无关文件会让 review 变得很痛苦。
- PR 描述里请写清：**改了什么、为什么改、怎么验证的**。涉及界面的改动**请附上截图**。
- 如果改动影响部署方式或配置项，请同步更新 `README.md`、`docs/DEPLOYMENT.md` 与 `.env.example`。

## 许可

提交 PR 即表示你同意以本项目的 [MIT 许可证](LICENSE) 授权你的贡献，并确认这些代码是你自己编写的、或来源与本许可证兼容。
