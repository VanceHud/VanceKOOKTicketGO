/**
 * 工单类型管理：一个类型对应多个面板（频道里的开单按钮卡片）。
 *
 * 说明：
 * - 类型是分类与权限的单位：管理员角色挂在类型上，该类型下所有面板开出的工单
 *   都会下发这些角色、展示类型名，并用「类型｜短编号｜昵称」命名工单频道；
 * - 面板是投放点：同一类型可以在多个频道各放一张或多张卡片；
 * - 面板卡片必须由机器人发送（按钮 value 需要机器人签名并内嵌面板 ID），
 *   因此新建/重建需要机器人在线；不在线时界面给出明确提示而不是静默失败；
 * - 编辑面板时仅在卡片文案或按钮文字变化后重建卡片；“开单后发送内容”保存即生效；
 * - 面板文案按 KMarkdown 渲染并支持多行，编辑时提供实时预览。
 */

import { useState } from "react"
import {
  ChevronDown,
  ChevronRight,
  LayoutPanelLeft,
  Pencil,
  Plus,
  RefreshCw,
  Trash2,
  UserPlus,
  X,
} from "lucide-react"
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
import { queryKeys, useKookChannels, useKookRoles, useTicketTypes } from "@/lib/queries"
import { toastError, toastSuccess } from "@/lib/toast"
import type { Panel, TicketType } from "@/lib/types"

/** 与后端 maxPanelTitleLength 保持一致。 */
const maxPanelTitleLength = 2000
/** 与后端 maxTicketTypeNameLength 保持一致。 */
const maxTypeNameLength = 64
/** 与后端 maxTicketTypeDescLength 保持一致。 */
const maxTypeDescLength = 256
/** 与后端 bot.ChannelNameMaxLength 保持一致：频道名中类型名的可见上限。 */
const channelTypeMaxLength = 12

