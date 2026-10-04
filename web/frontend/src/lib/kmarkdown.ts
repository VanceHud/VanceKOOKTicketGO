/**
 * 极简 KMarkdown 渲染器：仅用于面板文案的实时预览。
 *
 * 说明：
 * - KOOK 的 KMarkdown 只支持 **加粗**、*斜体*、~~删除线~~、`行内代码`、```代码块```、
 *   > 引用、--- 分隔线、[链接](url)，以及 (ins)/(met)/(rol)/(chn) 等自定义标签；
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

/** renderInline 处理行内标记（加粗、斜体、代码、链接、提及等）。 */
function renderInline(text: string): string {
  let out = escapeHtml(text)

  // 行内代码先取出占位，避免其中的 * _ ~ 被当作格式标记。
  // 占位符使用 Unicode 私用区字符，正常文案不会出现。
  const codeSpans: string[] = []
  out = out.replace(/`([^`\n]+)`/g, (_match, code: string) => {
    codeSpans.push(`<code>${code}</code>`)
    return `\uE000${codeSpans.length - 1}\uE000`
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
    // 提及
    .replace(/\(met\)\s*([^)\s]+)\s*\(met\)/g, '<span class="mdk-mention">@$1</span>')
    .replace(/\(rol\)\s*([^)\s]+)\s*\(rol\)/g, '<span class="mdk-mention">@角色$1</span>')
    .replace(/\(chn\)\s*([^)\s]+)\s*\(chn\)/g, '<span class="mdk-mention">#频道$1</span>')

  // 还原行内代码。
  out = out.replace(/\uE000(\d+)\uE000/g, (_match, idx: string) => codeSpans[Number(idx)] ?? "")
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

    // 引用
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
