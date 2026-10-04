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
  EmojiGrant,
  EmojiRule,
  KookOptionsResponse,
  ListResponse,
  Panel,
  RoleMapping,
  RuntimeInfo,
  SettingsResponse,
  StatsAnalytics,
  StatsOverview,
  Ticket,
  TicketListParams,
  TicketMessage,
  TicketNote,
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
  panels: ["panels"] as const,
  emojiRules: ["emoji-rules"] as const,
  emojiGrants: ["emoji-grants"] as const,
  adminRoles: ["admin-roles"] as const,
  roleMappings: ["role-mappings"] as const,
  kookRoles: ["kook-roles"] as const,
  kookChannels: ["kook-channels"] as const,
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

export function useTicketMessages(no: string | undefined): UseQueryResult<ListResponse<TicketMessage>> {
  return useQuery({
    queryKey: queryKeys.messages(no ?? ""),
    queryFn: () => api.get<ListResponse<TicketMessage>>(`/tickets/${encodeURIComponent(no as string)}/messages?limit=1000`),
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

export function usePanels(): UseQueryResult<ListResponse<Panel>> {
  return useQuery({ queryKey: queryKeys.panels, queryFn: () => api.get<ListResponse<Panel>>("/panels") })
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
