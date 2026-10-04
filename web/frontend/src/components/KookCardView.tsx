/**
 * KOOK 卡片消息渲染器（只读）。
 *
 * 工单聊天记录里的卡片消息由后端调用 message/view 补全原始 JSON，
 * 这里按 KOOK 卡片结构（card / module / element）渲染成接近客户端的样式：
 * header、section、context、divider、image-group、container、action-group，
 * 以及文件、音频、视频、倒计时等模块。
 *
 * 安全约定：
 * - 文本一律交给 KMarkdownPreview（内部会先做 HTML 转义）；
 * - 图片、音视频、文件只做展示与下载，按钮不绑定任何回调（点击不会触发机器人动作）。
 */

import { useState } from "react"
import { Download, FileAudio, FileText, FileVideo, ImageOff, Timer } from "lucide-react"
import { useTranslation } from "react-i18next"

import { KMarkdownPreview } from "@/components/KMarkdownPreview"
import { formatDateTime } from "@/lib/format"
import { elementText, parseCards, textOf, type CardText, type KookElement, type KookModule } from "@/lib/kookcard"
import { cn } from "@/lib/utils"

/** 卡片主题对应的边框与底色（对齐 KOOK 客户端的主题色）。 */
const CARD_THEMES: Record<string, string> = {
  primary: "border-primary/40 bg-primary/5",
  success: "border-emerald-500/40 bg-emerald-500/5",
  danger: "border-red-500/40 bg-red-500/5",
  warning: "border-amber-500/40 bg-amber-500/5",
  info: "border-sky-500/40 bg-sky-500/5",
  secondary: "border-border bg-muted/30",
  none: "border-transparent bg-transparent",
  invisible: "border-transparent bg-transparent",
}

/** 按钮主题（只读展示，不触发任何动作）。 */
const BUTTON_THEMES: Record<string, string> = {
  primary: "bg-primary text-primary-foreground",
  success: "bg-emerald-600 text-white",
  danger: "bg-red-600 text-white",
  warning: "bg-amber-500 text-white",
  info: "bg-sky-600 text-white",
  secondary: "bg-secondary text-secondary-foreground",
}

function RichText({ value, className }: { value: CardText | undefined; className?: string }) {
  const text = textOf(value)
  if (!text.content.trim()) return null
  if (text.markdown) {
    return <KMarkdownPreview source={text.content} emptyText="" className={className} />
  }
  return <p className={cn("text-sm leading-relaxed break-words whitespace-pre-wrap", className)}>{text.content}</p>
}

/** CardImage 带加载失败兜底的图片元素。 */
function CardImage({ src, alt, className, circle }: { src?: string; alt?: string; className?: string; circle?: boolean }) {
  const { t } = useTranslation()
  const [failed, setFailed] = useState(false)
  const url = (src ?? "").trim()
  if (!url) return null

  if (failed) {
    return (
      <a
        href={url}
        target="_blank"
        rel="noreferrer noopener"
        className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs underline underline-offset-2"
      >
        <ImageOff className="size-3.5" />
        {alt || t("ticket.mediaOpen")}
      </a>
    )
  }

  return (
    <img
      src={url}
      alt={alt ?? ""}
      loading="lazy"
      referrerPolicy="no-referrer"
      onError={() => setFailed(true)}
      className={cn("max-h-80 max-w-full rounded-md object-contain", circle && "rounded-full", className)}
    />
  )
}

/** CardButton 只读按钮：link 类型渲染为链接，其余仅作展示。 */
function CardButton({ element }: { element: KookElement }) {
  const text = elementText(element)
  const classes = cn(
    "inline-flex h-8 items-center rounded-md px-3 text-xs font-medium",
    BUTTON_THEMES[element.theme ?? "primary"] ?? BUTTON_THEMES.primary,
  )
  if (element.click === "link" && element.value) {
    return (
      <a href={element.value} target="_blank" rel="noreferrer noopener" className={classes}>
        {text.content}
      </a>
    )
  }
  return <span className={cn(classes, "cursor-default")}>{text.content}</span>
}

/** CardAccessory 渲染 section 的 accessory（图片或按钮）。 */
function CardAccessory({ element }: { element: KookElement }) {
  if (element.type === "image") {
    return (
      <CardImage
        src={element.src ?? element.fallbackUrl}
        alt={element.alt}
        className={element.size === "sm" ? "max-h-16" : "max-h-40"}
        circle={element.circle}
      />
    )
  }
  if (element.type === "button") {
    return <CardButton element={element} />
  }
  return null
}

