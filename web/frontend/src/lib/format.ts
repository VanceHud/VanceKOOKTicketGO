/**
 * 展示层格式化工具：时间、时长、文件大小。
 *
 * 时间一律按**业务时区**（默认 Asia/Shanghai）渲染，而不是浏览器本地时区，
 * 保证界面时间与 KOOK 群内时间、与后端统计口径（按北京时间分日/分时）一致。
 * 时长等无时区概念的量按语言本地化。
 */

import { formatDistanceToNowStrict, type Locale } from "date-fns"
import { enUS, zhCN } from "date-fns/locale"

import { formatInDisplayTimezone } from "@/lib/timezone"
import type { TicketStatus } from "@/lib/types"

const localeMap: Record<string, Locale> = {
  "zh-CN": zhCN,
  "en-US": enUS,
}

export function dateLocale(language: string): Locale {
  return localeMap[language] ?? enUS
}

/**
 * 判断时间是否可用。
 *
 * 后端理论上不会下发零值时间（Go 侧已用指针 + omitempty），
 * 但接口由多种来源拼装，这里再兜一层：早于 1970 的时间一律视为无值，
 * 避免界面出现 0001-01-01 这类明显异常的时间。
 */
function isValidDate(value?: string | null): value is string {
  if (!value) return false
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return false
  return date.getFullYear() >= 1970
}

/** 完整日期时间（业务时区），例如 2026-01-05 14:03:11。 */
export function formatDateTime(value?: string | null, language = "zh-CN"): string {
  if (!isValidDate(value)) return "—"
  return formatInDisplayTimezone(value, "yyyy-MM-dd HH:mm:ss", { locale: dateLocale(language) })
}

/** 仅日期（业务时区），例如 2026-01-05。 */
export function formatDate(value?: string | null, language = "zh-CN"): string {
  if (!isValidDate(value)) return "—"
  return formatInDisplayTimezone(value, "yyyy-MM-dd", { locale: dateLocale(language) })
}

/** 相对时间，例如 “3 分钟前”（与本地时区无关，按真实时间差计算）。 */
export function formatRelative(value?: string | null, language = "zh-CN"): string {
  if (!isValidDate(value)) return "—"
  return formatDistanceToNowStrict(new Date(value), { addSuffix: true, locale: dateLocale(language) })
}

/** 秒 → 可读时长（1h 20m / 45s）。 */
export function formatDuration(seconds?: number | null): string {
  if (seconds === undefined || seconds === null || Number.isNaN(seconds) || seconds < 0) return "—"
  const total = Math.round(seconds)
  if (total < 60) return `${total}s`
  const minutes = Math.floor(total / 60)
  if (minutes < 60) return `${minutes}m ${total % 60}s`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ${minutes % 60}m`
  const days = Math.floor(hours / 24)
  return `${days}d ${hours % 24}h`
}

/** 字节 → 人类可读大小。 */
export function formatBytes(bytes?: number | null): string {
  if (!bytes || bytes <= 0) return "—"
  const units = ["B", "KB", "MB", "GB"]
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}

/** 工单状态对应的语义色类名（与 index.css 中的 --status-* 变量一致）。 */
export function statusColorClass(status: TicketStatus): string {
  switch (status) {
    case "open":
      return "text-status-open bg-status-open/12 border-status-open/25"
    case "locked":
      return "text-status-locked bg-status-locked/15 border-status-locked/30"
    case "closed":
      return "text-status-closed bg-status-closed/12 border-status-closed/20"
    case "failed":
      return "text-status-failed bg-status-failed/12 border-status-failed/25"
    case "pending":
    default:
      return "text-status-pending bg-status-pending/12 border-status-pending/25"
  }
}

/** 角色对应的徽标色。 */
export function roleColorClass(role: string): string {
  switch (role) {
    case "admin":
      return "text-primary bg-primary/12 border-primary/25"
    case "staff":
      return "text-status-open bg-status-open/12 border-status-open/25"
    case "readonly":
    default:
      return "text-muted-foreground bg-muted border-border"
  }
}

/** 角色等级，用于前端判断是否显示操作按钮（服务端仍会强制校验）。 */
export function roleAtLeast(role: string | undefined, required: string): boolean {
  const rank: Record<string, number> = { admin: 3, staff: 2, readonly: 1 }
  return (rank[role ?? ""] ?? 0) >= (rank[required] ?? 0)
}
