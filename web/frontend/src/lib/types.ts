/**
 * 与后端接口一一对应的类型定义。
 *
 * 约定：所有时间字段都是 RFC3339 的 UTC 字符串，展示时由前端按本地时区转换。
 */

export type TicketStatus = "pending" | "open" | "locked" | "closed" | "failed"
export type WebRole = "admin" | "staff" | "readonly"
export type CodePurpose = "login" | "bind"

export interface Ticket {
  id: number
  no: string
  userId: string
  userName: string
  sourceChannelId: string
  channelId: string
  panelId?: number | null
  status: TicketStatus
  startedAt: string
  lockedAt?: string | null
  lockReason?: string
  closedAt?: string | null
  closedBy?: string
  closedByName?: string
  firstReplyAt?: string | null
  messageCount: number
  createdAt: string
  updatedAt: string
}

export interface TicketMessage {
  id: number
  ticketNo: string
  msgId: string
  channelId: string
  userId: string
  userName: string
  content: string
  type: string
  isBot: boolean
  createdAt: string
}

export interface TicketNote {
  id: number
  ticketNo: string
  authorId: string
  authorName: string
  source: string
  content: string
  createdAt: string
}

export interface ListResponse<T> {
  items: T[]
  total: number
  page?: number
  pageSize?: number
}

export interface TicketListParams {
  status?: string
  q?: string
  from?: string
  to?: string
  page?: number
  pageSize?: number
}

export interface TrendPoint {
  date: string
  opened: number
  closed: number
}

export interface StatsOverview {
  generatedAt: string
  rangeDays: number
  total: number
  active: number
  open: number
  locked: number
  closed: number
  openedToday: number
  closedToday: number
  uniqueUsers: number
  avgFirstReplySeconds: number
  avgResolutionSeconds: number
  trend: TrendPoint[]
  statusCounts: Record<string, number>
}

export interface MeUser {
  id: number
  username: string
  displayName: string
  role: WebRole
  mustChangePassword: boolean
  kookUserName?: string
  lastLoginAt?: string | null
  createdAt: string
}

export interface MeResponse {
  user: MeUser
  csrfToken: string
  server: {
    version: string
    dryRun: boolean
    botConnected: boolean
    idleTimeoutSeconds: number
  }
}

export interface WebUserAccount {
  id: number
  username: string
  displayName: string
  role: WebRole
  kookUserId?: string | null
  kookUserName?: string
  disabled: boolean
  mustChangePassword: boolean
  totpEnabled: boolean
  lastLoginAt?: string | null
  createdAt: string
}

export interface SettingsResponse {
  guildId: string
  guildName: string
  categoryId: string
  categoryName: string
  logChannelId: string
  logChannelName: string
  debugChannelId: string
  debugChannelName: string
  outdateHours: number
  kookTokenMasked: string
  hasKookToken: boolean
  missingRequired: string[]
  initializedAt?: string
  dryRun: boolean
  botConnected: boolean
}

export interface RuntimeInfo {
  dryRun: boolean
  botConnected: boolean
  version: string
  goVersion: string
  startedAt: string
  uptimeSeconds: number
  sseSubscribers: number
  sessionIdleSecs: number
  sessionMaxSecs: number
  ticketTimezone: string
  database: { path: string; sizeBytes: number }
  configuration: {
    guildId: string
    categoryId: string
    logChannelId: string
    debugChannelId: string
    outdateHours: number
    kookTokenMasked: string
    hasKookToken: boolean
    missingRequired: string[]
  }
}

export interface PanelRole {
  id: number
  panelId: number
  roleId: string
  roleName: string
  createdAt: string
}

export interface Panel {
  id: number
  channelId: string
  channelName: string
  msgId: string
  title: string
  enabled: boolean
  createdAt: string
  updatedAt: string
  roles?: PanelRole[]
}

export interface EmojiRule {
  id: number
  messageId: string
  channelId: string
  emojiId: string
  roleId: string
  label: string
  enabled: boolean
  createdAt: string
  updatedAt: string
}

export interface EmojiGrant {
  id: number
  kookUserId: string
  ruleId: number
  emojiId: string
  roleId: string
  grantedAt: string
}

export interface AdminRole {
  id: number
  roleId: string
  roleName: string
  createdAt: string
}

export interface RoleMapping {
  id: number
  kookRoleId: string
  kookRoleName: string
  webRole: WebRole
  createdAt: string
  updatedAt: string
}

export interface AuditLog {
  id: number
  actor: string
  actorType: string
  action: string
  target: string
  detail: string
  ip: string
  requestId: string
  createdAt: string
}

export interface KookOption {
  id: string
  name: string
  kind?: string
}

export interface KookOptionsResponse {
  available: boolean
  dryRun: boolean
  items: KookOption[]
  note?: string
}

export interface AuditParams {
  action?: string
  actor?: string
  target?: string
  from?: string
  to?: string
  page?: number
  pageSize?: number
}

export interface ApiErrorPayload {
  error: {
    code: string
    message: string
    requestId?: string
  }
}