export function TicketTypesPage() {
  const { t, i18n } = useTranslation()
  const queryClient = useQueryClient()
  const typesQuery = useTicketTypes()
  const runtimeQuery = useRuntimeInfo()
  const channelsQuery = useKookChannels()
  const rolesQuery = useKookRoles()

  const [createOpen, setCreateOpen] = useState(false)
  const [editingType, setEditingType] = useState<TicketType | null>(null)
  const [deletingType, setDeletingType] = useState<TicketType | null>(null)
  const [roleType, setRoleType] = useState<TicketType | null>(null)
  const [panelTarget, setPanelTarget] = useState<TicketType | null>(null)
  const [editingPanel, setEditingPanel] = useState<{ panel: Panel; type: TicketType } | null>(null)
  const [deletingPanel, setDeletingPanel] = useState<Panel | null>(null)
  const [expanded, setExpanded] = useState<number[]>([])

  const botConnected = runtimeQuery.data?.botConnected ?? false

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: queryKeys.types })
    void queryClient.invalidateQueries({ queryKey: queryKeys.kookChannels })
  }

  const toggleExpanded = (id: number) =>
    setExpanded((current) => (current.includes(id) ? current.filter((item) => item !== id) : [...current, id]))

  const createTypeMutation = useMutation({
    mutationFn: (payload: { name: string; description: string }) => api.post<TicketType>("/types", payload),
    onSuccess: (created) => {
      toastSuccess(t("types.createSuccess"))
      setCreateOpen(false)
      // 新建后自动展开，引导管理员继续添加面板。
      setExpanded((current) => (current.includes(created.id) ? current : [...current, created.id]))
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const editTypeMutation = useMutation({
    mutationFn: (payload: { id: number; name: string; description: string }) =>
      api.patch<TicketType>(`/types/${payload.id}`, { name: payload.name, description: payload.description }),
    onSuccess: () => {
      toastSuccess(t("types.editSuccess"))
      setEditingType(null)
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const toggleTypeMutation = useMutation({
    mutationFn: (payload: { id: number; enabled: boolean }) =>
      api.patch<TicketType>(`/types/${payload.id}`, { enabled: payload.enabled }),
    onSuccess: (_data, variables) => {
      toastSuccess(variables.enabled ? t("types.enableSuccess") : t("types.disableSuccess"))
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const deleteTypeMutation = useMutation({
    mutationFn: (id: number) => api.del(`/types/${id}`),
    onSuccess: () => {
      toastSuccess(t("types.deleteSuccess"))
      setDeletingType(null)
      invalidate()
    },
    onError: (error) => {
      toastError(error)
      setDeletingType(null)
    },
  })

  const addRoleMutation = useMutation({
    mutationFn: (payload: { typeId: number; roleId: string }) =>
      api.post<TicketType>(`/types/${payload.typeId}/roles`, { roleId: payload.roleId }),
    onSuccess: () => {
      toastSuccess(t("types.roleAddSuccess"))
      setRoleType(null)
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const removeRoleMutation = useMutation({
    mutationFn: (payload: { typeId: number; roleId: string }) =>
      api.del(`/types/${payload.typeId}/roles/${encodeURIComponent(payload.roleId)}`),
    onSuccess: () => {
      toastSuccess(t("types.roleRemoveSuccess"))
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const createPanelMutation = useMutation({
    mutationFn: (payload: {
      typeId: number
      channelId: string
      title: string
      buttonText: string
      openMessage: string
    }) => api.post<Panel>("/panels", payload),
    onSuccess: () => {
      toastSuccess(t("panels.createSuccess"))
      setPanelTarget(null)
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  // 编辑面板：先保存文案、按钮文字、所属类型与开单提示。
  // 只有卡片文案或按钮文字变化、且机器人在线时才重建卡片：
  //“开单后发送内容”与所属类型不参与卡片渲染，保存即生效。
  const editPanelMutation = useMutation({
    mutationFn: async (payload: {
      id: number
      typeId: number
      title: string
      buttonText: string
      openMessage: string
      cardChanged: boolean
      rebuild: boolean
    }) => {
      await api.patch<Panel>(`/panels/${payload.id}`, {
        typeId: payload.typeId,
        title: payload.title,
        buttonText: payload.buttonText,
        openMessage: payload.openMessage,
      })
      const rebuilt = payload.rebuild && payload.cardChanged
      if (rebuilt) {
        await api.post<Panel>(`/panels/${payload.id}/refresh`)
      }
      return { rebuilt, cardChanged: payload.cardChanged }
    },
    onSuccess: ({ rebuilt, cardChanged }) => {
      if (rebuilt) {
        toastSuccess(t("panels.editSuccess"))
      } else if (cardChanged) {
        toastSuccess(t("panels.editSaved"))
      } else {
        toastSuccess(t("panels.editSavedNoRebuild"))
      }
      setEditingPanel(null)
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const togglePanelMutation = useMutation({
    mutationFn: (payload: { id: number; enabled: boolean }) =>
      api.patch<Panel>(`/panels/${payload.id}`, { enabled: payload.enabled }),
    onSuccess: (_data, variables) => {
      toastSuccess(variables.enabled ? t("panels.enableSuccess") : t("panels.disableSuccess"))
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const refreshPanelMutation = useMutation({
    mutationFn: (id: number) => api.post<Panel>(`/panels/${id}/refresh`),
    onSuccess: () => {
      toastSuccess(t("panels.refreshSuccess"))
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const deletePanelMutation = useMutation({
    mutationFn: (id: number) => api.del(`/panels/${id}`),
    onSuccess: () => {
      toastSuccess(t("panels.deleteSuccess"))
      setDeletingPanel(null)
      invalidate()
    },
    onError: (error) => {
      toastError(error)
      setDeletingPanel(null)
    },
  })

  const types = typesQuery.data?.items ?? []
  const allTypes = types.map(({ id, name, enabled }) => ({ id, name, enabled }))

  return (
    <div className="space-y-4">
      <PageHeader title={t("types.title")} description={t("types.description")}>
        <Button size="sm" onClick={() => setCreateOpen(true)}>
          <Plus className="size-4" />
          {t("types.create")}
        </Button>
      </PageHeader>

      {!botConnected ? (
        <Alert>
          <AlertDescription>{t("panels.botOffline")}</AlertDescription>
        </Alert>
      ) : null}

      {typesQuery.isError ? (
        <ErrorState error={typesQuery.error} onRetry={() => void typesQuery.refetch()} />
      ) : typesQuery.isPending ? (
        <InlineLoader />
      ) : types.length === 0 ? (
        <Card>
          <CardContent className="space-y-2 py-8">
            <EmptyState title={t("types.empty")} icon={<LayoutPanelLeft className="size-6" />} />
            <p className="text-muted-foreground text-center text-sm">{t("types.emptyHint")}</p>
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-3">
          {types.map((item) => {
            const panels = item.panels ?? []
            const isExpanded = expanded.includes(item.id)
            return (
              <Card key={item.id} className="py-0">
                <CardContent className="px-4 py-4">
                  <div className="flex flex-wrap items-start justify-between gap-3">
                    <div className="flex min-w-0 flex-1 items-start gap-1">
                      <Button
                        variant="ghost"
                        size="icon"
                        className="mt-0.5 size-7 shrink-0"
                        aria-label={isExpanded ? t("types.collapse") : t("types.expand")}
                        onClick={() => toggleExpanded(item.id)}
                      >
                        {isExpanded ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
                      </Button>
                      <div className="min-w-0 space-y-1.5">
                        <div className="flex flex-wrap items-center gap-2">
                          <p className="text-sm font-medium">{item.name}</p>
                          <Badge variant="outline" className="font-normal">
                            {t("types.panelCount", { count: panels.length })}
                          </Badge>
                          {!item.enabled ? (
                            <Badge variant="outline" className="border-amber-500/40 font-normal text-amber-600">
                              {t("types.disabled")}
                            </Badge>
                          ) : null}
                        </div>
                        {item.description ? (
                          <p className="text-muted-foreground text-xs">{item.description}</p>
                        ) : null}
                        <div className="flex flex-wrap items-center gap-1">
                          <span className="text-muted-foreground text-xs">{t("types.rolesTitle")}</span>
                          {(item.roles ?? []).length === 0 ? (
                            <span className="text-muted-foreground text-xs">{t("common.none")}</span>
                          ) : (
                            item.roles?.map((role) => (
                              <Badge key={role.id} variant="outline" className="gap-1 font-normal">
                                {role.roleName || role.roleId}
                                <button
                                  type="button"
                                  aria-label={t("common.delete")}
                                  className="hover:text-destructive"
                                  onClick={() =>
                                    removeRoleMutation.mutate({ typeId: item.id, roleId: role.roleId })
                                  }
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
                            title={t("types.roleAdd")}
                            onClick={() => setRoleType(item)}
                          >
                            <UserPlus className="size-3.5" />
                          </Button>
                        </div>
                      </div>
                    </div>
                    <div className="flex items-center gap-1">
                      <Switch
                        checked={item.enabled}
                        disabled={toggleTypeMutation.isPending}
                        aria-label={t("types.enabledLabel")}
                        onCheckedChange={(checked) => toggleTypeMutation.mutate({ id: item.id, enabled: checked })}
                      />
                      <Button variant="ghost" size="icon" title={t("types.edit")} onClick={() => setEditingType(item)}>
                        <Pencil className="size-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        title={t("types.delete")}
                        onClick={() => setDeletingType(item)}
                      >
                        <Trash2 className="size-4" />
                      </Button>
                    </div>
                  </div>

                  {isExpanded ? (
                    <div className="mt-3 space-y-3 border-l pl-3">
                      {!item.enabled ? (
                        <p className="text-muted-foreground text-xs">{t("types.panelsDisabledHint")}</p>
                      ) : null}
                      {panels.length === 0 ? (
                        <p className="text-muted-foreground text-sm">{t("types.panelsEmpty")}</p>
                      ) : (
                        <Table>
                          <TableHeader>
                            <TableRow>
                              <TableHead>{t("panels.colChannel")}</TableHead>
                              <TableHead className="hidden md:table-cell">{t("panels.colTitle")}</TableHead>
                              <TableHead className="hidden md:table-cell">{t("types.colUpdated")}</TableHead>
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
                                  </div>
                                </TableCell>
                                <TableCell className="hidden max-w-56 md:table-cell">
                                  <div className="space-y-0.5">
                                    <p className="truncate text-sm">{panel.title || "—"}</p>
                                    {panel.openMessage ? (
                                      <p className="text-muted-foreground truncate text-xs">
                                        {t("panels.openMessageShort")}: {panel.openMessage}
                                      </p>
                                    ) : null}
                                  </div>
                                </TableCell>
                                <TableCell className="hidden md:table-cell">
                                  <span className="text-muted-foreground text-xs">
                                    {formatDateTime(panel.updatedAt, i18n.language)}
                                  </span>
                                </TableCell>
                                <TableCell>
                                  <Switch
                                    checked={panel.enabled}
                                    disabled={togglePanelMutation.isPending}
                                    aria-label={t("panels.enabledLabel")}
                                    onCheckedChange={(checked) =>
                                      togglePanelMutation.mutate({ id: panel.id, enabled: checked })
                                    }
                                  />
                                </TableCell>
                                <TableCell className="text-right">
                                  <div className="flex justify-end gap-1">
                                    <Button
                                      variant="ghost"
                                      size="icon"
                                      title={t("panels.edit")}
                                      onClick={() => setEditingPanel({ panel, type: item })}
                                    >
                                      <Pencil className="size-4" />
                                    </Button>
                                    <Button
                                      variant="ghost"
                                      size="icon"
                                      title={t("panels.refresh")}
                                      disabled={!botConnected || refreshPanelMutation.isPending}
                                      onClick={() => refreshPanelMutation.mutate(panel.id)}
                                    >
                                      <RefreshCw
                                        className={refreshPanelMutation.isPending ? "size-4 animate-spin" : "size-4"}
                                      />
                                    </Button>
                                    <Button
                                      variant="ghost"
                                      size="icon"
                                      title={t("common.delete")}
                                      onClick={() => setDeletingPanel(panel)}
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
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={!botConnected}
                        onClick={() => setPanelTarget(item)}
                      >
                        <Plus className="size-4" />
                        {t("types.addPanel")}
                      </Button>
                    </div>
                  ) : null}
                </CardContent>
              </Card>
            )
          })}
        </div>
      )}

      {/* 新建类型 */}
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden">
          <DialogHeader>
            <DialogTitle>{t("types.createTitle")}</DialogTitle>
            <DialogDescription>{t("types.createDesc")}</DialogDescription>
          </DialogHeader>
          <TypeForm
            pending={createTypeMutation.isPending}
            onSubmit={(name, description) => createTypeMutation.mutate({ name, description })}
          />
        </DialogContent>
      </Dialog>

      {/* 编辑类型 */}
      <Dialog open={Boolean(editingType)} onOpenChange={(open) => !open && setEditingType(null)}>
        <DialogContent className="grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden">
          <DialogHeader>
            <DialogTitle>{t("types.editTitle")}</DialogTitle>
            <DialogDescription>{t("types.editDesc")}</DialogDescription>
          </DialogHeader>
          {editingType ? (
            <TypeForm
              pending={editTypeMutation.isPending}
              initialName={editingType.name}
              initialDescription={editingType.description}
              onSubmit={(name, description) =>
                editTypeMutation.mutate({ id: editingType.id, name, description })
              }
            />
          ) : null}
        </DialogContent>
      </Dialog>

      {/* 新建面板 */}
      <Dialog open={Boolean(panelTarget)} onOpenChange={(open) => !open && setPanelTarget(null)}>
        <DialogContent className="grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle>{t("types.addPanelTitle")}</DialogTitle>
            <DialogDescription>
              {t("types.addPanelDesc", { name: panelTarget?.name ?? "" })}
            </DialogDescription>
          </DialogHeader>
          {panelTarget ? (
            <CreatePanelForm
              pending={createPanelMutation.isPending}
              channels={channelsQuery.data?.items ?? []}
              onSubmit={(payload) => createPanelMutation.mutate({ typeId: panelTarget.id, ...payload })}
            />
          ) : null}
        </DialogContent>
      </Dialog>

      {/* 编辑面板 */}
      <Dialog open={Boolean(editingPanel)} onOpenChange={(open) => !open && setEditingPanel(null)}>
        <DialogContent className="grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle>{t("panels.editTitle")}</DialogTitle>
            <DialogDescription>{t("panels.editDesc")}</DialogDescription>
          </DialogHeader>
          {editingPanel ? (
            <EditPanelForm
              pending={editPanelMutation.isPending}
              panel={editingPanel.panel}
              currentTypeID={editingPanel.type.id}
              types={allTypes}
              botConnected={botConnected}
              onSubmit={(payload) =>
                editPanelMutation.mutate({
                  id: editingPanel.panel.id,
                  typeId: payload.typeId,
                  title: payload.title,
                  buttonText: payload.buttonText,
                  openMessage: payload.openMessage,
                  cardChanged: payload.cardChanged,
                  rebuild: botConnected,
                })
              }
            />
          ) : null}
        </DialogContent>
      </Dialog>

      {/* 添加类型角色 */}
      <Dialog open={Boolean(roleType)} onOpenChange={(open) => !open && setRoleType(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("types.rolesTitle")}</DialogTitle>
            <DialogDescription>{t("types.rolesDesc")}</DialogDescription>
          </DialogHeader>
          {roleType ? (
            <RolePicker
              pending={addRoleMutation.isPending}
              roles={(rolesQuery.data?.items ?? []).filter(
                (role) => !(roleType.roles ?? []).some((existing) => existing.roleId === role.id),
              )}
              onSubmit={(roleId) => addRoleMutation.mutate({ typeId: roleType.id, roleId })}
            />
          ) : null}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={Boolean(deletingType)}
        onOpenChange={(open) => !open && setDeletingType(null)}
        title={t("types.deleteTitle")}
        description={t("types.deleteDesc")}
        confirmLabel={t("common.delete")}
        destructive
        pending={deleteTypeMutation.isPending}
        onConfirm={() => deletingType && deleteTypeMutation.mutate(deletingType.id)}
      />

      <ConfirmDialog
        open={Boolean(deletingPanel)}
        onOpenChange={(open) => !open && setDeletingPanel(null)}
        title={t("panels.deleteTitle")}
        description={t("panels.deleteDesc")}
        confirmLabel={t("common.delete")}
        destructive
        pending={deletePanelMutation.isPending}
        onConfirm={() => deletingPanel && deletePanelMutation.mutate(deletingPanel.id)}
      />
    </div>
  )
}

/** 类型表单：名称 + 备注，新建与编辑共用。 */
function TypeForm({
  pending,
  initialName = "",
  initialDescription = "",
  onSubmit,
}: {
  pending: boolean
  initialName?: string
  initialDescription?: string
  onSubmit: (name: string, description: string) => void
}) {
  const { t } = useTranslation()
  const [name, setName] = useState(initialName)
  const [description, setDescription] = useState(initialDescription)

  return (
    <>
      <div className="-mx-1 min-h-0 space-y-4 overflow-y-auto px-1">
        <div className="space-y-2">
          <Label htmlFor="type-name">{t("types.nameLabel")}</Label>
          <Input
            id="type-name"
            maxLength={maxTypeNameLength}
            value={name}
            placeholder={t("types.namePlaceholder")}
            onChange={(event) => setName(event.target.value)}
          />
          <p className="text-muted-foreground text-xs">
            {t("types.nameHint", { limit: channelTypeMaxLength })}
          </p>
        </div>
        <div className="space-y-2">
          <Label htmlFor="type-desc">{t("types.descLabel")}</Label>
          <Textarea
            id="type-desc"
            rows={3}
            maxLength={maxTypeDescLength}
            value={description}
            placeholder={t("types.descPlaceholder")}
            onChange={(event) => setDescription(event.target.value)}
          />
        </div>
      </div>
      <DialogFooter>
        <Button
          disabled={pending || name.trim().length === 0}
          onClick={() => onSubmit(name.trim(), description.trim())}
        >
          {pending ? t("common.saving") : t("common.save")}
        </Button>
      </DialogFooter>
    </>
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
  onSubmit: (payload: {
    channelId: string
    title: string
    buttonText: string
    openMessage: string
  }) => void
}) {
  const { t } = useTranslation()
  const playable = channels.filter((channel) => channel.kind !== "category")
  const [channelId, setChannelId] = useState(playable[0]?.id ?? "")
  const [title, setTitle] = useState("")
  const [buttonText, setButtonText] = useState("")
  const [openMessage, setOpenMessage] = useState("")

  return (
    <>
      <div className="-mx-1 min-h-0 space-y-4 overflow-y-auto px-1">
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
        <PanelTextEditor
          value={title}
          onChange={setTitle}
          idPrefix="panel-create"
          label={t("panels.titleLabel")}
          hint={t("panels.titleHint")}
          placeholder={"# 请点击右侧按钮发起工单\n\n**处理范围**\n- 账号问题\n- 充值问题"}
        />
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
        <PanelTextEditor
          value={openMessage}
          onChange={setOpenMessage}
          idPrefix="panel-create-open"
          rows={6}
          label={t("panels.openMessageLabel")}
          hint={t("panels.openMessageHint")}
          placeholder={t("panels.openMessagePlaceholder")}
        />
      </div>
      <DialogFooter>
        <Button
          disabled={pending || channelId.trim().length === 0}
          onClick={() =>
            onSubmit({
              channelId: channelId.trim(),
              title: title.trim(),
              buttonText: buttonText.trim(),
              openMessage: openMessage.trim(),
            })
          }
        >
          {pending ? t("common.saving") : t("common.create")}
        </Button>
      </DialogFooter>
    </>
  )
}

/** 编辑面板：可调整所属类型；卡片字段变化时才重建卡片，只改开单提示则保存即生效。 */
function EditPanelForm({
  pending,
  panel,
  currentTypeID,
  types,
  botConnected,
  onSubmit,
}: {
  pending: boolean
  panel: Panel
  currentTypeID: number
  types: { id: number; name: string; enabled: boolean }[]
  botConnected: boolean
  onSubmit: (payload: {
    typeId: number
    title: string
    buttonText: string
    openMessage: string
    cardChanged: boolean
  }) => void
}) {
  const { t } = useTranslation()
  const [typeID, setTypeID] = useState(String(currentTypeID))
  const [title, setTitle] = useState(panel.title)
  const [buttonText, setButtonText] = useState(panel.buttonText)
  const [openMessage, setOpenMessage] = useState(panel.openMessage)

  // 卡片正文与按钮文字决定频道内卡片的外观；“开单后发送内容”与所属类型只在开单时生效。
  const cardChanged = title.trim() !== panel.title.trim() || buttonText.trim() !== panel.buttonText.trim()
  const typeChanged = Number(typeID) !== currentTypeID
  const openMessageChanged = openMessage.trim() !== panel.openMessage.trim()
  const dirty = cardChanged || typeChanged || openMessageChanged

  return (
    <>
      <div className="-mx-1 min-h-0 space-y-4 overflow-y-auto px-1">
        <div className="space-y-2">
          <Label>{t("types.moveLabel")}</Label>
          <Select value={typeID} onValueChange={setTypeID}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {types.map((item) => (
                <SelectItem key={item.id} value={String(item.id)}>
                  {item.name}
                  {item.enabled ? "" : `（${t("types.disabled")}）`}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className="text-muted-foreground text-xs">{t("types.moveHint")}</p>
        </div>
        <PanelTextEditor
          value={title}
          onChange={setTitle}
          idPrefix="panel-edit"
          label={t("panels.titleLabel")}
          hint={t("panels.titleHint")}
          placeholder={"# 请点击右侧按钮发起工单\n\n**处理范围**\n- 账号问题\n- 充值问题"}
        />
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
        <PanelTextEditor
          value={openMessage}
          onChange={setOpenMessage}
          idPrefix="panel-edit-open"
          rows={6}
          label={t("panels.openMessageLabel")}
          hint={t("panels.openMessageHint")}
          placeholder={t("panels.openMessagePlaceholder")}
        />
      </div>
      <DialogFooter>
        <Button
          disabled={pending || !dirty || Number(typeID) === 0}
          onClick={() =>
            onSubmit({
              typeId: Number(typeID),
              title: title.trim(),
              buttonText: buttonText.trim(),
              openMessage: openMessage.trim(),
              cardChanged,
            })
          }
        >
          {pending ? t("common.saving") : cardChanged && botConnected ? t("panels.saveAndRefresh") : t("common.save")}
        </Button>
      </DialogFooter>
    </>
  )
}

/** 文案编辑器：左侧多行输入，右侧 KMarkdown 实时预览。 */
function PanelTextEditor({
  value,
  onChange,
  idPrefix,
  label,
  hint,
  placeholder,
  maxLength = maxPanelTitleLength,
  rows = 8,
}: {
  value: string
  onChange: (value: string) => void
  idPrefix: string
  label: string
  hint: string
  placeholder: string
  maxLength?: number
  rows?: number
}) {
  const { t } = useTranslation()

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <Label htmlFor={`${idPrefix}-title`}>{label}</Label>
        <span className="text-muted-foreground text-xs">
          {value.length}/{maxLength}
        </span>
      </div>
      <div className="grid gap-3 md:grid-cols-2">
        <Textarea
          id={`${idPrefix}-title`}
          rows={rows}
          maxLength={maxLength}
          value={value}
          className="max-h-64 min-h-40 font-mono text-sm"
          placeholder={placeholder}
          onChange={(event) => onChange(event.target.value)}
        />
        <div className="bg-muted/30 rounded-lg border p-3">
          <p className="text-muted-foreground mb-2 text-xs font-medium">{t("panels.preview")}</p>
          <div className="max-h-56 overflow-y-auto">
            <KMarkdownPreview source={value} emptyText={t("panels.previewEmpty")} />
          </div>
        </div>
      </div>
      <p className="text-muted-foreground text-xs">{hint}</p>
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
          {pending ? t("common.saving") : t("types.roleAdd")}
        </Button>
      </DialogFooter>
    </>
  )
}
