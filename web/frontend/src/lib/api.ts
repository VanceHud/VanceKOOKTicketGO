/**
 * API 客户端。
 *
 * 安全要点：
 *  - 所有请求带 credentials: same-origin，会话依赖 HttpOnly Cookie；
 *  - 非幂等请求自动附带 X-CSRF-Token（由 /auth/me 下发、服务端派生）；
 *  - 错误统一为 ApiError，页面根据 code 做二次处理（例如 401 跳登录、403 提示无权限）。
 */

import type { ApiErrorPayload } from "@/lib/types"

const API_BASE = "/api/v1"

/** 服务端返回的结构化错误。 */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly requestId?: string

  constructor(status: number, code: string, message: string, requestId?: string) {
    super(message)
    this.name = "ApiError"
    this.status = status
    this.code = code
    this.requestId = requestId
  }

  /** 未认证：需要跳转登录页。 */
  get isUnauthenticated(): boolean {
    return this.status === 401
  }

  /** 权限不足。 */
  get isForbidden(): boolean {
    return this.status === 403
  }

  /** 状态冲突（例如工单已关闭）。 */
  get isConflict(): boolean {
    return this.status === 409
  }
}

let csrfToken: string | null = null

/** 保存最新下发的 CSRF 令牌。 */
export function setCsrfToken(token: string | null) {
  csrfToken = token
}

export function getCsrfToken(): string | null {
  return csrfToken
}

type Method = "GET" | "POST" | "PATCH" | "PUT" | "DELETE"

async function request<T>(method: Method, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" }
  if (body !== undefined) {
    headers["Content-Type"] = "application/json"
  }
  if (csrfToken && method !== "GET") {
    headers["X-CSRF-Token"] = csrfToken
  }

  const response = await fetch(`${API_BASE}${path}`, {
    method,
    headers,
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  })

  const text = await response.text()
  let payload: unknown = undefined
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = undefined
    }
  }

  if (!response.ok) {
    const detail = (payload as ApiErrorPayload | undefined)?.error
    throw new ApiError(
      response.status,
      detail?.code ?? "unknown_error",
      detail?.message ?? `请求失败（HTTP ${response.status}）`,
      detail?.requestId,
    )
  }
  return payload as T
}

/** 构造查询串，自动忽略空值。 */
export function buildQuery(params: Record<string, string | number | undefined | null>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === "") continue
    search.set(key, String(value))
  }
  const query = search.toString()
  return query ? `?${query}` : ""
}

export const api = {
  get: <T,>(path: string) => request<T>("GET", path),
  post: <T,>(path: string, body?: unknown) => request<T>("POST", path, body),
  patch: <T,>(path: string, body?: unknown) => request<T>("PATCH", path, body),
  put: <T,>(path: string, body?: unknown) => request<T>("PUT", path, body),
  del: <T,>(path: string) => request<T>("DELETE", path),
}

/** 触发浏览器下载导出文件（GET 请求，浏览器会带上会话 Cookie）。 */
export function downloadExport(ticketNo: string, format: "json" | "csv" | "html") {
  const url = `${API_BASE}/tickets/${encodeURIComponent(ticketNo)}/export?format=${format}`
  // 使用隐藏 iframe 触发下载，避免被拦截弹窗。
  const frame = document.createElement("iframe")
  frame.style.display = "none"
  frame.src = url
  document.body.appendChild(frame)
  window.setTimeout(() => frame.remove(), 60_000)
}
