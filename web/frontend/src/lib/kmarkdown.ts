/**
 * 极简 KMarkdown 渲染器：用于工单聊天记录展示与面板文案的实时预览。
 *
 * 说明：
 * - 覆盖 KOOK 官方文档声明的语法：**加粗**、*斜体*、~~删除线~~、`行内代码`、
 *   ```代码块```、> 引用、--- 分隔线、[链接](url)、(ins) 下划线、(spl) 剧透、
 *   (font) 主题色 (font)[theme]（官方限定卡片内可用，卡片文本走本渲染器）、
 *   (met)/(rol)/(chn) 提及、`\` 转义；
 *   `# 标题`、`__下划线__`、`- 列表` 这些 Markdown 写法 KOOK 并不认识。
 * - 后端发送前会做一次归一化（internal/kook/kmd.go）：首行 `# 标题` 改用卡片 header
 *   模块，`__下划线__` 转成 (ins)，`- 列表` 转成「• 」。
 * - 因此这里的渲染规则要和后端保持一致，保证「预览即所得」。
 * - 所有文本先做 HTML 转义，再套用标记，避免把用户输入当作 HTML 执行。
 */

/** escapeHtml 把文本中的 HTML 特殊字符转义。 */
function escapeHtml(input: string): string {
  return input
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;")
}

/** makeMark 生成一次行内渲染内唯一的占位符标记（私用区字符 + 随机串）。
 *
 * 旧实现用固定的 `\uE000<序号>\uE000`：用户文本恰好包含该序列时，
 * 行内代码的还原会错位或重复。随机前缀让碰撞概率忽略不计。 */
function makeMark(): string {
  return `\uE000${Math.random().toString(36).slice(2, 10)}\uE001`
}

/** renderInline 处理行内标记（加粗、斜体、代码、链接、提及、剧透、主题色等）。 */
function renderInline(text: string): string {
  let out = escapeHtml(text)

  // 行内代码先取出占位，避免其中的 * _ ~ ( 等被当作格式标记。
  const codeMark = makeMark()
  const codeSpans: string[] = []
  out = out.replace(/`([^`\n]+)`/g, (_match, code: string) => {
    codeSpans.push(`<code>${code}</code>`)
    return `${codeMark}${codeSpans.length - 1}${codeMark}`
  })

  // 转义（官方语法 `\特殊字符`）：被转义的字符按字面渲染，不参与标记解析。
  const escapeMark = makeMark()
  const escapedChars: string[] = []
  out = out.replace(/\\([\\`*_~[\]()])/g, (_match, ch: string) => {
    escapedChars.push(ch)
    return `${escapeMark}${escapedChars.length - 1}${escapeMark}`
  })

  out = out
    // 链接
    .replace(
      /\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\)/g,
      '<a href="$2" target="_blank" rel="noreferrer noopener">$1</a>',
    )
    // 加粗
    .replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>")
    // 下划线：KOOK 原生写法 (ins) 与 Markdown 写法 __ 都会渲染成下划线
    // （后端会把 __文本__ 自动转换成 (ins)文本(ins)，这里同步展示）。
    .replace(/\(ins\)([^(\n]+)\(ins\)/g, "<u>$1</u>")
    .replace(/__([^_\n]+)__/g, "<u>$1</u>")
    // 删除线
    .replace(/~~([^~\n]+)~~/g, "<del>$1</del>")
    // 斜体
    .replace(/(^|[^*])\*([^*\n]+)\*/g, "$1<em>$2</em>")
    // 剧透：默认隐藏内容，点击揭示（KMarkdownPreview 用事件委托切换揭示状态）。
    .replace(/\(spl\)([^(\n]+)\(spl\)/g, '<span class="mdk-spoiler" tabindex="0">$1</span>')
    // 主题色：官方语法 (font)内容(font)[theme]，theme 限定字母（防 class 注入），
    // 未识别的主题只是没有对应颜色，不影响展示。
    .replace(/\(font\)([^(\n]+)\(font\)(?:\[([a-zA-Z]+)\])?/g, (_match, content: string, theme: string) => {
      const cls = theme ? `mdk-font mdk-font-${theme}` : "mdk-font"
      return `<span class="${cls}">${content}</span>`
    })
    // 提及
    .replace(/\(met\)\s*([^)\s]+)\s*\(met\)/g, '<span class="mdk-mention">@$1</span>')
    .replace(/\(rol\)\s*([^)\s]+)\s*\(rol\)/g, '<span class="mdk-mention">@角色$1</span>')
    .replace(/\(chn\)\s*([^)\s]+)\s*\(chn\)/g, '<span class="mdk-mention">#频道$1</span>')

  // 还原转义字符与行内代码。
  out = out
    .replace(new RegExp(`${escapeMark}(\\d+)${escapeMark}`, "g"), (_match, idx: string) => escapedChars[Number(idx)] ?? "")
    .replace(new RegExp(`${codeMark}(\\d+)${codeMark}`, "g"), (_match, idx: string) => codeSpans[Number(idx)] ?? "")
  return out
}

