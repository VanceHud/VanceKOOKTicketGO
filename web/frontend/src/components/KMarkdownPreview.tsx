/**
 * KMarkdown 预览（工单消息与面板文案共用）。
 *
 * renderKMarkdown 已经对输入做了 HTML 转义，因此这里可以安全地使用
 * dangerouslySetInnerHTML 展示结果。
 */

import { memo, useMemo, type MouseEvent } from "react"

import { renderKMarkdown } from "@/lib/kmarkdown"
import { cn } from "@/lib/utils"

const previewStyles = [
  "text-sm leading-relaxed break-words",
  "[&_p]:my-1",
  "[&_strong]:font-semibold",
  "[&_em]:italic",
  "[&_del]:line-through",
  "[&_u]:underline",
  "[&_h1]:mt-1 [&_h1]:mb-2 [&_h1]:text-base [&_h1]:font-semibold [&_h1]:leading-snug",
  "[&_ul]:my-1 [&_ul]:list-disc [&_ul]:pl-5",
  "[&_ol]:my-1 [&_ol]:list-decimal [&_ol]:pl-5",
  "[&_blockquote]:my-2 [&_blockquote]:border-l-2 [&_blockquote]:border-border [&_blockquote]:pl-2 [&_blockquote]:text-muted-foreground",
  "[&_a]:text-primary [&_a]:underline [&_a]:underline-offset-2",
  "[&_.mdk-mention]:rounded [&_.mdk-mention]:bg-primary/10 [&_.mdk-mention]:px-1 [&_.mdk-mention]:text-primary",
  "[&_code]:rounded [&_code]:bg-muted [&_code]:px-1 [&_code]:py-0.5 [&_code]:font-mono [&_code]:text-xs",
  "[&_pre]:my-2 [&_pre]:overflow-x-auto [&_pre]:rounded-md [&_pre]:bg-muted [&_pre]:p-2",
  "[&_pre_code]:bg-transparent [&_pre_code]:p-0",
  "[&_hr]:my-2 [&_hr]:border-0 [&_hr]:border-t [&_hr]:border-border",
  // 剧透：默认文字透明 + 灰底，点击（或键盘聚焦后回车）切换揭示状态。
  "[&_.mdk-spoiler]:cursor-pointer [&_.mdk-spoiler]:select-none [&_.mdk-spoiler]:rounded [&_.mdk-spoiler]:bg-muted [&_.mdk-spoiler]:px-1 [&_.mdk-spoiler]:text-transparent",
  "[&_.mdk-spoiler.mdk-spoiler-revealed]:bg-transparent [&_.mdk-spoiler.mdk-spoiler-revealed]:text-inherit [&_.mdk-spoiler.mdk-spoiler-revealed]:select-text",
  // 主题色（官方 (font) 语法，主题名见 kmarkdown.ts）：未列出的主题按默认色展示。
  "[&_.mdk-font-primary]:text-primary",
  "[&_.mdk-font-success]:text-emerald-600",
  "[&_.mdk-font-danger]:text-red-600",
  "[&_.mdk-font-warning]:text-amber-600",
  "[&_.mdk-font-info]:text-sky-600",
  "[&_.mdk-font-secondary]:text-muted-foreground",
  "[&_.mdk-font-body]:text-foreground",
  "[&_.mdk-font-tips]:text-muted-foreground",
  "[&_.mdk-font-pink]:text-pink-600",
  "[&_.mdk-font-purple]:text-purple-600",
].join(" ")

// 事件委托：剧透内容在渲染后的 HTML 字符串里，无法直接绑 React 事件；
// 在容器上代理点击，切换被点击的剧透元素的揭示状态（对齐 KOOK 客户端的交互）。
function handleSpoilerClick(event: MouseEvent<HTMLDivElement>) {
  const target = event.target as HTMLElement
  const spoiler = target.closest(".mdk-spoiler")
  if (spoiler instanceof HTMLElement) {
    spoiler.classList.toggle("mdk-spoiler-revealed")
  }
}

// memo + useMemo：消息列表里每条文本消息都会渲染一次预览，
// 父组件任意状态变化（备注输入等）不应触发全部消息重新解析 KMarkdown。
export const KMarkdownPreview = memo(function KMarkdownPreview({
  source,
  emptyText,
  className,
}: {
  source: string
  emptyText: string
  className?: string
}) {
  const html = useMemo(() => renderKMarkdown(source), [source])

  if (!html) {
    // 空文案不渲染占位段落：卡片 section 里会留下一个空行。
    if (!emptyText) return null
    return <p className={cn("text-muted-foreground text-sm", className)}>{emptyText}</p>
  }

  return (
    <div
      className={cn(previewStyles, className)}
      onClick={handleSpoilerClick}
      // eslint-disable-next-line react/no-danger -- 内容已做 HTML 转义
      dangerouslySetInnerHTML={{ __html: html }}
    />
  )
})
