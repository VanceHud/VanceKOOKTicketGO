// 一次性脚本：为 README 生成界面截图。
//
// 用法（需先以 DryRun 模式启动服务）：
//   npm install playwright                      # 仅需一次，不会写入项目依赖
//   KOOK_DRYRUN=1 ADMIN_PASSWORD='DemoTicket@2026' PORT=9235 go run ./cmd/server &
//   ADMIN_PASSWORD='DemoTicket@2026' node scripts/screenshots.mjs
//
// 可用环境变量：BASE（服务地址，默认 http://127.0.0.1:9235）、ADMIN_PASSWORD。
// 产物写入 docs/screenshots/*.png。不要把它当作回归测试使用，
// UI 验收请走 scripts/e2e/。

import { mkdir } from "node:fs/promises"
import path from "node:path"
import { chromium } from "playwright"

const BASE = process.env.BASE ?? "http://127.0.0.1:9235"
const PASSWORD = process.env.ADMIN_PASSWORD ?? "DemoTicket@2026"
const OUT = path.resolve("docs/screenshots")

const VIEWPORT = { width: 1440, height: 900 }

async function main() {
  await mkdir(OUT, { recursive: true })

  const browser = await chromium.launch()
  const context = await browser.newContext({
    viewport: VIEWPORT,
    deviceScaleFactor: 2,
    locale: "zh-CN",
    timezoneId: "Asia/Shanghai",
    colorScheme: "light",
  })
  const page = await context.newPage()

  // 固定浅色主题，避免截图受系统偏好影响。
  await page.addInitScript(() => {
    localStorage.setItem("theme", "light")
  })

  const shot = async (name, { full = false } = {}) => {
    await page.waitForTimeout(500)
    await page.screenshot({ path: path.join(OUT, `${name}.png`), fullPage: full })
    console.log(`  ✓ ${name}.png`)
  }

  // ---- 登录页 ----
  console.log("登录页…")
  await page.goto(`${BASE}/login`, { waitUntil: "networkidle" })
  await shot("login")

  // ---- 登录 ----
  await page.fill('input[name="username"], input#username', "admin")
  await page.fill('input[type="password"]', PASSWORD)
  await page.click('button[type="submit"]')
  await page.waitForURL((u) => !u.pathname.includes("login"), { timeout: 20000 })
  await page.waitForLoadState("networkidle")
  console.log("已登录")

  // ---- 仪表盘 ----
  console.log("仪表盘…")
  await page.goto(`${BASE}/`, { waitUntil: "networkidle" })
  await shot("dashboard", { full: true })

  // ---- 工单列表 ----
  console.log("工单列表…")
  await page.goto(`${BASE}/tickets`, { waitUntil: "networkidle" })
  await shot("tickets")

  // ---- 工单详情（挑一个消息最多的工单）----
  console.log("工单详情…")
  const firstTicket = page.locator('a[href^="/tickets/TK-"]').first()
  if (await firstTicket.count()) {
    await firstTicket.click()
    await page.waitForLoadState("networkidle")
    await page.waitForTimeout(800)
    await shot("ticket-detail", { full: true })
  } else {
    console.warn("  ! 未找到工单链接，跳过详情页")
  }

  // ---- 统计看板 ----
  console.log("统计看板…")
  await page.goto(`${BASE}/stats`, { waitUntil: "networkidle" })
  await shot("stats", { full: true })

  // ---- 面板管理 ----
  console.log("面板管理…")
  await page.goto(`${BASE}/panels`, { waitUntil: "networkidle" })
  await shot("panels")

  // ---- 表情上角色 ----
  console.log("表情上角色…")
  await page.goto(`${BASE}/emoji-roles`, { waitUntil: "networkidle" })
  await shot("emoji-roles")

  // ---- 机器人状态 ----
  console.log("机器人状态…")
  await page.goto(`${BASE}/bot`, { waitUntil: "networkidle" })
  await shot("bot-status")

  // ---- 机器人动态 ----
  console.log("机器人动态…")
  await page.goto(`${BASE}/activity`, { waitUntil: "networkidle" })
  await shot("activity")

  // ---- 系统设置 ----
  console.log("系统设置…")
  await page.goto(`${BASE}/settings`, { waitUntil: "networkidle" })
  await shot("settings")

  // ---- 审计日志 ----
  console.log("审计日志…")
  await page.goto(`${BASE}/audit`, { waitUntil: "networkidle" })
  await shot("audit")

  // ---- 用户管理 ----
  console.log("用户管理…")
  await page.goto(`${BASE}/users`, { waitUntil: "networkidle" })
  await shot("users")

  await browser.close()
  console.log(`\n截图已写入 ${OUT}`)
}

main().catch((err) => {
  console.error(err)
  process.exit(1)
})
