/**
 * 认证上下文：集中管理 /auth/me、登录、登出与 CSRF 令牌。
 *
 * 设计说明：
 *  - 会话是 HttpOnly Cookie，前端无法也不应该读取，因此“是否登录”完全以 /auth/me 为准；
 *  - 登录成功后立即写入 CSRF 令牌，后续写操作由 api 层自动附加；
 *  - 登出会清空查询缓存，避免残留上一账号的数据。
 */

import { createContext, useCallback, useContext, useMemo, type ReactNode } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import { ApiError, api, setCsrfToken } from "@/lib/api"
import { setDisplayTimezone } from "@/lib/timezone"
import type { MeResponse } from "@/lib/types"

interface AuthContextValue {
  me: MeResponse | null
  isLoading: boolean
  isAuthenticated: boolean
  mustChangePassword: boolean
  login: (username: string, password: string) => Promise<MeResponse>
  loginPending: boolean
  loginError: ApiError | null
  resetLoginError: () => void
  logout: () => Promise<void>
  refresh: () => Promise<unknown>
}

const AuthContext = createContext<AuthContextValue | null>(null)

export const ME_QUERY_KEY = ["me"] as const

/**
 * 是否可能存在已登录会话。
 *
 * 服务端在登录时写入一个不含任何凭据的提示 Cookie（kt_logged_in=1），
 * 前端据此决定要不要调用 /auth/me：未登录时直接跳过，
 * 避免在浏览器控制台留下“401 Unauthorized”这类容易误判的噪音。
 * 注意：这只是体验优化，真正的认证判断始终来自服务端响应。
 */
function hasSessionMarker(): boolean {
  return document.cookie.split("; ").some((entry) => entry.startsWith("kt_logged_in="))
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()

  const meQuery = useQuery<MeResponse | null, ApiError>({
    queryKey: ME_QUERY_KEY,
    queryFn: async () => {
      if (!hasSessionMarker()) {
        setCsrfToken(null)
        return null
      }
      try {
        const me = await api.get<MeResponse>("/auth/me")
        setCsrfToken(me.csrfToken)
        // 时间统一按后端业务时区渲染（默认 Asia/Shanghai）
        setDisplayTimezone(me.server.ticketTimezone)
        return me
      } catch (error) {
        if (error instanceof ApiError && error.isUnauthenticated) {
          setCsrfToken(null)
          return null
        }
        throw error
      }
    },
    retry: false,
    staleTime: 60_000,
    refetchOnWindowFocus: false,
  })

  const loginMutation = useMutation<MeResponse, ApiError, { username: string; password: string }>({
    mutationFn: (variables) => api.post<MeResponse>("/auth/login", variables),
    onSuccess: (me) => {
      setCsrfToken(me.csrfToken)
      setDisplayTimezone(me.server.ticketTimezone)
      queryClient.setQueryData(ME_QUERY_KEY, me)
    },
  })

  const logoutMutation = useMutation<unknown, ApiError, void>({
    mutationFn: () => api.post<unknown>("/auth/logout"),
    onSettled: () => {
      setCsrfToken(null)
      // 先清空业务缓存，再把 me 显式置空，避免闪现上一账号数据。
      queryClient.clear()
      queryClient.setQueryData(ME_QUERY_KEY, null)
    },
  })

  const refresh = useCallback(() => queryClient.invalidateQueries({ queryKey: ME_QUERY_KEY }), [queryClient])

  const value = useMemo<AuthContextValue>(() => {
    const me = meQuery.data ?? null
    return {
      me,
      isLoading: meQuery.isLoading,
      isAuthenticated: Boolean(me),
      mustChangePassword: Boolean(me?.user.mustChangePassword),
      login: (username, password) => loginMutation.mutateAsync({ username, password }),
      loginPending: loginMutation.isPending,
      loginError: loginMutation.error ?? null,
      resetLoginError: () => loginMutation.reset(),
      logout: async () => {
        await logoutMutation.mutateAsync()
      },
      refresh,
    }
  }, [meQuery.data, meQuery.isLoading, loginMutation, logoutMutation, refresh])

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthContextValue {
  const context = useContext(AuthContext)
  if (!context) {
    throw new Error("useAuth 必须在 AuthProvider 内部使用")
  }
  return context
}
