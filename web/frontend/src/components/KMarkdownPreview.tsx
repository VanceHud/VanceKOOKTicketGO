/**
 * 面板文案的 KMarkdown 预览。
 *
 * renderKMarkdown 已经对输入做了 HTML 转义，因此这里可以安全地使用
 * dangerouslySetInnerHTML 展示结果。
 */

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
].join(" ")

export function KMarkdownPreview({
  source,
  emptyText,
  className,
}: {
  source: string
  emptyText: string
  className?: string
}) {
  const html = renderKMarkdown(source)

  if (!html) {
    return <p className={cn("text-muted-foreground text-sm", className)}>{emptyText}</p>
  }

  return (
    <div
      className={cn(previewStyles, className)}
      // eslint-disable-next-line react/no-danger -- 内容已做 HTML 转义
      dangerouslySetInnerHTML={{ __html: html }}
    />
  )
}
