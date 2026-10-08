/**
 * 统一的查询封装：集中定义查询键与请求路径，避免键名散落导致缓存失效逻辑出错。
 */

import { useQuery, type UseQueryResult } from "@tanstack/react-query"

import { api, buildQuery } from "@/lib/api"
import { setDisplayTimezone } from "@/lib/timezone"
import type {
  AdminRole,
  AuditLog,
  AuditParams,
  BotActivityResponse,
  EmojiGrant,
  EmojiRule,
  GameListResponse,
  KookOptionsResponse,
  ListResponse,
  RoleMapping,
  RuntimeInfo,
  SettingsResponse,
  StatsAnalytics,
  StatsOverview,
  Ticket,
  TicketListParams,
  TicketMessage,
  TicketNote,
  TicketType,
  WebUserAccount,
} from "@/lib/types"

export const queryKeys = {
  me: ["me"] as const,
  tickets: (params: TicketListParams) => ["tickets", params] as const,
  ticket: (no: string) => ["ticket", no] as const,
  messages: (no: string) => ["messages", no] as const,
  notes: (no: string) => ["notes", no] as const,
  stats: (days: number) => ["stats", days] as const,
  analytics: (days: number) => ["analytics", days] as const,
  settings: ["settings"] as const,
  runtime: ["runtime"] as const,
  users: ["users"] as const,
  audit: (params: AuditParams) => ["audit", params] as const,
  types: ["ticket-types"] as const,
  emojiRules: ["emoji-rules"] as const,
  emojiGrants: ["emoji-grants"] as const,
  adminRoles: ["admin-roles"] as const,
  roleMappings: ["role-mappings"] as const,
  kookRoles: ["kook-roles"] as const,
  kookChannels: ["kook-channels"] as const,
  games: (type: number) => ["games", type] as const,
  botActivity: ["bot-activity"] as const,
}

export function useTickets(params: TicketListParams): UseQueryResult<ListResponse<Ticket>> {
  return useQuery({
    queryKey: queryKeys.tickets(params),
    queryFn: () => api.get<ListResponse<Ticket>>(`/tickets${buildQuery({ ...params } as Record<string, string | number>)}`),
    placeholderData: (previous) => previous,
  })
}

export function useTicket(no: string | undefined): UseQueryResult<Ticket> {
  return useQuery({
    queryKey: queryKeys.ticket(no ?? ""),
    queryFn: () => api.get<Ticket>(`/tickets/${encodeURIComponent(no as string)}`),
    enabled: Boolean(no),
  })
}

/**
 * 工单详情默认加载最近的消息条数。
 *
 * 后端 tail=1 返回最新的一段；被截断时页面提供「加载更早的消息」按钮
 * （增大 limit 重新查询，上限为后端的 2000 条）。
 */
export const DEFAULT_MESSAGE_LIMIT = 300
const MAX_MESSAGE_LIMIT = 2000

/** 下一档消息加载量（用于「加载更早的消息」）。 */
export function nextMessageLimit(current: number): number {
  return Math.min(current + DEFAULT_MESSAGE_LIMIT, MAX_MESSAGE_LIMIT)
}

export function useTicketMessages(no: string | undefined, limit = DEFAULT_MESSAGE_LIMIT): UseQueryResult<ListResponse<TicketMessage>> {
  return useQuery({
    queryKey: [...queryKeys.messages(no ?? ""), limit],
    queryFn: () =>
      api.get<ListResponse<TicketMessage>>(
        `/tickets/${encodeURIComponent(no as string)}/messages${buildQuery({ tail: 1, limit })}`,
      ),
    enabled: Boolean(no),
  })
}

export function useTicketNotes(no: string | undefined): UseQueryResult<ListResponse<TicketNote>> {
  return useQuery({
    queryKey: queryKeys.notes(no ?? ""),
    queryFn: () => api.get<ListResponse<TicketNote>>(`/tickets/${encodeURIComponent(no as string)}/notes`),
    enabled: Boolean(no),
  })
}

