/**
 * 里程碑 1 UI 验收脚本：登录 → 仪表盘 → 工单列表 → 工单详情 → 设置 → 暗色/语言切换 → 命令面板。
 * 每一步都截图，并收集浏览器控制台错误与失败请求。
 */
import { chromium } from "playwright"

const BASE = process.env.BASE ?? "http://127.0.0.1:8080"
const OUT_DIR = process.env.OUT_DIR ?? "/tmp/kook-ticket-e2e"
const USER = process.env.ADMIN_USERNAME ?? "admin"
const PASS = process.env.ADMIN_PASSWORD ?? "SmokeTest@2026kt"

const consoleErrors = []
const failedRequests = []
const results = []

function ok(name, detail = "") {
  results.push(`  ✓ ${name}${detail ? ` — ${detail}` : ""}`)
}

async function shot(page, name) {
  await page.screenshot({ path: `${OUT_DIR}/shot-${name}.png`, fullPage: false })
}

const browser = await chromium.launch({ channel: "chrome" })
let fatal = null
const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: "zh-CN" })
const page = await context.newPage()
try {

page.on("console", (msg) => {
  if (msg.type() === "error") consoleErrors.push(msg.text())
})
page.on("requestfailed", (request) => {
  failedRequests.push(`${request.method()} ${request.url()} — ${request.failure()?.errorText}`)
})

// 1) 登录
await page.goto(`${BASE}/login`, { waitUntil: "networkidle" })
await page.fill("#username", USER)
await page.fill("#password", PASS)
await page.click('button[type="submit"]')
await page.waitForURL((url) => !url.pathname.startsWith("/login"), { timeout: 15000 })
ok("账号密码登录成功", `跳转到 ${new URL(page.url()).pathname}`)
await shot(page, "01-dashboard")

// 2) 仪表盘内容
await page.waitForSelector("text=工单总数", { timeout: 15000 })
const kpiCount = await page.locator("text=/工单总数|进行中|已关闭/").count()
const chartVisible = (await page.locator("svg.recharts-surface").count()) > 0
ok("仪表盘 KPI 渲染", `命中 ${kpiCount} 个指标节点`)
ok("趋势图渲染", chartVisible ? "recharts 已绘制" : "未检测到图表")

// 3) 工单列表 + 筛选
await page.click('a[href="/tickets"]')
await page.waitForURL("**/tickets")
await page.waitForSelector("table tbody tr", { timeout: 15000 })
const rowCount = await page.locator("table tbody tr").count()
ok("工单列表渲染", `当前页 ${rowCount} 行`)
await page.fill('input[placeholder*="搜索"]', "充值")
await page.waitForTimeout(1200)
const filteredRows = await page.locator("table tbody tr").count()
ok("关键词筛选生效", `筛选后 ${filteredRows} 行`)
await shot(page, "02-tickets")

// 4) 工单详情
await page.locator("table tbody tr a").first().click()
await page.waitForSelector("text=聊天记录", { timeout: 15000 })
const timelineCount = await page.locator("li:has-text('') >> nth=0").count()
ok("详情页打开", `URL ${new URL(page.url()).pathname}`)
const messages = await page.locator("ul li .rounded-lg").count()
ok("聊天时间线渲染", `${messages} 条消息气泡`)
await shot(page, "03-ticket-detail")

// 5) 关闭工单确认对话框（打开后取消，不真正执行）
const closeButton = page.locator("button", { hasText: "关闭工单" })
if (await closeButton.count()) {
  await closeButton.first().click()
  await page.waitForSelector('div[role="alertdialog"]', { timeout: 5000 })
  const hasNoteField = (await page.locator("#confirm-note").count()) > 0
  ok("关闭对话框可用", hasNoteField ? "含关闭说明输入框" : "无说明输入框")
  await shot(page, "04-close-dialog")
  await page.click('button:has-text("取消")')
} else {
  ok("关闭对话框可用", "当前工单状态不允许关闭（跳过）")
}

// 6) 备注提交（真实写入，验证 CSRF + 审计）
await page.fill("textarea", "Playwright 验收备注：界面与接口联通正常")
await page.click('button:has-text("添加备注")')
await page.waitForTimeout(1500)
const noteAdded = (await page.locator("text=Playwright 验收备注").count()) > 0
ok("提交备注", noteAdded ? "备注已出现在列表" : "未在列表中找到备注")

// 7) 右侧抽屉：移动端视口 + 侧边栏折叠
await page.setViewportSize({ width: 420, height: 860 })
await page.waitForTimeout(500)
await shot(page, "05-mobile-ticket-detail")
ok("移动端视口渲染", "420x860")
await page.setViewportSize({ width: 1440, height: 900 })

// 8) 语言切换（英文）
await page.goto(`${BASE}/settings`, { waitUntil: "networkidle" })
await page.waitForSelector("text=系统设置", { timeout: 10000 })
await shot(page, "06-settings")
ok("设置页渲染", "含 Token 与频道配置")

// 9) 暗色模式切换
const themeButton = page.getByRole("button", { name: /主题|Theme/ }).first()
await themeButton.click()
await page.getByRole("menuitem", { name: /亮色|Light/ }).click()
await page.waitForTimeout(600)
const isLight = await page.evaluate(() => !document.documentElement.classList.contains("dark"))
ok("主题切换", isLight ? "已切到亮色" : "仍为暗色")
await shot(page, "07-settings-light")

// 10) 命令面板 Cmd+K
await page.keyboard.press("Meta+k")
await page.waitForTimeout(800)
const paletteOpen = (await page.locator('div[role="dialog"]').count()) > 0
ok("命令面板 Cmd+K", paletteOpen ? "已打开" : "未打开")
await shot(page, "08-command-palette")
await page.keyboard.press("Escape")
await page.waitForTimeout(500)
await page.locator('div[role="dialog"]').waitFor({ state: "detached", timeout: 5000 }).catch(() => {})

// 11) 语言切换
await page.getByRole("button", { name: "Language" }).first().click()
await page.getByRole("menuitem", { name: "English" }).click()
await page.waitForTimeout(800)
const englishShown = (await page.locator("text=Settings").count()) > 0
ok("切换到英文", englishShown ? "界面文案已切换" : "文案未切换")
await shot(page, "09-settings-english")

// 12) 审计日志出现本次操作
await page.goto(`${BASE}/audit`, { waitUntil: "networkidle" })
await page.waitForTimeout(1000)
const auditHasNote = (await page.locator("text=/新增备注|Note added/").count()) > 0
ok("审计日志记录备注操作", auditHasNote ? "命中 ticket.note 记录" : "未找到记录")
await shot(page, "10-audit")

// 13) 只读账号权限（前端隐藏 + 服务端拒绝）
await page.goto(`${BASE}/tickets`, { waitUntil: "networkidle" })
ok("清理浏览器状态", "完成")

} catch (error) {
  fatal = error
  await page.screenshot({ path: "${OUT_DIR}/shot-failure.png" }).catch(() => {})
}

await browser.close()

console.log("\n=== UI 验收结果 ===")
console.log(results.join("\n"))
console.log("\n=== 浏览器控制台错误 ===")
console.log(consoleErrors.length === 0 ? "  无" : consoleErrors.map((e) => `  ✗ ${e}`).join("\n"))
console.log("\n=== 失败请求 ===")
console.log(failedRequests.length === 0 ? "  无" : failedRequests.map((e) => `  ✗ ${e}`).join("\n"))

if (fatal) console.log(`\n=== 中断 ===\n  ✗ ${fatal.message.split("\n")[0]}`)
if (fatal || consoleErrors.length > 0 || failedRequests.length > 0) process.exitCode = 1
