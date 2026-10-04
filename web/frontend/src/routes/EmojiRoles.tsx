/**
 * 表情上角色：规则的增删改与最近发放记录。
 *
 * 使用流程：管理员先在频道内发出“角色选择”消息（可用 KOOK 官方卡片编辑器），
 * 再把该消息的 ID 与「表情 → 角色」的对应关系登记到本页。
 */

import { useState } from "react"
import { Pencil, Plus, SmilePlus, Trash2 } from "lucide-react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"

import { PageHeader } from "@/components/PageHeader"
import { EmptyState, ErrorState, InlineLoader } from "@/components/StateViews"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api } from "@/lib/api"
import { formatDateTime } from "@/lib/format"
import { queryKeys, useEmojiGrants, useEmojiRules, useKookChannels, useKookRoles } from "@/lib/queries"
import { toastError, toastSuccess } from "@/lib/toast"
import type { EmojiRule } from "@/lib/types"

export function EmojiRolesPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const rulesQuery = useEmojiRules()
  const grantsQuery = useEmojiGrants()

  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<EmojiRule | null>(null)
  const [deleting, setDeleting] = useState<EmojiRule | null>(null)

  const invalidate = () => void queryClient.invalidateQueries({ queryKey: queryKeys.emojiRules })

  const createMutation = useMutation({
    mutationFn: (payload: Record<string, unknown>) => api.post<EmojiRule>("/emoji/rules", payload),
    onSuccess: () => {
      toastSuccess(t("emoji.createSuccess"))
      setCreating(false)
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const updateMutation = useMutation({
    mutationFn: (payload: { id: number; body: Record<string, unknown> }) =>
      api.patch<EmojiRule>(`/emoji/rules/${payload.id}`, payload.body),
    onSuccess: () => {
      toastSuccess(t("emoji.updateSuccess"))
      setEditing(null)
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const toggleMutation = useMutation({
    mutationFn: (payload: { id: number; enabled: boolean }) =>
      api.patch(`/emoji/rules/${payload.id}`, { enabled: payload.enabled }),
    onSuccess: () => {
      toastSuccess(t("emoji.toggleSuccess"))
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => api.del(`/emoji/rules/${id}`),
    onSuccess: () => {
      toastSuccess(t("emoji.deleteSuccess"))
      setDeleting(null)
      invalidate()
    },
    onError: (error) => {
      toastError(error)
      setDeleting(null)
    },
  })

  const rules = rulesQuery.data?.items ?? []

  return (
    <div className="space-y-4">
      <PageHeader title={t("emoji.title")} description={t("emoji.description")}>
        <Button size="sm" onClick={() => setCreating(true)}>
          <Plus className="size-4" />
          {t("emoji.create")}
        </Button>
      </PageHeader>

      {rulesQuery.isError ? (
        <ErrorState error={rulesQuery.error} onRetry={() => void rulesQuery.refetch()} />
      ) : rulesQuery.isPending ? (
        <InlineLoader />
      ) : (
        <Card className="py-0">
          <CardContent className="px-0">
            {rules.length === 0 ? (
              <EmptyState title={t("emoji.empty")} icon={<SmilePlus className="size-6" />} />
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("emoji.colEmoji")}</TableHead>
                    <TableHead>{t("emoji.colLabel")}</TableHead>
                    <TableHead className="hidden md:table-cell">{t("emoji.colMessage")}</TableHead>
                    <TableHead className="hidden lg:table-cell">{t("emoji.colChannel")}</TableHead>
                    <TableHead>{t("emoji.colRole")}</TableHead>
                    <TableHead>{t("emoji.colEnabled")}</TableHead>
                    <TableHead className="text-right">{t("common.actions")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rules.map((rule) => (
                    <TableRow key={rule.id}>
                      <TableCell className="text-lg">{rule.emojiId}</TableCell>
                      <TableCell className="text-sm">{rule.label || "—"}</TableCell>
                      <TableCell className="hidden max-w-48 truncate font-mono text-xs md:table-cell">
                        {rule.messageId}
                      </TableCell>
                      <TableCell className="hidden font-mono text-xs lg:table-cell">{rule.channelId || "—"}</TableCell>
                      <TableCell className="font-mono text-xs">{rule.roleId}</TableCell>
                      <TableCell>
                        <Switch
                          checked={rule.enabled}
                          disabled={toggleMutation.isPending}
                          aria-label={t("emoji.colEnabled")}
                          onCheckedChange={(checked) => toggleMutation.mutate({ id: rule.id, enabled: checked })}
                        />
                      </TableCell>
                      <TableCell className="text-right">
                        <div className="flex justify-end gap-1">
                          <Button variant="ghost" size="icon" title={t("common.edit")} onClick={() => setEditing(rule)}>
                            <Pencil className="size-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            title={t("common.delete")}
                            onClick={() => setDeleting(rule)}
                          >
                            <Trash2 className="size-4" />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>
      )}

      <RuleInfoCard />

      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("emoji.grantsTitle")}</CardTitle>
        </CardHeader>
        <CardContent className="px-0">
          {grantsQuery.isPending ? (
            <InlineLoader />
          ) : (grantsQuery.data?.items.length ?? 0) === 0 ? (
            <EmptyState title={t("emoji.grantsEmpty")} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("emoji.colUser")}</TableHead>
                  <TableHead>{t("emoji.colEmoji")}</TableHead>
                  <TableHead>{t("emoji.colRole")}</TableHead>
                  <TableHead>{t("emoji.colGrantedAt")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {grantsQuery.data?.items.map((grant) => (
                  <TableRow key={grant.id}>
                    <TableCell className="font-mono text-xs">{grant.kookUserId}</TableCell>
                    <TableCell className="text-lg">{grant.emojiId}</TableCell>
                    <TableCell className="font-mono text-xs">{grant.roleId}</TableCell>
                    <TableCell className="text-muted-foreground text-xs">{formatDateTime(grant.grantedAt)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* 新增规则 */}
      <Dialog open={creating} onOpenChange={setCreating}>
        <DialogContent className="max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{t("emoji.createTitle")}</DialogTitle>
            <DialogDescription>{t("emoji.createDesc")}</DialogDescription>
          </DialogHeader>
          <RuleForm pending={createMutation.isPending} onSubmit={(values) => createMutation.mutate(values)} />
        </DialogContent>
      </Dialog>

      {/* 编辑规则 */}
      <Dialog open={Boolean(editing)} onOpenChange={(open) => !open && setEditing(null)}>
        <DialogContent className="max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{t("emoji.editTitle")}</DialogTitle>
            <DialogDescription>
              <span className="font-mono text-xs">{editing?.messageId}</span>
            </DialogDescription>
          </DialogHeader>
          {editing ? (
            <RuleForm
              pending={updateMutation.isPending}
              initial={editing}
              onSubmit={(values) =>
                updateMutation.mutate({ id: editing.id, body: { emojiId: values.emojiId, roleId: values.roleId, label: values.label, enabled: values.enabled } })
              }
            />
          ) : null}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={Boolean(deleting)}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={t("emoji.deleteTitle")}
        description={t("emoji.deleteDesc")}
        confirmLabel={t("common.delete")}
        destructive
        pending={deleteMutation.isPending}
        onConfirm={() => deleting && deleteMutation.mutate(deleting.id)}
      />
    </div>
  )
}

/** 规则表单：新增时填写消息 ID，编辑时消息 ID 只读。 */
function RuleForm({
  pending,
  initial,
  onSubmit,
}: {
  pending: boolean
  initial?: EmojiRule
  onSubmit: (values: { messageId: string; channelId: string; emojiId: string; roleId: string; label: string; enabled: boolean }) => void
}) {
  const { t } = useTranslation()
  const rolesQuery = useKookRoles()
  const channelsQuery = useKookChannels()
  const roles = rolesQuery.data?.items ?? []
  const channels = (channelsQuery.data?.items ?? []).filter((channel) => channel.kind !== "category")

  const [messageId, setMessageId] = useState(initial?.messageId ?? "")
  const [channelId, setChannelId] = useState(initial?.channelId ?? "")
  const [emojiId, setEmojiId] = useState(initial?.emojiId ?? "")
  const [roleId, setRoleId] = useState(initial?.roleId ?? "")
  const [label, setLabel] = useState(initial?.label ?? "")
  const [enabled, setEnabled] = useState(initial?.enabled ?? true)

  const valid = messageId.trim().length >= 16 && emojiId.trim().length > 0 && roleId.trim().length > 0

  return (
    <>
      <div className="space-y-4">
        <div className="space-y-2">
          <Label htmlFor="rule-message">{t("emoji.messageLabel")}</Label>
          <Input
            id="rule-message"
            value={messageId}
            readOnly={Boolean(initial)}
            placeholder={t("emoji.messagePlaceholder")}
            className="font-mono text-xs"
            onChange={(event) => setMessageId(event.target.value)}
          />
          <p className="text-muted-foreground text-xs">{t("emoji.messageHint")}</p>
        </div>

        <div className="space-y-2">
          <Label htmlFor="rule-channel">{t("emoji.channelLabel")}</Label>
          {initial ? (
            <Input id="rule-channel" value={channelId} readOnly className="font-mono text-xs" />
          ) : channels.length > 0 ? (
            <Select value={channelId || "none"} onValueChange={(value) => setChannelId(value === "none" ? "" : value)}>
              <SelectTrigger>
                <SelectValue placeholder={t("common.none")} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">{t("common.none")}</SelectItem>
                {channels.map((channel) => (
                  <SelectItem key={channel.id} value={channel.id}>
                    {channel.name} · {channel.id}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          ) : (
            <Input
              id="rule-channel"
              inputMode="numeric"
              value={channelId}
              onChange={(event) => setChannelId(event.target.value)}
            />
          )}
        </div>

        <div className="space-y-2">
          <Label htmlFor="rule-emoji">{t("emoji.emojiLabel")}</Label>
          <Input
            id="rule-emoji"
            value={emojiId}
            maxLength={32}
            placeholder="❤"
            onChange={(event) => setEmojiId(event.target.value)}
          />
          <p className="text-muted-foreground text-xs">{t("emoji.emojiHint")}</p>
        </div>

        <div className="space-y-2">
          <Label htmlFor="rule-role">{t("emoji.roleLabel")}</Label>
          {roles.length > 0 ? (
            <Select value={roleId} onValueChange={setRoleId}>
              <SelectTrigger>
                <SelectValue placeholder={t("emoji.rolePickerLabel")} />
              </SelectTrigger>
              <SelectContent>
                {roles.map((role) => (
                  <SelectItem key={role.id} value={role.id}>
                    {role.name} · {role.id}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          ) : null}
          <Input
            id="rule-role"
            inputMode="numeric"
            value={roleId}
            placeholder="10002"
            onChange={(event) => setRoleId(event.target.value)}
          />
        </div>

        <div className="space-y-2">
          <Label htmlFor="rule-label">{t("emoji.labelLabel")}</Label>
          <Input id="rule-label" value={label} maxLength={64} onChange={(event) => setLabel(event.target.value)} />
        </div>

        <div className="flex items-center justify-between rounded-lg border p-3">
          <Label htmlFor="rule-enabled">{t("emoji.colEnabled")}</Label>
          <Switch id="rule-enabled" checked={enabled} onCheckedChange={setEnabled} />
        </div>
      </div>

      <DialogFooter>
        <Button
          disabled={pending || !valid}
          onClick={() =>
            onSubmit({
              messageId: messageId.trim(),
              channelId: channelId.trim(),
              emojiId: emojiId.trim(),
              roleId: roleId.trim(),
              label: label.trim(),
              enabled,
            })
          }
        >
          {pending ? t("common.saving") : t("common.save")}
        </Button>
      </DialogFooter>
    </>
  )
}

/** 使用说明与安全提示。 */
function RuleInfoCard() {
  const { t } = useTranslation()
  return (
    <Alert>
      <SmilePlus className="size-4" />
      <AlertDescription>
        {t("emoji.createDesc")}
        <Badge variant="outline" className="ml-2 font-normal">
          {t("bot.rolesTitle")}
        </Badge>
      </AlertDescription>
    </Alert>
  )
}
