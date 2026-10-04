/**
 * 工单面板管理：新建、重建卡片、编辑文案、启停、删除，以及面板级管理员角色的增删。
 *
 * 说明：
 * - 同一频道允许存在多张工单按钮卡片，每张卡片各自携带独立的文案与面板管理员角色；
 * - 面板卡片必须由机器人发送（按钮 value 需要机器人签名并内嵌面板 ID），
 *   因此新建/重建需要机器人在线；不在线时界面给出明确提示而不是静默失败；
 * - 面板文案按 KMarkdown 渲染并支持多行，编辑时提供实时预览。
 */

import { useState } from "react"
import { LayoutPanelLeft, Pencil, Plus, RefreshCw, Trash2, UserPlus, X } from "lucide-react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"

import { KMarkdownPreview } from "@/components/KMarkdownPreview"
import { PageHeader } from "@/components/PageHeader"
import { EmptyState, ErrorState, InlineLoader } from "@/components/StateViews"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
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
import { Textarea } from "@/components/ui/textarea"
import { api } from "@/lib/api"
import { useRuntimeInfo } from "@/lib/queries"
import { formatDateTime } from "@/lib/format"
import { queryKeys, useKookChannels, useKookRoles, usePanels } from "@/lib/queries"
import { toastError, toastSuccess } from "@/lib/toast"
import type { Panel } from "@/lib/types"

/** 与后端 maxPanelTitleLength 保持一致。 */
const maxPanelTitleLength = 2000