/** renderKMarkdown 把 KMarkdown 源文本渲染为可安全插入的 HTML 字符串。 */
export function renderKMarkdown(source: string): string {
  const normalized = source.replace(/\r\n/g, "\n")
  if (normalized.trim() === "") {
    return ""
  }

  const lines = normalized.split("\n")
  const blocks: string[] = []
  let index = 0

  const flushParagraph = (buffer: string[]) => {
    if (buffer.length > 0) {
      blocks.push(`<p>${buffer.map(renderInline).join("<br/>")}</p>`)
      buffer.length = 0
    }
  }

  const paragraph: string[] = []

  while (index < lines.length) {
    const line = lines[index]

    // 代码块
    if (line.trimStart().startsWith("```")) {
      flushParagraph(paragraph)
      index += 1
      const body: string[] = []
      while (index < lines.length && !lines[index].trimStart().startsWith("```")) {
        body.push(lines[index])
        index += 1
      }
      index += 1 // 跳过结束围栏
      blocks.push(`<pre><code>${escapeHtml(body.join("\n"))}</code></pre>`)
      continue
    }

    // 空行
    if (line.trim() === "") {
      flushParagraph(paragraph)
      index += 1
      continue
    }

    // 标题：只有正文首行会显示为卡片大标题。
    // KOOK 的 KMarkdown 没有 "# 标题" 语法，后端会把首行标题放进卡片的 header 模块；
    // 出现在其它位置、或后面没有任何正文时会被降级为加粗（卡片必须保留内容模块），
    // 这里与 KOOK 实际渲染保持一致。
    const heading = /^(#{1,6})\s+(.+)$/.exec(line)
    if (heading) {
      flushParagraph(paragraph)
      const content = renderInline(heading[2])
      const hasBody = lines.slice(index + 1).some((rest) => rest.trim() !== "")
      blocks.push(blocks.length === 0 && hasBody ? `<h1>${content}</h1>` : `<p><strong>${content}</strong></p>`)
      index += 1
      continue
    }

    // 分隔线：独立成行的 ---（官方 KMarkdown 语法）。
    if (/^\s*-{3,}\s*$/.test(line)) {
      flushParagraph(paragraph)
      blocks.push(`<hr/>`)
      index += 1
      continue
    }

    // 引用（官方语法：`> 引用内容`，连续引用行合并为一块，空行结束）。
    if (/^\s*>\s?/.test(line)) {
      flushParagraph(paragraph)
      const quoted: string[] = []
      while (index < lines.length && /^\s*>\s?/.test(lines[index])) {
        quoted.push(lines[index].replace(/^\s*>\s?/, ""))
        index += 1
      }
      blocks.push(`<blockquote>${quoted.map(renderInline).join("<br/>")}</blockquote>`)
      continue
    }

    // 无序列表（KOOK 会渲染成「• 项目」，视觉上就是列表）
    if (/^\s*[-*+]\s+/.test(line)) {
      flushParagraph(paragraph)
      const items: string[] = []
      while (index < lines.length && /^\s*[-*+]\s+/.test(lines[index])) {
        items.push(lines[index].replace(/^\s*[-*+]\s+/, ""))
        index += 1
      }
      blocks.push(`<ul>${items.map((item) => `<li>${renderInline(item)}</li>`).join("")}</ul>`)
      continue
    }

    // 有序列表
    if (/^\s*\d+\.\s+/.test(line)) {
      flushParagraph(paragraph)
      const items: string[] = []
      while (index < lines.length && /^\s*\d+\.\s+/.test(lines[index])) {
        items.push(lines[index].replace(/^\s*\d+\.\s+/, ""))
        index += 1
      }
      blocks.push(`<ol>${items.map((item) => `<li>${renderInline(item)}</li>`).join("")}</ol>`)
      continue
    }

    paragraph.push(line)
    index += 1
  }

  flushParagraph(paragraph)
  return blocks.join("")
}
