import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import { test } from "node:test"
import ts from "typescript"

// 使用现有 TypeScript 编译器装载纯工具模块，无需额外测试依赖。
async function loadTS(path) {
  const source = await readFile(new URL(path, import.meta.url), "utf8")
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { target: ts.ScriptTarget.ES2023, module: ts.ModuleKind.ESNext },
  })
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString("base64")}`)
}

test("blockquote content is rendered, not dropped", async () => {
  const { renderKMarkdown } = await loadTS("../src/lib/kmarkdown.ts")
  // 回归：旧实现的引用分支把收集好的内容丢弃（push 了空字符串），
  // 引用文字在网页上直接消失。
  const html = renderKMarkdown("> 引用第一行\n> 引用第二行\n\n正文")
  assert.ok(html.includes("<blockquote>引用第一行<br/>引用第二行</blockquote>"), html)
  assert.ok(html.includes("<p>正文</p>"), html)
})

test("standalone --- renders a divider, not a literal paragraph", async () => {
  const { renderKMarkdown } = await loadTS("../src/lib/kmarkdown.ts")
  const html = renderKMarkdown("上文\n\n---\n\n下文")
  assert.ok(html.includes("<hr/>"), html)
  assert.ok(!html.includes(">---<"), html)
  // 列表语法不受影响：- 开头带空格仍是列表项。
  const list = renderKMarkdown("- 项目一\n- 项目二")
  assert.ok(list.includes("<ul>"), list)
})

test("spoiler tags render as hidden spans", async () => {
  const { renderKMarkdown } = await loadTS("../src/lib/kmarkdown.ts")
  const html = renderKMarkdown("剧透：(spl)关键剧情(spl) 结束")
  assert.ok(html.includes('<span class="mdk-spoiler" tabindex="0">关键剧情</span>'), html)
})

test("font theme tags render with theme class", async () => {
  const { renderKMarkdown } = await loadTS("../src/lib/kmarkdown.ts")
  const html = renderKMarkdown("(font)重要提示(font)[danger]")
  assert.ok(html.includes('<span class="mdk-font mdk-font-danger">重要提示</span>'), html)
  // 无主题属性时也能渲染为普通 span。
  const plain = renderKMarkdown("(font)普通(font)")
  assert.ok(plain.includes('<span class="mdk-font">普通</span>'), plain)
})

test("escaped markdown characters render literally", async () => {
  const { renderKMarkdown } = await loadTS("../src/lib/kmarkdown.ts")
  const html = renderKMarkdown("\\*不是斜体\\*")
  assert.ok(html.includes("*不是斜体*"), html)
  assert.ok(!html.includes("<em>"), html)
})

test("user text containing old placeholder-like sequences does not corrupt code spans", async () => {
  const { renderKMarkdown } = await loadTS("../src/lib/kmarkdown.ts")
  // 回归：旧实现用固定 \uE000<序号>\uE000 占位，用户文本恰好含该序列时
  // 行内代码还原会错位/复制。
  const html = renderKMarkdown("`a` 和 \uE0000\uE000 以及 `b`")
  assert.ok(html.includes("<code>a</code>"), html)
  assert.ok(html.includes("<code>b</code>"), html)
  assert.equal((html.match(/<code>/g) ?? []).length, 2)
})

test("html in user content stays escaped while markdown still applies", async () => {
  const { renderKMarkdown } = await loadTS("../src/lib/kmarkdown.ts")
  const html = renderKMarkdown('<script>alert("x")</script> **加粗** [链接](https://example.test)')
  assert.ok(!html.includes("<script>"), html)
  assert.ok(html.includes("&lt;script&gt;"), html)
  assert.ok(html.includes("<strong>加粗</strong>"), html)
  assert.ok(html.includes('<a href="https://example.test"'), html)
})

test("paragraph fields keep per-field typing", async () => {
  const { fieldsOf } = await loadTS("../src/lib/kookcard.ts")
  const fields = fieldsOf({
    type: "paragraph",
    fields: [
      { type: "plain-text", content: "字面 * 星号" },
      { type: "kmarkdown", content: "**加粗**" },
    ],
  })
  assert.deepEqual(fields, [
    { content: "字面 * 星号", markdown: false },
    { content: "**加粗**", markdown: true },
  ])
  // 兼容旧形态：字符串与普通 text 元素。
  assert.deepEqual(fieldsOf("纯文本"), [{ content: "纯文本", markdown: false }])
  assert.deepEqual(fieldsOf({ type: "kmarkdown", content: "**x**" }), [
    { content: "**x**", markdown: true },
  ])
})

test("countdown endTime is parsed defensively", async () => {
  const { safeCountdownTime } = await loadTS("../src/lib/kookcard.ts")
  // 合法毫秒时间戳（2026 年）。
  assert.equal(safeCountdownTime(1767225600000), "2026-01-01T00:00:00.000Z")
  // 秒级时间戳（10 位）换算成毫秒。
  assert.equal(safeCountdownTime(1767225600), "2026-01-01T00:00:00.000Z")
  // 数字字符串也接受（JSON 里可能是字符串）。
  assert.equal(safeCountdownTime("1767225600"), "2026-01-01T00:00:00.000Z")
  // 非法值一律返回 null，绝不能抛 RangeError（会炸掉整个详情页）。
  for (const bad of ["not-a-date", "NaN", 0, -1, "0", 1e18, "1e18", null, undefined, {}, "17672256000000000"]) {
    assert.equal(safeCountdownTime(bad), null, `endTime=${JSON.stringify(bad)}`)
  }
})