export function PanelsPage() {
  const { t, i18n } = useTranslation()
  const queryClient = useQueryClient()
  const panelsQuery = usePanels()
  const runtimeQuery = useRuntimeInfo()
  const channelsQuery = useKookChannels()
  const rolesQuery = useKookRoles()

  const [createOpen, setCreateOpen] = useState(false)
  const [deleting, setDeleting] = useState<Panel | null>(null)
  const [editing, setEditing] = useState<Panel | null>(null)
  const [rolePanel, setRolePanel] = useState<Panel | null>(null)

  const botConnected = runtimeQuery.data?.botConnected ?? false

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: queryKeys.panels })
    void queryClient.invalidateQueries({ queryKey: queryKeys.kookChannels })
  }

  const createMutation = useMutation({
    mutationFn: (payload: { channelId: string; title: string; buttonText: string }) =>
      api.post<Panel>("/panels", payload),
    onSuccess: () => {
      toastSuccess(t("panels.createSuccess"))
      setCreateOpen(false)
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const refreshMutation = useMutation({
    mutationFn: (id: number) => api.post<Panel>(`/panels/${id}/refresh`),
    onSuccess: () => {
      toastSuccess(t("panels.refreshSuccess"))
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  // 编辑文案：先保存文案与按钮文字；若机器人在线则同时重建卡片，让修改立即生效。
  const editMutation = useMutation({
    mutationFn: async (payload: { id: number; title: string; buttonText: string; rebuild: boolean }) => {
      await api.patch<Panel>(`/panels/${payload.id}`, {
        title: payload.title,
        buttonText: payload.buttonText,
      })
      if (payload.rebuild) {
        await api.post<Panel>(`/panels/${payload.id}/refresh`)
      }
      return payload.rebuild
    },
    onSuccess: (rebuilt) => {
      toastSuccess(rebuilt ? t("panels.editSuccess") : t("panels.editSaved"))
      setEditing(null)
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const toggleMutation = useMutation({
    mutationFn: (payload: { id: number; enabled: boolean }) =>
      api.patch<Panel>(`/panels/${payload.id}`, { enabled: payload.enabled }),
    onSuccess: (_data, variables) => {
      toastSuccess(variables.enabled ? t("panels.enableSuccess") : t("panels.disableSuccess"))
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => api.del(`/panels/${id}`),
    onSuccess: () => {
      toastSuccess(t("panels.deleteSuccess"))
      setDeleting(null)
      invalidate()
    },
    onError: (error) => {
      toastError(error)
      setDeleting(null)
    },
  })

  const addRoleMutation = useMutation({
    mutationFn: (payload: { panelId: number; roleId: string }) =>
      api.post(`/panels/${payload.panelId}/roles`, { roleId: payload.roleId }),
    onSuccess: () => {
      toastSuccess(t("panels.roleAddSuccess"))
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const removeRoleMutation = useMutation({
    mutationFn: (payload: { panelId: number; roleId: string }) =>
      api.del(`/panels/${payload.panelId}/roles/${encodeURIComponent(payload.roleId)}`),
    onSuccess: () => {
      toastSuccess(t("panels.roleRemoveSuccess"))
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const panels = panelsQuery.data?.items ?? []

  return (
    <div className="space-y-4">
      <PageHeader title={t("panels.title")} description={t("panels.description")}>
        <Button size="sm" disabled={!botConnected} onClick={() => setCreateOpen(true)}>
          <Plus className="size-4" />
          {t("panels.create")}
        </Button>
      </PageHeader>

      {!botConnected ? (
        <Alert>
          <AlertDescription>{t("panels.botOffline")}</AlertDescription>
        </Alert>
      ) : null}

      {panelsQuery.isError ? (
        <ErrorState error={panelsQuery.error} onRetry={() => void panelsQuery.refetch()} />
      ) : panelsQuery.isPending ? (
        <InlineLoader />
      ) : panels.length === 0 ? (
        <Card>
          <CardContent>
            <EmptyState title={t("panels.empty")} icon={<LayoutPanelLeft className="size-6" />} />
          </CardContent>
        </Card>
      ) : (
        <Card className="py-0">
          <CardContent className="px-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("panels.colChannel")}</TableHead>
                  <TableHead className="hidden md:table-cell">{t("panels.colTitle")}</TableHead>
                  <TableHead>{t("panels.colRoles")}</TableHead>
                  <TableHead>{t("panels.enabledLabel")}</TableHead>
                  <TableHead className="text-right">{t("common.actions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {panels.map((panel) => (
                  <TableRow key={panel.id}>
                    <TableCell>
                      <div className="space-y-0.5">
                        <p className="text-sm font-medium">{panel.channelName || "—"}</p>
                        <p className="text-muted-foreground font-mono text-xs">{panel.channelId}</p>
                        <p className="text-muted-foreground text-xs">
                          {formatDateTime(panel.updatedAt, i18n.language)}
                        </p>
                      </div>
                    </TableCell>
                    <TableCell className="hidden max-w-56 truncate text-sm md:table-cell">
                      {panel.title || "—"}
                    </TableCell>
                    <TableCell>
                      <div className="flex flex-wrap items-center gap-1">
                        {(panel.roles ?? []).length === 0 ? (
                          <span className="text-muted-foreground text-xs">{t("common.none")}</span>
                        ) : (
                          panel.roles?.map((role) => (
                            <Badge key={role.id} variant="outline" className="gap-1 font-normal">
                              {role.roleName || role.roleId}
                              <button
                                type="button"
                                aria-label={t("common.delete")}
                                className="hover:text-destructive"
                                onClick={() => removeRoleMutation.mutate({ panelId: panel.id, roleId: role.roleId })}
                              >
                                <X className="size-3" />
                              </button>
                            </Badge>
                          ))
                        )}
                        <Button
                          variant="ghost"
                          size="icon"
                          className="size-6"
                          title={t("panels.roleAdd")}
                          onClick={() => setRolePanel(panel)}
                        >
                          <UserPlus className="size-3.5" />
                        </Button>
                      </div>
                    </TableCell>
                    <TableCell>
                      <Switch
                        checked={panel.enabled}
                        disabled={toggleMutation.isPending}
                        aria-label={t("panels.enabledLabel")}
                        onCheckedChange={(checked) => toggleMutation.mutate({ id: panel.id, enabled: checked })}
                      />
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="icon"
                          title={t("panels.edit")}
                          onClick={() => setEditing(panel)}
                        >
                          <Pencil className="size-4" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          title={t("panels.refresh")}
                          disabled={!botConnected || refreshMutation.isPending}
                          onClick={() => refreshMutation.mutate(panel.id)}
                        >
                          <RefreshCw className={refreshMutation.isPending ? "size-4 animate-spin" : "size-4"} />
                        </Button>
                        <Button variant="ghost" size="icon" title={t("common.delete")} onClick={() => setDeleting(panel)}>
                          <Trash2 className="size-4" />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      {/* 新建面板 */}
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle>{t("panels.createTitle")}</DialogTitle>
            <DialogDescription>{t("panels.createDesc")}</DialogDescription>
          </DialogHeader>
          <CreatePanelForm
            pending={createMutation.isPending}
            channels={channelsQuery.data?.items ?? []}
            onSubmit={(payload) => createMutation.mutate(payload)}
          />
        </DialogContent>
      </Dialog>

      {/* 编辑文案 */}
      <Dialog open={Boolean(editing)} onOpenChange={(open) => !open && setEditing(null)}>
        <DialogContent className="sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle>{t("panels.editTitle")}</DialogTitle>
            <DialogDescription>{t("panels.editDesc")}</DialogDescription>
          </DialogHeader>
          {editing ? (
            <EditPanelForm
              pending={editMutation.isPending}
              initialTitle={editing.title}
              initialButtonText={editing.buttonText}
              rebuild={botConnected}
              onSubmit={(title, buttonText) =>
                editMutation.mutate({ id: editing.id, title, buttonText, rebuild: botConnected })
              }
            />
          ) : null}
        </DialogContent>
      </Dialog>

      {/* 添加面板角色 */}
      <Dialog open={Boolean(rolePanel)} onOpenChange={(open) => !open && setRolePanel(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("panels.rolesTitle")}</DialogTitle>
            <DialogDescription>{t("panels.rolesDesc")}</DialogDescription>
          </DialogHeader>
          {rolePanel ? (
            <RolePicker
              pending={addRoleMutation.isPending}
              roles={(rolesQuery.data?.items ?? []).filter(
                (role) => !(rolePanel.roles ?? []).some((existing) => existing.roleId === role.id),
              )}
              onSubmit={(roleId) =>
                addRoleMutation.mutate(
                  { panelId: rolePanel.id, roleId },
                  { onSuccess: () => setRolePanel(null) },
                )
              }
            />
          ) : null}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={Boolean(deleting)}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={t("panels.deleteTitle")}
        description={t("panels.deleteDesc")}
        confirmLabel={t("common.delete")}
        destructive
        pending={deleteMutation.isPending}
        onConfirm={() => deleting && deleteMutation.mutate(deleting.id)}
      />
    </div>
  )
}

/** 新建面板表单：优先从服务器频道列表选择，也支持手填 ID。 */
function CreatePanelForm({
  pending,
  channels,
  onSubmit,
}: {
  pending: boolean
  channels: { id: string; name: string; kind?: string }[]
  onSubmit: (payload: { channelId: string; title: string; buttonText: string }) => void
}) {
  const { t } = useTranslation()
  const playable = channels.filter((channel) => channel.kind !== "category")
  const [channelId, setChannelId] = useState(playable[0]?.id ?? "")
  const [title, setTitle] = useState("")
  const [buttonText, setButtonText] = useState("")

  return (
    <>
      <div className="space-y-4">
        {playable.length > 0 ? (
          <div className="space-y-2">
            <Label>{t("panels.channelLabel")}</Label>
            <Select value={channelId} onValueChange={setChannelId}>
              <SelectTrigger>
                <SelectValue placeholder={t("panels.channelPlaceholder")} />
              </SelectTrigger>
              <SelectContent>
                {playable.map((channel) => (
                  <SelectItem key={channel.id} value={channel.id}>
                    {channel.name} · {channel.id}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        ) : null}
        <div className="space-y-2">
          <Label htmlFor="panel-channel">{t("panels.channelManual")}</Label>
          <Input
            id="panel-channel"
            inputMode="numeric"
            value={channelId}
            placeholder="2000000000000001"
            onChange={(event) => setChannelId(event.target.value)}
          />
        </div>
        <PanelTextEditor value={title} onChange={setTitle} idPrefix="panel-create" />
        <div className="space-y-2">
          <Label htmlFor="panel-button">{t("panels.buttonLabel")}</Label>
          <Input
            id="panel-button"
            maxLength={32}
            value={buttonText}
            placeholder="ticket"
            onChange={(event) => setButtonText(event.target.value)}
          />
        </div>
      </div>
      <DialogFooter>
        <Button
          disabled={pending || channelId.trim().length === 0}
          onClick={() =>
            onSubmit({ channelId: channelId.trim(), title: title.trim(), buttonText: buttonText.trim() })
          }
        >
          {pending ? t("common.saving") : t("common.create")}
        </Button>
      </DialogFooter>
    </>
  )
}

/** 编辑面板文案：保存后按需重建卡片。 */
function EditPanelForm({
  pending,
  initialTitle,
  initialButtonText,
  rebuild,
  onSubmit,
}: {
  pending: boolean
  initialTitle: string
  initialButtonText: string
  rebuild: boolean
  onSubmit: (title: string, buttonText: string) => void
}) {
  const { t } = useTranslation()
  const [title, setTitle] = useState(initialTitle)
  const [buttonText, setButtonText] = useState(initialButtonText)

  return (
    <>
      <div className="space-y-4">
        <PanelTextEditor value={title} onChange={setTitle} idPrefix="panel-edit" />
        <div className="space-y-2">
          <Label htmlFor="panel-edit-button">{t("panels.buttonLabel")}</Label>
          <Input
            id="panel-edit-button"
            maxLength={32}
            value={buttonText}
            placeholder="ticket"
            onChange={(event) => setButtonText(event.target.value)}
          />
        </div>
      </div>
      <DialogFooter>
        <Button disabled={pending} onClick={() => onSubmit(title.trim(), buttonText.trim())}>
          {pending ? t("common.saving") : rebuild ? t("panels.saveAndRefresh") : t("common.save")}
        </Button>
      </DialogFooter>
    </>
  )
}

/** 面板文案编辑器：左侧多行输入，右侧 KMarkdown 实时预览。 */
function PanelTextEditor({
  value,
  onChange,
  idPrefix,
}: {
  value: string
  onChange: (value: string) => void
  idPrefix: string
}) {
  const { t } = useTranslation()

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <Label htmlFor={`${idPrefix}-title`}>{t("panels.titleLabel")}</Label>
        <span className="text-muted-foreground text-xs">
          {value.length}/{maxPanelTitleLength}
        </span>
      </div>
      <div className="grid gap-3 md:grid-cols-2">
        <Textarea
          id={`${idPrefix}-title`}
          rows={8}
          maxLength={maxPanelTitleLength}
          value={value}
          className="max-h-64 min-h-40 font-mono text-sm"
          placeholder={"请点击右侧按钮发起工单\n\n**处理范围**\n- 账号问题\n- 充值问题"}
          onChange={(event) => onChange(event.target.value)}
        />
        <div className="rounded-lg border bg-muted/30 p-3">
          <p className="text-muted-foreground mb-2 text-xs font-medium">{t("panels.preview")}</p>
          <div className="max-h-56 overflow-y-auto">
            <KMarkdownPreview source={value} emptyText={t("panels.previewEmpty")} />
          </div>
        </div>
      </div>
      <p className="text-muted-foreground text-xs">{t("panels.titleHint")}</p>
    </div>
  )
}

/** 角色选择器：从服务器已有角色中挑选。 */
function RolePicker({
  pending,
  roles,
  onSubmit,
}: {
  pending: boolean
  roles: { id: string; name: string }[]
  onSubmit: (roleId: string) => void
}) {
  const { t } = useTranslation()
  const [roleId, setRoleId] = useState("")

  if (roles.length === 0) {
    return (
      <>
        <p className="text-muted-foreground text-sm">{t("common.empty")}</p>
        <DialogFooter>
          <Button disabled>{t("common.create")}</Button>
        </DialogFooter>
      </>
    )
  }

  return (
    <>
      <div className="space-y-2">
        <Label>{t("emoji.rolePickerLabel")}</Label>
        <Select value={roleId} onValueChange={setRoleId}>
          <SelectTrigger>
            <SelectValue placeholder={t("roles.mappingRoleIdPlaceholder")} />
          </SelectTrigger>
          <SelectContent>
            {roles.map((role) => (
              <SelectItem key={role.id} value={role.id}>
                {role.name} · {role.id}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <DialogFooter>
        <Button disabled={pending || roleId === ""} onClick={() => onSubmit(roleId)}>
          {pending ? t("common.saving") : t("panels.roleAdd")}
        </Button>
      </DialogFooter>
    </>
  )
}
