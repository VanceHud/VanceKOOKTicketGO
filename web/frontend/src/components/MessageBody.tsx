/**
 * 工单聊天记录的消息正文渲染。
 *
 * 归档消息里的富媒体会带上结构化字段（mediaUrl/mediaName/mediaType）：
 * - 图片：内联展示（点击可在新标签打开原图），加载失败退化为链接；
 * - 视频/语音：内联播放器；
 * - 文件：下载链接；
 * - 卡片：交给 KookCardView 按 KOOK 卡片结构渲染。
 *
 * 兼容旧记录：媒体记录早期只存了「[图片] 地址」文本，这里会从中解析出地址。
 */

import { useState } from "react"
import { Download, FileText, ImageOff } from "lucide-react"
import { useTranslation } from "react-i18next"

import { mediaFileName, resolveMediaUrl } from "@/lib/media"
import type { TicketMessage } from "@/lib/types"
import { cn } from "@/lib/utils"

/** MediaLink 打开/下载原文件的兜底链接。 */
function MediaLink({ url, label }: { url: string; label: string }) {
  return (
    <a
      href={url}
      target="_blank"
      rel="noreferrer noopener"
      className="text-primary inline-flex items-center gap-1.5 text-xs underline underline-offset-2"
    >
      <Download className="size-3.5" />
      {label}
    </a>
  )
}

function MessageImage({ message }: { message: TicketMessage }) {
  const { t } = useTranslation()
  const [failed, setFailed] = useState(false)
  const url = resolveMediaUrl(message)

  if (!url) {
    return <span className="text-muted-foreground break-words">{message.content}</span>
  }
  if (failed) {
    return (
      <a
        href={url}
        target="_blank"
        rel="noreferrer noopener"
        className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-xs underline underline-offset-2"
      >
        <ImageOff className="size-3.5" />
        {t("ticket.mediaImageFailed")}
      </a>
    )
  }

  return (
    <a href={url} target="_blank" rel="noreferrer noopener" className="block">
      <img
        src={url}
        alt={message.mediaName || t("msgType.image")}
        loading="lazy"
        referrerPolicy="no-referrer"
        onError={() => setFailed(true)}
        className="max-h-80 w-auto max-w-full rounded-md"
      />
    </a>
  )
}

function MessageVideo({ message }: { message: TicketMessage }) {
  const { t } = useTranslation()
  const url = resolveMediaUrl(message)
  if (!url) {
    return <span className="text-muted-foreground break-words">{message.content}</span>
  }
  return (
    <div className="space-y-1">
      <video controls preload="metadata" src={url} className="max-h-80 w-full max-w-md rounded-md" />
      <MediaLink url={url} label={message.mediaName || t("ticket.mediaOpen")} />
    </div>
  )
}

function MessageAudio({ message }: { message: TicketMessage }) {
  const { t } = useTranslation()
  const url = resolveMediaUrl(message)
  if (!url) {
    return <span className="text-muted-foreground break-words">{message.content}</span>
  }
  return (
    <div className="w-full max-w-sm space-y-1">
      <audio controls preload="metadata" src={url} className="w-full" />
      <MediaLink url={url} label={message.mediaName || t("msgType.audio")} />
    </div>
  )
}

function MessageFile({ message }: { message: TicketMessage }) {
  const url = resolveMediaUrl(message)
  if (!url) {
    return <span className="text-muted-foreground break-words">{message.content}</span>
  }
  const name = message.mediaName || mediaFileName(url)
  return (
    <a
      href={url}
      target="_blank"
      rel="noreferrer noopener"
      download
      className="bg-background/60 hover:bg-muted flex items-center gap-2 rounded-md border px-3 py-2 text-sm transition-colors"
    >
      <FileText className="text-muted-foreground size-4 shrink-0" />
      <span className="min-w-0 flex-1 truncate">{name}</span>
      <Download className="text-muted-foreground size-4 shrink-0" />
    </a>
  )
}

/** MessageBody 按消息类型渲染正文（不含外层气泡样式）。 */
export function MessageBody({ message, className }: { message: TicketMessage; className?: string }) {
  switch (message.type) {
    case "image":
      return <MessageImage message={message} />
    case "video":
      return <MessageVideo message={message} />
    case "audio":
      return <MessageAudio message={message} />
    case "file":
      return <MessageFile message={message} />
    default:
      return <span className={cn("break-words whitespace-pre-wrap", className)}>{message.content}</span>
  }
}
