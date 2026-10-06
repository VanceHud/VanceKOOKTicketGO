/**
 * 工单详情：只读聊天记录 + 内部备注 + 管理操作（锁定 / 重新激活 / 关闭 / 导出）。
 *
 * 说明：WebUI 不提供“以机器人身份发言”，对话仍在 KOOK 内进行（按需求约定）。
 * 操作按钮仅对客服及以上角色展示，服务端仍会二次校验权限。
 */

import { useEffect, useRef, useState } from "react"
import {
  ArrowLeft,
  Bot,
  Download,
  LockKeyhole,
  MessageSquare,
  RefreshCw,
  StickyNote,
  UnlockKeyhole,
  User,
  XCircle,
} from "lucide-react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"
import { Link, useParams, useSearchParams } from "react-router"

import { StatusBadge } from "@/components/Badges"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { KookCardView } from "@/components/KookCardView"
import { MessageBody } from "@/components/MessageBody"
import { PageHeader } from "@/components/PageHeader"
import { EmptyState, ErrorState, InlineLoader } from "@/components/StateViews"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Separator } from "@/components/ui/separator"
import { Textarea } from "@/components/ui/textarea"
import { api, downloadExport } from "@/lib/api"
import { useAuth } from "@/lib/auth"
import { formatDateTime, formatRelative, roleAtLeast } from "@/lib/format"
import { timezoneLabel } from "@/lib/timezone"
import { queryKeys, useTicket, useTicketMessages, useTicketNotes } from "@/lib/queries"
import { toastError, toastSuccess } from "@/lib/toast"
import type { Ticket } from "@/lib/types"
import { cn } from "@/lib/utils"

type DialogKind = "close" | "lock" | "reopen"

