/**
 * 展示时区。
 *
 * 为什么需要它：后端的业务时区是 Asia/Shanghai（TICKET_TZ），
 * 但浏览器可能在任何时区。若直接使用浏览器本地时区渲染，
 * 客服看到的“几点开的单”会与 KOOK 群里的时间、与统计数据（按北京时间分日/分时）不一致。
 *
 * 因此：所有时间统一按业务时区渲染，时区由 /auth/me 与 /meta/runtime 下发，默认 Asia/Shanghai。
 */

import { formatInTimeZone } from "date-fns-tz"

/** 默认业务时区：中国北京时间（UTC+8）。 */
export const DEFAULT_TIMEZONE = "Asia/Shanghai"

let currentTimezone = DEFAULT_TIMEZONE

/** 设置展示时区（由接口下发，非法值会被忽略）。 */
export function setDisplayTimezone(timezone?: string | null) {
  if (!timezone) return
  const trimmed = timezone.trim()
  if (!trimmed) return
  try {
    // 校验是否为合法 IANA 时区
    new Intl.DateTimeFormat("en-US", { timeZone: trimmed })
    currentTimezone = trimmed
  } catch {
    // 忽略非法时区，继续使用默认值
  }
}

/** 返回当前展示时区。 */
export function getDisplayTimezone(): string {
  return currentTimezone
}

/** 返回形如 +08:00 的偏移量。 */
export function timezoneOffset(timezone = currentTimezone): string {
  try {
    return formatInTimeZone(new Date(), timezone, "xxx")
  } catch {
    return "+08:00"
  }
}

/** 返回人类可读的时区标签，例如「北京时间 (UTC+8)」。 */
export function timezoneLabel(language = "zh-CN", timezone = currentTimezone): string {
  const offset = timezoneOffset(timezone)
  const utcOffset = offset === "+00:00" ? "UTC+0" : `UTC${offset.startsWith("+") ? offset.slice(0, 3) : offset.slice(0, 3)}`
  if (timezone === DEFAULT_TIMEZONE) {
    return language.startsWith("zh") ? `北京时间 (${utcOffset})` : `Beijing time (${utcOffset})`
  }
  return `${timezone} (${utcOffset})`
}

/** 按展示时区格式化时间，pattern 使用 date-fns 语法。 */
export function formatInDisplayTimezone(
  value: Date | string,
  pattern: string,
  options?: Parameters<typeof formatInTimeZone>[3],
): string {
  const date = typeof value === "string" ? new Date(value) : value
  return formatInTimeZone(date, currentTimezone, pattern, options)
}
