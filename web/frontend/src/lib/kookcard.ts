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

/** elementText 提取 context 等元素里的文本（兼容 text 嵌套与直接 content）。 */
export function elementText(element: KookElement): { content: string; markdown: boolean } {
  if (element.text != null) {
    const text = textOf(element.text)
    if (text.content.trim()) return text
  }
  return { content: element.content ?? "", markdown: element.type === "kmarkdown" }
}