export function TicketDetailPage() {
  const { no } = useParams<{ no: string }>()
  const { t, i18n } = useTranslation()
  const { me } = useAuth()
  const queryClient = useQueryClient()
  const [searchParams, setSearchParams] = useSearchParams()

  const canOperate = roleAtLeast(me?.user.role, "staff")

  const ticketQuery = useTicket(no)
  const messagesQuery = useTicketMessages(no)
  const notesQuery = useTicketNotes(no)
  const ticket = ticketQuery.data

  const timelineEndRef = useRef<HTMLDivElement | null>(null)
  const [dialog, setDialog] = useState<DialogKind | null>(null)
  const [closeNote, setCloseNote] = useState("")
  const [noteDraft, setNoteDraft] = useState("")

  // 命令面板通过 ?action= 触发操作，这里消费后清理参数，避免刷新重复弹出。
  useEffect(() => {
    const action = searchParams.get("action")
    if (action === "close" || action === "lock" || action === "reopen") {
      setDialog(action)
      const next = new URLSearchParams(searchParams)
      next.delete("action")
      setSearchParams(next, { replace: true })
    }
  }, [searchParams, setSearchParams])

  // 聊天记录按时间正序展示，加载完成后自动滚到底部（与 IM 阅读习惯一致）。
  const messageCount = messagesQuery.data?.items.length ?? 0
  useEffect(() => {
    timelineEndRef.current?.scrollIntoView({ block: "end" })
  }, [messageCount])

  const invalidateTicket = () => {
    void queryClient.invalidateQueries({ queryKey: queryKeys.ticket(no ?? "") })
    void queryClient.invalidateQueries({ queryKey: ["tickets"] })
    void queryClient.invalidateQueries({ queryKey: ["stats"] })
  }

  const closeMutation = useMutation<Ticket, unknown, void>({
    mutationFn: () => api.post<Ticket>(`/tickets/${encodeURIComponent(no as string)}/close`, { note: closeNote.trim() }),
    onSuccess: () => {
      toastSuccess(t("ticket.toastClosed"))
      setDialog(null)
      setCloseNote("")
      invalidateTicket()
    },
    onError: (error) => {
      toastError(error)
      setDialog(null)
    },
  })

  const lockMutation = useMutation<Ticket, unknown, void>({
    mutationFn: () => api.post<Ticket>(`/tickets/${encodeURIComponent(no as string)}/lock`, { reason: "manual" }),
    onSuccess: () => {
      toastSuccess(t("ticket.toastLocked"))
      setDialog(null)
      invalidateTicket()
    },
    onError: (error) => {
      toastError(error)
      setDialog(null)
    },
  })

  const reopenMutation = useMutation<Ticket, unknown, void>({
    mutationFn: () => api.post<Ticket>(`/tickets/${encodeURIComponent(no as string)}/reopen`),
    onSuccess: () => {
      toastSuccess(t("ticket.toastReopened"))
      setDialog(null)
      invalidateTicket()
    },
    onError: (error) => {
      toastError(error)
      setDialog(null)
    },
  })

  const noteMutation = useMutation({
    mutationFn: (content: string) => api.post(`/tickets/${encodeURIComponent(no as string)}/notes`, { content }),
    onSuccess: () => {
      toastSuccess(t("ticket.notesAdded"))
      setNoteDraft("")
      void queryClient.invalidateQueries({ queryKey: queryKeys.notes(no ?? "") })
    },
    onError: (error) => toastError(error),
  })

  if (ticketQuery.isPending) return <InlineLoader />
  if (ticketQuery.isError) return <ErrorState error={ticketQuery.error} onRetry={() => void ticketQuery.refetch()} />
  if (!ticket) return <ErrorState error={new Error(t("ticket.notFound"))} />

  const pending = closeMutation.isPending || lockMutation.isPending || reopenMutation.isPending

  return (
    <div className="space-y-5">
      <div className="flex items-center gap-2">
        <Button asChild variant="ghost" size="sm" className="text-muted-foreground">
          <Link to="/tickets">
            <ArrowLeft className="size-4" />
            {t("ticket.backToList")}
          </Link>
        </Button>
      </div>

      <PageHeader
        title={ticket.no}
        description={`${ticket.userName} · ${t("ticket.startedAt")} ${formatDateTime(ticket.startedAt, i18n.language)}`}
      >
        <StatusBadge status={ticket.status} />
        {ticket.status === "locked" && ticket.lockReason ? (
          <Badge variant="outline" className="border-status-locked/40 text-status-locked font-normal">
            {t(`status.lockReason.${ticket.lockReason}`)}
          </Badge>
        ) : null}

        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" size="sm">
              <Download className="size-4" />
              {t("ticket.exportTitle")}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuLabel>{t("ticket.exportTitle")}</DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={() => downloadExport(ticket.no, "json")}>{t("ticket.exportJson")}</DropdownMenuItem>
            <DropdownMenuItem onClick={() => downloadExport(ticket.no, "csv")}>{t("ticket.exportCsv")}</DropdownMenuItem>
            <DropdownMenuItem onClick={() => downloadExport(ticket.no, "html")}>{t("ticket.exportHtml")}</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>

        {canOperate && ticket.status !== "closed" ? (
          <>
            {ticket.status === "locked" ? (
              <Button size="sm" onClick={() => setDialog("reopen")}>
                <UnlockKeyhole className="size-4" />
                {t("ticket.actionReopen")}
              </Button>
            ) : (
              <Button size="sm" variant="outline" onClick={() => setDialog("lock")}>
                <LockKeyhole className="size-4" />
                {t("ticket.actionLock")}
              </Button>
            )}
            <Button size="sm" variant="destructive" onClick={() => setDialog("close")}>
              <XCircle className="size-4" />
              {t("ticket.actionClose")}
            </Button>
          </>
        ) : null}

        {!canOperate ? (
          <Badge variant="outline" className="text-muted-foreground font-normal">
            {t("ticket.readonlyHint")}
          </Badge>
        ) : null}
      </PageHeader>

      <div className="grid gap-4 lg:grid-cols-3">
        {/* 聊天记录 */}
        <Card className="lg:col-span-2">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <MessageSquare className="size-4" />
              {t("ticket.timeline")}
            </CardTitle>
            <CardDescription>{t("ticket.timelineHint")}</CardDescription>
          </CardHeader>
          <CardContent className="px-0">
            {messagesQuery.isPending ? (
              <InlineLoader />
            ) : messagesQuery.isError ? (
              <div className="px-6">
                <ErrorState error={messagesQuery.error} onRetry={() => void messagesQuery.refetch()} />
              </div>
            ) : (messagesQuery.data?.items.length ?? 0) === 0 ? (
              <EmptyState title={t("ticket.timelineEmpty")} />
            ) : (
              <ScrollArea className="h-[520px] px-6">
                <ul className="space-y-4 pb-4">
                  {messagesQuery.data?.items.map((message) => {
                    const isSystem = message.type === "system"
                    return (
                      <li key={message.id} className={cn("flex gap-3", isSystem && "opacity-80")}>
                        <div
                          className={cn(
                            "mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-full border",
                            message.isBot ? "bg-primary/10 text-primary" : "bg-muted text-muted-foreground",
                          )}
                        >
                          {message.isBot ? <Bot className="size-4" /> : <User className="size-4" />}
                        </div>
                        <div className="min-w-0 flex-1 space-y-1">
                          <div className="flex flex-wrap items-center gap-2 text-xs">
                            <span className="font-medium">{message.userName || message.userId}</span>
                            {message.isBot ? (
                              <Badge variant="secondary" className="h-4 px-1.5 text-[10px]">
                                {t("ticket.fromBot")}
                              </Badge>
                            ) : null}
                            {message.type !== "text" ? (
                              <Badge variant="outline" className="h-4 px-1.5 text-[10px] font-normal">
                                {t(`msgType.${message.type}`, { defaultValue: message.type })}
                              </Badge>
                            ) : null}
                            <span className="text-muted-foreground" title={formatDateTime(message.createdAt, i18n.language)}>
                              {formatDateTime(message.createdAt, i18n.language)}
                            </span>
                          </div>
                          {message.type === "card" ? (
                            <KookCardView json={message.cardJson} fallback={message.content} />
                          ) : (
                            <div className="bg-muted/50 rounded-lg px-3 py-2 text-sm break-words">
                              <MessageBody message={message} />
                            </div>
                          )}
                        </div>
                      </li>
                    )
                  })}
                </ul>
                <div ref={timelineEndRef} />
              </ScrollArea>
            )}
          </CardContent>
        </Card>

        <div className="space-y-4">
          {/* 工单信息 */}
          <Card>
            <CardHeader>
              <CardTitle className="text-base">{t("ticket.overview")}</CardTitle>
              <CardDescription>{timezoneLabel(i18n.language)}</CardDescription>
            </CardHeader>
            <CardContent className="space-y-2 text-sm">
              <InfoRow label={t("tickets.colUser")} value={`${ticket.userName}（${ticket.userId}）`} />
              <InfoRow label={t("ticket.type")} value={ticket.typeName || t("common.none")} />
              <InfoRow label={t("ticket.startedAt")} value={formatDateTime(ticket.startedAt, i18n.language)} />
              <InfoRow label={t("ticket.messageCount")} value={String(ticket.messageCount)} />
              <InfoRow
                label={t("ticket.firstReply")}
                value={ticket.firstReplyAt ? formatDateTime(ticket.firstReplyAt, i18n.language) : t("common.none")}
              />
              <InfoRow label={t("ticket.ticketChannel")} value={ticket.channelId || t("common.none")} mono />
              <InfoRow label={t("ticket.sourceChannel")} value={ticket.sourceChannelId || t("common.none")} mono />
              {ticket.lockedAt ? (
                <InfoRow label={t("ticket.lockedAt")} value={formatDateTime(ticket.lockedAt, i18n.language)} />
              ) : null}
              {ticket.closedAt ? (
                <>
                  <Separator />
                  <InfoRow label={t("ticket.closedAt")} value={formatDateTime(ticket.closedAt, i18n.language)} />
                  <InfoRow label={t("ticket.closedBy")} value={ticket.closedByName || ticket.closedBy || t("common.none")} />
                </>
              ) : null}
            </CardContent>
          </Card>

          {/* 内部备注 */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <StickyNote className="size-4" />
                {t("ticket.notesTitle")}
              </CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              {canOperate ? (
                <div className="space-y-2">
                  <Textarea
                    rows={3}
                    maxLength={2000}
                    placeholder={t("ticket.notesPlaceholder")}
                    value={noteDraft}
                    onChange={(event) => setNoteDraft(event.target.value)}
                  />
                  <Button
                    size="sm"
                    className="w-full"
                    disabled={noteMutation.isPending || noteDraft.trim().length === 0}
                    onClick={() => noteMutation.mutate(noteDraft.trim())}
                  >
                    {noteMutation.isPending ? <RefreshCw className="size-4 animate-spin" /> : null}
                    {t("ticket.notesAdd")}
                  </Button>
                </div>
              ) : null}

              {notesQuery.isPending ? (
                <InlineLoader className="py-4" />
              ) : (notesQuery.data?.items.length ?? 0) === 0 ? (
                <EmptyState title={t("ticket.notesEmpty")} className="py-6" />
              ) : (
                <ul className="space-y-3">
                  {notesQuery.data?.items.map((note) => (
                    <li key={note.id} className="bg-muted/40 space-y-1 rounded-lg border p-3 text-sm">
                      <div className="text-muted-foreground flex items-center justify-between text-xs">
                        <span className="font-medium">{note.authorName || note.authorId}</span>
                        <span title={formatDateTime(note.createdAt, i18n.language)}>
                          {formatRelative(note.createdAt, i18n.language)}
                        </span>
                      </div>
                      <p className="break-words whitespace-pre-wrap">{note.content}</p>
                    </li>
                  ))}
                </ul>
              )}
            </CardContent>
          </Card>
        </div>
      </div>

      <ConfirmDialog
        open={dialog === "close"}
        onOpenChange={(open) => setDialog(open ? "close" : null)}
        title={t("ticket.closeDialogTitle")}
        description={t("ticket.closeDialogDesc")}
        confirmLabel={t("ticket.actionClose")}
        destructive
        pending={pending}
        withNote
        noteLabel={t("ticket.closeNoteLabel")}
        noteValue={closeNote}
        onNoteChange={setCloseNote}
        onConfirm={() => closeMutation.mutate()}
      />

      <ConfirmDialog
        open={dialog === "lock"}
        onOpenChange={(open) => setDialog(open ? "lock" : null)}
        title={t("ticket.lockDialogTitle")}
        description={t("ticket.lockDialogDesc")}
        confirmLabel={t("ticket.actionLock")}
        pending={pending}
        onConfirm={() => lockMutation.mutate()}
      />

      <ConfirmDialog
        open={dialog === "reopen"}
        onOpenChange={(open) => setDialog(open ? "reopen" : null)}
        title={t("ticket.reopenDialogTitle")}
        description={t("ticket.reopenDialogDesc")}
        confirmLabel={t("ticket.actionReopen")}
        pending={pending}
        onConfirm={() => reopenMutation.mutate()}
      />
    </div>
  )
}

function InfoRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-3">
      <span className="text-muted-foreground shrink-0 text-xs">{label}</span>
      <span className={cn("min-w-0 text-right text-xs break-all", mono && "font-mono")}>{value}</span>
    </div>
  )
}
