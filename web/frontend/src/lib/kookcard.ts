/**
 * KOOK 卡片消息的 JSON 结构与解析工具。
 *
 * 与后端 internal/kook/card.go 的字段保持一致：卡片数组 → card → module → element。
 * 只做解析，不做渲染（渲染见 components/KookCardView.tsx）。
 */

export type CardText = string | { type?: string; content?: string; fields?: CardTextField[] }

export interface CardTextField {
  type?: string
  content?: string
}

export interface KookElement {
  type?: string
  text?: CardText
  content?: string
  value?: string
  click?: string
  theme?: string
  src?: string
  alt?: string
  size?: string
  circle?: boolean
  fallbackUrl?: string
}

export interface KookModule {
  type?: string
  text?: CardText
  elements?: KookElement[]
  accessory?: KookElement
  mode?: string
  src?: string
  title?: string
  cover?: string
  endTime?: number
  startTime?: number
}

export interface KookCard {
  type?: string
  theme?: string
  color?: string
  size?: string
  modules?: KookModule[]
}

/** parseCards 解析卡片 JSON，兼容单个对象与数组；无法解析时返回空数组。 */
export function parseCards(raw: string | undefined): KookCard[] {
  const content = (raw ?? "").trim()
  if (!content) return []
  try {
    const parsed = JSON.parse(content) as unknown
    const list = Array.isArray(parsed) ? parsed : [parsed]
    return list.filter((item): item is KookCard => {
      if (!item || typeof item !== "object") return false
      return Array.isArray((item as KookCard).modules)
    })
  } catch {
    return []
  }
}

/** textOf 归一化文本元素：兼容字符串、plain-text / kmarkdown / paragraph。 */
export function textOf(value: CardText | undefined): { content: string; markdown: boolean } {
  if (value == null) return { content: "", markdown: false }
  if (typeof value === "string") return { content: value, markdown: false }
  if (value.type === "paragraph" && Array.isArray(value.fields)) {
    const parts = value.fields
      .map((field) => (typeof field === "string" ? field : field?.content ?? ""))
      .map((part) => part.trim())
      .filter(Boolean)
    return {
      content: parts.join("\n"),
      markdown: value.fields.some((field) => (typeof field === "object" ? field?.type === "kmarkdown" : false)),
    }
  }
  return { content: value.content ?? "", markdown: value.type === "kmarkdown" }
}

/** CardFieldView 是 paragraph 里单个字段的渲染视图。 */
export interface CardFieldView {
  content: string
  markdown: boolean
}

/**
 * fieldsOf 返回文本元素的逐字段视图（兼容字符串、plain-text / kmarkdown / paragraph）。
 *
 * KOOK 的 paragraph 模块允许混排 plain-text 与 kmarkdown 字段，且每个字段按
 * 自身类型渲染；旧实现把整块拼起来后只要任一字段是 kmarkdown 就整块按
 * kmarkdown 解析，plain 字段里的 `*`、`[` 会被误格式化。这里逐字段拆开，
 * 供渲染层按各自类型展示（见 KookCardView 的 RichText）。
 */
export function fieldsOf(value: CardText | undefined): CardFieldView[] {
  if (value == null) return []
  if (typeof value === "string") {
    return value.trim() ? [{ content: value, markdown: false }] : []
  }
  if (value.type === "paragraph" && Array.isArray(value.fields)) {
    const views: CardFieldView[] = []
    for (const field of value.fields) {
      const content = typeof field === "string" ? field : field?.content ?? ""
      const markdown = typeof field === "object" && field?.type === "kmarkdown"
      if (content.trim()) views.push({ content, markdown })
    }
    return views
  }
  const content = value.content ?? ""
  return content.trim() ? [{ content, markdown: value.type === "kmarkdown" }] : []
}

/** elementText 提取 context 等元素里的文本（兼容 text 嵌套与直接 content）。 */
export function elementText(element: KookElement): { content: string; markdown: boolean } {
  if (element.text != null) {
    const text = textOf(element.text)
    if (text.content.trim()) return text
  }
  return { content: element.content ?? "", markdown: element.type === "kmarkdown" }
}

/**
 * safeCountdownTime 把卡片倒计时的 endTime 安全解析为 ISO 字符串。
 *
 * endTime 来自用户消息的卡片 JSON（message/view 原样入库），可能是任意
 * 类型/量纲：非法值直接返回 null（渲染层显示兜底文案），绝不让
 * new Date(非法值).toISOString() 抛 RangeError——那会炸掉整个工单详情页
 * （只有顶层 ErrorBoundary 兜底）。数值小于 1e11 视为秒级时间戳（10 位）
 * 换算成毫秒；年份落在 2001–2100 之外视为非法（同时兜住秒/毫秒误判）。
 */
export function safeCountdownTime(endTime: unknown): string | null {
  const value = typeof endTime === "number" ? endTime : Number(endTime)
  if (!Number.isFinite(value) || value <= 0) return null
  const ms = value < 1e11 ? value * 1000 : value
  const date = new Date(ms)
  if (Number.isNaN(date.getTime())) return null
  const year = date.getFullYear()
  if (year < 2001 || year > 2100) return null
  return date.toISOString()
}
