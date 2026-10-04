/**
 * 极简 KMarkdown 渲染器：仅用于面板文案的实时预览。
 *
 * 说明：
 * - KOOK 卡片正文按 KMarkdown 渲染，支持加粗、斜体、删除线、下划线、行内代码、
 *   代码块、引用、标题、列表、链接与 (met)/(rol)/(chn) 提及。
 * - 预览只做展示，不追求与 KOOK 客户端 100% 一致；
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
    // 下划线
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

    // 标题
    const heading = /^(#{1,3})\s+(.*)$/.exec(line)
    if (heading) {
      flushParagraph(paragraph)
      const level = heading[1].length
      blocks.push(`<h${level}>${renderInline(heading[2])}</h${level}>`)
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

    // 无序列表
    if (/^\s*[-*]\s+/.test(line)) {
      flushParagraph(paragraph)
      const items: string[] = []
      while (index < lines.length && /^\s*[-*]\s+/.test(lines[index])) {
        items.push(lines[index].replace(/^\s*[-*]\s+/, ""))
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
