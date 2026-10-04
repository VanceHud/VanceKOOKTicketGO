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

export interface DurationStats {
  count: number
  avgSeconds: number
  p50Seconds: number
  p90Seconds: number
}

export interface HourBucket {
  hour: number
  opened: number
  closed: number
}

export interface CloserStat {
  name: string
  closed: number
  avgResolutionSeconds: number
}

export interface SourceStat {
  /** 形如 "频道ID|频道名"（服务端已附带可读名称）。 */
  channelId: string
  opened: number
  closed: number
  closedRate: number
}

export interface StatsAnalytics {
  generatedAt: string
  rangeDays: number
  total: number
  closed: number
  closedRate: number
  firstReply: DurationStats
  resolution: DurationStats
  hourly: HourBucket[]
  closers: CloserStat[]
  sources: SourceStat[]
  archivedMessages: number
  avgMessagesPerTicket: number
  openedToday: number
  openedYesterday: number
  closedToday: number
  closedYesterday: number
}

export interface MeResponse {
  user: MeUser
  csrfToken: string
  server: {
    version: string
    dryRun: boolean
    botConnected: boolean
    idleTimeoutSeconds: number
    /** 业务时区（默认 Asia/Shanghai），前端据此渲染所有时间。 */
    ticketTimezone: string
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

export interface BotStatus {
  running: boolean
  connected: boolean
  botId?: string
  botName?: string
  guildId?: string
  guildName?: string
  sessionId?: string
  eventsHandled: number
  lastError?: string
  connectedAt?: string
  startedAt?: string
  lastEventAt?: string
}

export interface RuntimeInfo {
  dryRun: boolean
  botConnected: boolean
  bot?: BotStatus
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
  buttonText: string
  /** 开单成功后机器人在工单频道单独发送的 KMarkdown 内容；为空则不发送。 */
  openMessage: string
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

/** KOOK 游戏对象（对应 object-game）。 */
export interface KookGame {
  id: number
  name: string
  /** 0 游戏 / 1 VUP / 2 进程。 */
  type: number
  options?: string
  kmhookAdmin?: boolean
  processName?: string[]
  productName?: string[]
  icon?: string
}

/** 游戏列表响应；机器人离线时 available 为 false。 */
export interface GameListResponse {
  available: boolean
  dryRun: boolean
  type: number
  items: KookGame[]
  note?: string
}

/** 动态类型：1 游戏 / 2 音乐（与平台 data_type 一致）。 */
export type ActivityDataType = 1 | 2

/** 音乐动态支持的软件枚举。 */
export type MusicSoftware = "cloudmusic" | "qqmusic" | "kugou"

/** 机器人当前在玩/在听动态（服务端持久化的期望状态）。 */
export interface BotActivityState {
  dataType: ActivityDataType
  gameId?: number
  gameName?: string
  musicName?: string
  singer?: string
  software?: MusicSoftware | string
  actor?: string
  startedAt: string
}

/** 动态页初始化数据。 */
export interface BotActivityResponse {
  connected: boolean
  dryRun: boolean
  autoRestore: boolean
  current: BotActivityState | null
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
