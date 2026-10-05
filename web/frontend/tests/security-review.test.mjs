import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import { test } from "node:test"
import { setTimeout as sleep } from "node:timers/promises"
import ts from "typescript"

// 使用现有 TypeScript 编译器装载纯工具模块，无需额外测试依赖。
async function loadTS(path) {
  const source = await readFile(new URL(path, import.meta.url), "utf8")
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { target: ts.ScriptTarget.ES2023, module: ts.ModuleKind.ESNext },
  })
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString("base64")}`)
}

test("media links reject active protocols and preserve valid attachments", async () => {
  const { safeMediaUrl, resolveMediaUrl } = await loadTS("../src/lib/media.ts")
  for (const raw of ["javascript:alert(1)", "java\nscript:alert(1)", "data:text/html,x", "file:///tmp/x", "//example.test/x", "https://user:pass@example.test/x", "https://example.test/\nx", {}, null]) {
    assert.equal(safeMediaUrl(raw), "")
  }
  assert.equal(safeMediaUrl("https://example.test/image.png"), "https://example.test/image.png")
  assert.equal(resolveMediaUrl({ mediaUrl: "javascript:alert(1)", content: "[图片] https://example.test/x" }), "")
  assert.equal(resolveMediaUrl({ content: "[图片] https://example.test/x filename" }), "https://example.test/x")
})

test("bursts refresh each query once and include analytics and bot status", async () => {
  const { createRealtimeBatch } = await loadTS("../src/lib/realtime-batch.ts")
  const keys = []
  const batch = createRealtimeBatch((key) => keys.push(key), 10)
  for (let i = 0; i < 100; i++) batch.push("ticket.message", "TK-261005-TEST")
  batch.push("stats.invalidated")
  batch.push("bot.status")
  batch.push("ticket.note", "TK-261005-TEST")
  await sleep(30)
  assert.equal(keys.length, 7)
  assert.equal(new Set(keys.map((key) => JSON.stringify(key))).size, 7)
  assert.ok(keys.some((key) => key[0] === "analytics"))
  assert.ok(keys.some((key) => key[0] === "runtime"))
  batch.push("ticket.created", "another")
  batch.dispose()
  await sleep(30)
  assert.equal(keys.length, 7)
})
