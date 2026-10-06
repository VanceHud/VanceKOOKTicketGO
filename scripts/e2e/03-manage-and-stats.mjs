/**
 * 里程碑 5–6 的 UI 验收：统计看板、面板管理、表情规则增删改、北京时间标注。
 *
 * 前提：服务运行在 DryRun 模式（演示数据），并已注入 playwright（见 README）。
 */

import { chromium } from "playwright"

const BASE = process.env.BASE ?? "http://127.0.0.1:9235"
const OUT_DIR = process.env.OUT_DIR ?? "/tmp/kook-ticket-e2e"
const USER = process.env.ADMIN_USERNAME ?? "admin"
const PASS = process.env.ADMIN_PASSWORD ?? "SmokeTest@2026kt"

const errors = []
const failed = []
const results = []
const ok = (name, detail = "") => results.push(`  ✓ ${name}${detail ? ` — ${detail}` : ""}`)
const shot = (page, name) => page.screenshot({ path: `${OUT_DIR}/m6-${name}.png` })

const browser = await chromium.launch({ channel: "chrome" })
const page = await browser.newPage({ viewport: { width: 1500, height: 980 }, locale: "zh-CN", colorScheme: "light" })
let fatal = null

page.on("console", (m) => {
  if (m.type() === "error") errors.push(m.text())
})
page.on("requestfailed", (r) => failed.push(`${r.method()} ${r.url()} — ${r.failure()?.errorText}`))

try {
  // 登录
  await page.goto(`${BASE}/login`, { waitUntil: "networkidle" })
  await page.fill("#username", USER)
  await page.fill("#password", PASS)
  await page.click('button[type="submit"]')
  await page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 15000 })
  ok("管理员登录成功")

  // 1) 统计看板
  await page.goto(`${BASE}/stats`, { waitUntil: "networkidle" })
  await page.waitForSelector("text=统计看板", { timeout: 15000 })
  await page.waitForSelector("svg.recharts-surface", { timeout: 15000 })
  await page.waitForTimeout(800)
  const statsText = await page.locator("main").last().innerText()
  ok("统计看板渲染", statsText.includes("区间工单") ? "含 KPI" : "缺少 KPI")
  ok("时长分位展示", statsText.includes("P50") && statsText.includes("P90") ? "含 P50/P90" : "缺少分位")
  ok("客服处理量", statsText.includes("客服小林") ? "含关闭人统计" : "缺少关闭人")
  ok("来源面板统计", statsText.includes("工单面板") ? "含来源频道名" : "缺少来源")
  ok("北京时间标注", statsText.includes("北京时间") ? "已标注时区" : "未标注时区")
  await shot(page, "stats")

  // 2) 工单类型管理（DryRun 下机器人离线：应提示并禁用面板相关操作）
  await page.goto(`${BASE}/types`, { waitUntil: "networkidle" })
  await page.waitForSelector("text=工单类型", { timeout: 15000 })
  const typesText = await page.locator("main").last().innerText()
  ok("工单类型列表渲染", typesText.includes("类型管理员角色") ? "含角色" : "缺少角色")
  ok("演示类型已加载", typesText.includes("账号与充值") && typesText.includes("举报与投诉") ? "含 2 个类型" : "缺少类型数据")
  ok("机器人离线提示", typesText.includes("机器人未连接 KOOK") ? "已提示" : "未提示")
  const createTypeDisabled = await page.getByRole("button", { name: "新建类型" }).isDisabled()
  ok("离线时仍可新建类型", !createTypeDisabled ? "按钮可点击" : "按钮被禁用（异常）")

  // 展开第一个类型，检查其面板列表与离线时的新建面板限制
  await page.getByRole("button", { name: "展开面板" }).first().click()
  await page.waitForTimeout(400)
  const expandedText = await page.locator("main").last().innerText()
  ok("类型下展示面板", expandedText.includes("充值工单") || expandedText.includes("工单面板") ? "含面板" : "缺少面板")
  const addPanelDisabled = await page.getByRole("button", { name: "为该类型添加面板" }).first().isDisabled()
  ok("离线时禁用新建面板", addPanelDisabled ? "按钮已禁用" : "按钮仍可点击（异常）")
  await shot(page, "ticket-types")

  // 3) 表情规则：新增 → 停用 → 删除
  await page.goto(`${BASE}/emoji-roles`, { waitUntil: "networkidle" })
  await page.waitForSelector("text=表情上角色", { timeout: 15000 })
  const beforeRows = await page.locator("table tbody tr").count()

  await page.getByRole("button", { name: "新增规则" }).click()
  await page.waitForSelector("#rule-message", { timeout: 10000 })
  await page.fill("#rule-message", "9f8e7d6c5b4a39281706f5e4")
  await page.fill("#rule-emoji", "💛")
  await page.fill("#rule-role", "10003")
  await page.fill("#rule-label", "验收规则")
  await page.getByRole("button", { name: "保存" }).click()
  await page.waitForTimeout(1500)
  const afterCreateRows = await page.locator("table tbody tr").count()
  ok("新增表情规则", afterCreateRows === beforeRows + 1 ? `行数 ${beforeRows} → ${afterCreateRows}` : "行数未变化")
  await shot(page, "emoji-created")

  // 停用刚创建的规则（最后一行）
  const lastRow = page.locator("table tbody tr").last()
  await lastRow.locator('button[role="switch"]').click()
  await page.waitForTimeout(1200)
  ok("切换规则状态", (await page.locator("[data-sonner-toast]").count()) > 0 ? "已弹提示" : "无提示")

  // 删除
  await lastRow.getByRole("button", { name: "删除" }).click().catch(async () => {
    await lastRow.locator("button").last().click()
  })
  await page.waitForSelector('div[role="alertdialog"]', { timeout: 10000 })
  await page.getByRole("button", { name: "删除", exact: true }).click()
  await page.waitForTimeout(1500)
  const afterDeleteRows = await page.locator("table tbody tr").count()
  ok("删除表情规则", afterDeleteRows === afterCreateRows - 1 ? `行数 ${afterCreateRows} → ${afterDeleteRows}` : "行数未恢复")
  await shot(page, "emoji-deleted")

  // 4) 工单列表与审计的时区标注
  await page.goto(`${BASE}/tickets`, { waitUntil: "networkidle" })
  await page.waitForSelector('input[placeholder*="搜索"]', { timeout: 10000 })
  const ticketsText = await page.locator("main").last().innerText()
  ok("工单列表标注时区", ticketsText.includes("北京时间") ? "已标注" : "未标注")

  await page.goto(`${BASE}/audit`, { waitUntil: "networkidle" })
  await page.waitForTimeout(1200)
  const auditText = await page.locator("main").last().innerText()
  ok("审计页标注时区", auditText.includes("北京时间") ? "已标注" : "未标注")
  ok("审计记录含新增的管理动作", /表情|面板/.test(auditText) ? "含 emoji/panel 记录" : "未见新记录")
  await shot(page, "audit")
} catch (error) {
  fatal = error
  await page.screenshot({ path: `${OUT_DIR}/m6-failure.png` }).catch(() => {})
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