export function useStats(days: number): UseQueryResult<StatsOverview> {
  return useQuery({
    queryKey: queryKeys.stats(days),
    queryFn: () => api.get<StatsOverview>(`/stats/overview?days=${days}`),
  })
}

export function useAnalytics(days: number): UseQueryResult<StatsAnalytics> {
  return useQuery({
    queryKey: queryKeys.analytics(days),
    queryFn: () => api.get<StatsAnalytics>(`/stats/analytics?days=${days}`),
  })
}

export function useSettings(): UseQueryResult<SettingsResponse> {
  return useQuery({ queryKey: queryKeys.settings, queryFn: () => api.get<SettingsResponse>("/settings") })
}

export function useRuntimeInfo(): UseQueryResult<RuntimeInfo> {
  return useQuery({
    queryKey: queryKeys.runtime,
    queryFn: async () => {
      const info = await api.get<RuntimeInfo>("/meta/runtime")
      setDisplayTimezone(info.ticketTimezone)
      return info
    },
  })
}

export function useUsers(): UseQueryResult<ListResponse<WebUserAccount>> {
  return useQuery({ queryKey: queryKeys.users, queryFn: () => api.get<ListResponse<WebUserAccount>>("/users") })
}

export function useAuditLogs(params: AuditParams): UseQueryResult<ListResponse<AuditLog>> {
  return useQuery({
    queryKey: queryKeys.audit(params),
    queryFn: () => api.get<ListResponse<AuditLog>>(`/audit${buildQuery({ ...params } as Record<string, string | number>)}`),
    placeholderData: (previous) => previous,
  })
}

export function useTicketTypes(): UseQueryResult<ListResponse<TicketType>> {
  return useQuery({ queryKey: queryKeys.types, queryFn: () => api.get<ListResponse<TicketType>>("/types") })
}

export function useEmojiRules(): UseQueryResult<ListResponse<EmojiRule>> {
  return useQuery({ queryKey: queryKeys.emojiRules, queryFn: () => api.get<ListResponse<EmojiRule>>("/emoji/rules") })
}

export function useEmojiGrants(): UseQueryResult<ListResponse<EmojiGrant>> {
  return useQuery({ queryKey: queryKeys.emojiGrants, queryFn: () => api.get<ListResponse<EmojiGrant>>("/emoji/grants?limit=50") })
}

export function useAdminRoles(): UseQueryResult<ListResponse<AdminRole>> {
  return useQuery({ queryKey: queryKeys.adminRoles, queryFn: () => api.get<ListResponse<AdminRole>>("/roles/admin") })
}

export function useRoleMappings(): UseQueryResult<ListResponse<RoleMapping>> {
  return useQuery({ queryKey: queryKeys.roleMappings, queryFn: () => api.get<ListResponse<RoleMapping>>("/roles/mappings") })
}

export function useKookRoles(): UseQueryResult<KookOptionsResponse> {
  return useQuery({ queryKey: queryKeys.kookRoles, queryFn: () => api.get<KookOptionsResponse>("/meta/guild/roles") })
}

export function useKookChannels(): UseQueryResult<KookOptionsResponse> {
  return useQuery({ queryKey: queryKeys.kookChannels, queryFn: () => api.get<KookOptionsResponse>("/meta/guild/channels") })
}

/** 游戏库；type 为平台过滤值（0 全部 / 1 用户创建 / 2 系统创建）。 */
export function useGames(type: number): UseQueryResult<GameListResponse> {
  return useQuery({ queryKey: queryKeys.games(type), queryFn: () => api.get<GameListResponse>(`/games?type=${type}`) })
}

/** 机器人当前在玩/在听动态与自动恢复开关。 */
export function useBotActivity(): UseQueryResult<BotActivityResponse> {
  return useQuery({ queryKey: queryKeys.botActivity, queryFn: () => api.get<BotActivityResponse>("/bot/activity") })
}
