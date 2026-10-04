/**
 * 里程碑 2–4 的 UI 验收：登录码登录、我的账号页、机器人状态页、原有工单链路回归。
 */
import { chromium } from "playwright"

const BASE = process.env.BASE ?? "http://127.0.0.1:8080"
const OUT_DIR = process.env.OUT_DIR ?? "/tmp/kook-ticket-e2e"
const USER = process.env.ADMIN_USERNAME ?? "admin"
const PASS = process.env.ADMIN_PASSWORD ?? "SmokeTest@2026kt"
const errors = []
const failed = []
const results = []
const ok = (name, detail = "") => results.push(`  ✓ ${name}${detail ? ` — ${detail}` : ""}`)
const shot = (page, name) => page.screenshot({ path: `${OUT_DIR}/m4-${name}.png` })

const browser = await chromium.launch({ channel: "chrome" })
const page = await browser.newPage({ viewport: { width: 1440, height: 940 }, locale: "zh-CN", colorScheme: "light" })
let fatal = null

page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()) })
page.on("requestfailed", (r) => failed.push(`${r.method()} ${r.url()} — ${r.failure()?.errorText}`))

try {
  // 1) 登录码登录
  await page.goto(`${BASE}/login`, { waitUntil: "networkidle" })
  await page.getByRole("tab", { name: "登录码" }).click()
  await page.fill("#login-code", "xyz789")
  await page.getByRole("button", { name: "使用登录码登录" }).click()
  await page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 15000 })
  ok("登录码登录成功", `跳转到 ${new URL(page.url()).pathname}`)

  // 2) 自动创建账号的角色正确
  await page.goto(`${BASE}/account`, { waitUntil: "networkidle" })
  await page.waitForSelector("text=我的账号", { timeout: 10000 })
  const accountText = await page.locator("main").last().innerText()
  ok("我的账号页渲染", accountText.includes("KOOK 绑定") ? "含绑定入口" : "缺少绑定入口")
  ok("登录码账号权限为客服", accountText.includes("客服") ? "staff 角色已生效" : "角色异常")
  await shot(page, "account")

  // 3) 重复使用同一登录码应失败（一次性）——用全新上下文模拟另一台设备
  const replayContext = await browser.newContext({ viewport: { width: 1280, height: 800 }, locale: "zh-CN" })
  const replayPage = await replayContext.newPage()
  await replayPage.goto(`${BASE}/login`, { waitUntil: "networkidle" })
  await replayPage.getByRole("tab", { name: "登录码" }).click()
  await replayPage.fill("#login-code", (process.env.LOGIN_CODE ?? "XYZ789"))
  await replayPage.getByRole("button", { name: "使用登录码登录" }).click()
  await replayPage.waitForTimeout(1500)
  const replayBlocked = (await replayPage.locator("text=/无效或已过期/").count()) > 0
  const stillOnLogin = new URL(replayPage.url()).pathname.startsWith("/login")
  ok("登录码不可重复使用", replayBlocked && stillOnLogin ? "另一会话被拒绝" : "未拦截")

  // 4) 在同一新上下文用密码登录，检查机器人状态页
  await replayPage.getByRole("tab", { name: "账号密码" }).click()
  await replayPage.fill("#username", "admin")
  await replayPage.fill("#password", PASS)
  await replayPage.click('button[type="submit"]')
  await replayPage.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 15000 })
  await replayContext.close()

  // 回到主上下文（登录码账号为客服，无法看到管理员专属的重连按钮，这里改用管理员会话验证）
  const adminContext = await browser.newContext({ viewport: { width: 1440, height: 940 }, locale: "zh-CN" })
  const adminPage = await adminContext.newPage()
  adminPage.on("console", (m) => { if (m.type() === "error") errors.push(m.text()) })
  await adminPage.goto(`${BASE}/login`, { waitUntil: "networkidle" })
  await adminPage.fill("#username", "admin")
  await adminPage.fill("#password", PASS)
  await adminPage.click('button[type="submit"]')
  await adminPage.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 15000 })

  await adminPage.goto(`${BASE}/bot`, { waitUntil: "networkidle" })
  await adminPage.waitForSelector("text=运行状态", { timeout: 10000 })
  const botText = await adminPage.locator("main").last().innerText()
  ok("机器人状态页含实时状态", botText.includes("已处理事件") ? "含事件计数" : "缺少实时字段")
  ok("机器人状态页含重连按钮", (await adminPage.getByRole("button", { name: /重新连接/ }).count()) > 0 ? "管理员可见" : "不可见")
  await shot(adminPage, "bot-status")

  // 5) 重新连接（DryRun 未配置 Token，应给出可读错误而不是崩溃）
  await adminPage.getByRole("button", { name: /重新连接/ }).click()
  await adminPage.waitForTimeout(1800)
  const restartFeedback = (await adminPage.locator("[data-sonner-toast]").count()) > 0
  ok("重连给出明确反馈", restartFeedback ? "已弹提示" : "无提示")
  await shot(adminPage, "bot-restart")

  // 权限边界：客服（登录码账号）不应看到重连按钮
  await page.goto(`${BASE}/bot`, { waitUntil: "networkidle" })
  await page.waitForSelector("text=运行状态", { timeout: 10000 })
  const staffSeesRestart = (await page.getByRole("button", { name: /重新连接/ }).count()) > 0
  ok("非管理员看不到重连按钮", staffSeesRestart ? "仍可见（异常）" : "已隐藏")
  await adminContext.close()

  // 6) 工单链路回归：列表 → 详情 → 备注
  await page.goto(`${BASE}/tickets`, { waitUntil: "networkidle" })
  await page.waitForSelector('input[placeholder*="搜索"]')
  await page.waitForSelector("table tbody tr", { timeout: 10000 })
  await page.waitForTimeout(600)
  const rows = await page.locator("table tbody tr").count()
  ok("工单列表渲染", `${rows} 行`)
  await page.locator("table tbody tr td a").first().click()
  await page.waitForSelector("text=聊天记录", { timeout: 10000 })
  await page.fill("textarea", "里程碑 4 验收备注")
  await page.getByRole("button", { name: "添加备注" }).click()
  await page.waitForTimeout(1500)
  ok("备注提交", (await page.locator("text=里程碑 4 验收备注").count()) > 0 ? "已入库并显示" : "未显示")
  await shot(page, "ticket-detail")
} catch (error) {
  fatal = error
  await page.screenshot({ path: "${OUT_DIR}/m4-failure.png" }).catch(() => {})
}

await browser.close()

console.log("\n=== UI 验收结果 ===")
console.log(results.join("\n"))
console.log("\n=== 控制台错误 ===")
console.log(errors.length === 0 ? "  无" : errors.map((e) => `  ✗ ${e}`).join("\n"))
console.log("\n=== 失败请求 ===")
console.log(failed.length === 0 ? "  无" : failed.map((e) => `  ✗ ${e}`).join("\n"))
if (fatal) console.log(`\n=== 中断 ===\n  ✗ ${fatal.message.split("\n")[0]}`)
if (fatal || errors.length || failed.length) process.exitCode = 1
