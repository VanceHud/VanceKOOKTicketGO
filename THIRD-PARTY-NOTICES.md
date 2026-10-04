# 第三方组件与许可声明

本项目为独立实现，但使用/内嵌了以下第三方组件。按依赖类型分类列出，便于合规审查。

## 1. 内嵌（vendor）到源码中的文件

| 文件 | 来源 | 许可 | 说明 |
|---|---|---|---|
| `web/frontend/src/styles/shadcn-tailwind.css` | npm 包 `shadcn@4.21.1` 的 `dist/tailwind.css` | MIT (Copyright (c) 2023 shadcn) | Tailwind v4 风格工具集（自定义 variant、scroll-fade、shimmer 等）。**未安装 `shadcn` 包**：它是构建期 CLI，其传递依赖（fast-glob / micromatch / ts-morph）带有 high 危漏洞，因此只提取这一份样式文件并在仓库内自行维护。 |

## 2. 前端组件源码（由官方 registry 生成，进入本仓库）

`web/frontend/src/components/ui/*.tsx` 由 shadcn 官方 registry 生成（本 preset 为 `radix-nova`，基座 Radix UI）：
alert、alert-dialog、avatar、badge、breadcrumb、button、calendar、card、chart、checkbox、collapsible、command、
dialog、dropdown-menu、field、input、input-group、label、popover、progress、scroll-area、select、separator、sheet、
sidebar、skeleton、sonner、switch、table、tabs、textarea、tooltip。

| 项目 | 许可 |
|---|---|
| [shadcn/ui](https://github.com/shadcn-ui/ui) | MIT |
| [Radix UI](https://github.com/radix-ui/primitives) | MIT |
| [cmdk](https://github.com/pacocoursey/cmdk) | MIT |
| [Sonner](https://github.com/emilkowalski/sonner) | MIT |
| [Recharts](https://github.com/recharts/recharts) | MIT |
| [date-fns](https://github.com/date-fns/date-fns) | MIT |
| [lucide](https://github.com/lucide-icons/lucide) | ISC |
| [Tailwind CSS](https://github.com/tailwindlabs/tailwindcss) | MIT |
| [Geist 字体](https://github.com/vercel/geist-font)（经 `@fontsource-variable/geist` 自托管） | SIL OFL 1.1 |
| [React](https://github.com/facebook/react) | MIT |
| [TanStack Query / Table](https://github.com/TanStack) | MIT |
| [React Router](https://github.com/remix-run/react-router) | MIT |
| [i18next / react-i18next](https://github.com/i18next/i18next) | MIT |
| [react-hook-form](https://github.com/react-hook-form/react-hook-form) | MIT |
| [zod](https://github.com/colinhacks/zod) | MIT |
| [Vite](https://github.com/vitejs/vite) | MIT |

字体文件（`*.woff2`）随前端构建产物打包进二进制，**不请求任何第三方 CDN**，因此内容安全策略中 `font-src 'self'` 即可满足。

## 3. 后端 Go 依赖

| 模块 | 许可 | 用途 |
|---|---|---|
| [gin](https://github.com/gin-gonic/gin) | MIT | HTTP 路由与中间件 |
| [GORM](https://github.com/go-gorm/gorm) | MIT | ORM 与自动迁移 |
| [glebarez/sqlite](https://github.com/glebarez/sqlite) + go-sqlite | MIT | 纯 Go SQLite 驱动（无 CGO）与内嵌 SQLite 引擎（SQLite 本身为 Public Domain） |
| [golang.org/x/crypto](https://cs.opensource.google/go/x/crypto) | BSD-3-Clause | bcrypt |

## 4. 基础镜像

| 镜像 | 许可/说明 |
|---|---|
| `node:22-alpine` | 仅用于构建阶段，不进入最终镜像 |
| `golang:1.26-alpine` | 仅用于构建阶段，不进入最终镜像 |
| `alpine` + `tzdata` + `ca-certificates` | 运行时镜像；最终镜像不含 Node、npm 与源码 |

---

如需移除或替换以上任一组件（例如换掉字体、去掉图表库），请同步更新本文件与 `README.md` 中的技术栈说明。