function CardModuleView({ module }: { module: KookModule }) {
  const { t, i18n } = useTranslation()

  switch (module.type) {
    case "header":
      return <RichText value={module.text} className="text-base font-semibold" />

    case "section": {
      const accessory = module.accessory ? <CardAccessory element={module.accessory} /> : null
      return (
        <div className="flex items-start gap-3">
          {module.mode === "left" ? accessory : null}
          <div className="min-w-0 flex-1">
            <RichText value={module.text} />
          </div>
          {module.mode === "left" ? null : accessory}
        </div>
      )
    }

    case "context":
      return (
        <div className="text-muted-foreground flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
          {(module.elements ?? []).map((element, index) => {
            if (element.type === "image") {
              return (
                <CardImage
                  key={index}
                  src={element.src ?? element.fallbackUrl}
                  alt={element.alt}
                  className="size-4"
                  circle={element.circle}
                />
              )
            }
            const text = elementText(element)
            if (!text.content.trim()) return null
            if (text.markdown) {
              return <KMarkdownPreview key={index} source={text.content} emptyText="" className="text-xs" />
            }
            return (
              <span key={index} className="break-words">
                {text.content}
              </span>
            )
          })}
        </div>
      )

    case "divider":
      return <div className="bg-border/70 h-px w-full" />

    case "image-group":
      return (
        <div className="grid grid-cols-3 gap-1">
          {(module.elements ?? [])
            .filter((element) => element.type === "image")
            .map((element, index) => (
              <a
                key={index}
                href={element.src ?? element.fallbackUrl}
                target="_blank"
                rel="noreferrer noopener"
                className="block"
              >
                <CardImage
                  src={element.src ?? element.fallbackUrl}
                  alt={element.alt}
                  className="aspect-square w-full rounded-md object-cover"
                />
              </a>
            ))}
        </div>
      )

    case "container":
      return (
        <div className="space-y-1">
          {(module.elements ?? [])
            .filter((element) => element.type === "image")
            .map((element, index) => (
              <CardImage key={index} src={element.src ?? element.fallbackUrl} alt={element.alt} className="w-full rounded-md" />
            ))}
        </div>
      )

    case "action-group":
      return (
        <div className="flex flex-wrap gap-2">
          {(module.elements ?? [])
            .filter((element) => element.type === "button")
            .map((element, index) => (
              <CardButton key={index} element={element} />
            ))}
        </div>
      )

    case "file":
      return (
        <a
          href={module.src}
          target="_blank"
          rel="noreferrer noopener"
          download
          className="bg-background/60 hover:bg-muted flex items-center gap-2 rounded-md border px-3 py-2 text-sm transition-colors"
        >
          <FileText className="text-muted-foreground size-4 shrink-0" />
          <span className="min-w-0 flex-1 truncate">{module.title || t("ticket.attachment")}</span>
          <Download className="text-muted-foreground size-4 shrink-0" />
        </a>
      )

    case "audio":
      return (
        <div className="space-y-1">
          {module.title ? (
            <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
              <FileAudio className="size-3.5" />
              {module.title}
            </p>
          ) : null}
          <audio controls preload="metadata" src={module.src} className="w-full" />
        </div>
      )

    case "video":
      return (
        <div className="space-y-1">
          {module.title ? (
            <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
              <FileVideo className="size-3.5" />
              {module.title}
            </p>
          ) : null}
          <video controls preload="metadata" src={module.src} className="max-h-80 w-full rounded-md" />
        </div>
      )

    case "countdown":
      return (
        <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
          <Timer className="size-3.5" />
          {module.endTime
            ? t("ticket.countdownTo", { time: formatDateTime(new Date(module.endTime).toISOString(), i18n.language) })
            : t("ticket.countdown")}
        </p>
      )

    default:
      return null
  }
}

/**
 * KookCardView 渲染一条卡片消息。
 *
 * cards 为空（旧记录或补全失败）时退回纯文本展示，保证历史记录仍可读。
 */
export function KookCardView({ json, fallback }: { json?: string; fallback: string }) {
  const cards = parseCards(json)

  if (cards.length === 0) {
    return <p className="text-sm break-words whitespace-pre-wrap">{fallback}</p>
  }

  return (
    <div className="space-y-2">
      {cards.map((card, index) => (
        <div
          key={index}
          className={cn("space-y-2.5 rounded-lg border px-3 py-2.5", CARD_THEMES[card.theme ?? "primary"] ?? CARD_THEMES.primary)}
          style={card.color ? { borderColor: card.color } : undefined}
        >
          {(card.modules ?? []).map((module, moduleIndex) => (
            <CardModuleView key={moduleIndex} module={module} />
          ))}
        </div>
      ))}
    </div>
  )
}
