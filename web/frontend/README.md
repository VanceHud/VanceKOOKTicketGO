# KOOK Ticket · WebUI

KOOK 工单控制台前端：React 19 + Vite + TypeScript + Tailwind v4 + shadcn/ui。

后端（Go）通过 `//go:embed` 把本目录的构建产物打进单二进制，因此**生产环境不需要单独部署前端**。
本目录只在开发与构建时使用。

## 开发

```bash
npm ci
npm run dev          # Vite 开发服务器，/api 自动代理到 127.0.0.1:9235
```

后端用 DryRun 模式起在 9235（另开一个终端，在仓库根目录执行）：

```bash
make dev-backend     # 等价于 KOOK_DRYRUN=1 PORT=9235 LOG_LEVEL=debug go run ./cmd/server
```

默认管理员密码在首次启动日志里搜索 `initial_password`；也可以用
`ADMIN_PASSWORD='DemoTicket@2026' make dev-backend` 固定成已知密码。

## 构建

```bash
npm run build        # tsc -b && vite build，产物输出到 dist/
```

产物需要同步到仓库根的 `web/dist/` 才会被 `go:embed` 打包。在仓库根目录执行：

```bash
make frontend        # 等价于 npm run build + 同步 dist 到 web/dist（并保留 .gitkeep）
make build           # 前端 + 后端一起，产出 bin/kook-ticket
```

## 检查

```bash
npm run lint         # oxlint
npm run test         # node --test tests/*.test.mjs（SSE 合并刷新的回归用例）
```

`make check` 会一次性跑 `gofmt` 校验、`go vet`、`go build` 与前端构建。

## 目录约定

| 路径 | 说明 |
|---|---|
| `src/routes/` | 页面组件，与后端路由一一对应 |
| `src/components/ui/` | shadcn/ui 官方 registry 生成的组件，尽量不改，需要改时同步 `THIRD-PARTY-NOTICES.md` |
| `src/components/` | 本项目自有的业务组件（卡片渲染、Markdown 预览、分页等） |
| `src/lib/` | API 客户端、SSE、i18n、时区与格式化、KOOK 卡片/KMarkdown 解析 |
| `src/locale/` | 中英文词条，新增文案必须两种语言都补齐 |
| `tests/` | 仅覆盖纯逻辑（如 SSE 合并窗口），UI 验收走仓库根的 `scripts/e2e/` |

## 注意

* **所有时间按业务时区（默认 `Asia/Shanghai`）渲染**，不是浏览器本地时区。新增时间展示请用 `src/lib/timezone.ts`，不要直接调 `date-fns` 的本地格式化。
* 渲染用户可控内容时保持转义：`KMarkdownPreview` / `KookCardView` 已做协议白名单（只允许绝对 `http(s)` 链接），新增加载外链的地方请复用 `src/lib/media.ts`。
