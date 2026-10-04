/**
 * 富媒体消息地址解析。
 *
 * 归档消息优先使用结构化字段 mediaUrl；旧记录只存了「[图片] 地址」文本，
 * 这里做兼容解析，保证历史聊天记录也能正常展示。
 */

import type { TicketMessage } from "@/lib/types"

/** LEGACY_MEDIA_PREFIX 匹配旧记录里的「[类型] 」前缀。 */
const LEGACY_MEDIA_PREFIX = /^\[(图片|视频|文件|语音|卡片消息)\]\s*/u

/** resolveMediaUrl 解析媒体地址：优先结构化字段，其次兼容旧记录文本。 */
export function resolveMediaUrl(message: Pick<TicketMessage, "content" | "mediaUrl">): string {
  const direct = (message.mediaUrl ?? "").trim()
  if (direct) return direct
  const content = (message.content ?? "").trim().replace(LEGACY_MEDIA_PREFIX, "")
  const first = content.split(/\s+/)[0] ?? ""
  return /^https?:\/\//i.test(first) ? first : ""
}

/** mediaFileName 从地址中推断文件名（无文件名时的兜底展示）。 */
export function mediaFileName(url: string): string {
  const path = url.split("?")[0] ?? url
  const name = path.split("/").pop() ?? ""
  return name || url
}
