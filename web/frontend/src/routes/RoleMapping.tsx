/**
 * 角色与权限（仅管理员）。
 *
 * 上：全局管理员角色（对应原项目 admin_role，决定谁能处理工单）
 * 下：登录角色映射（KOOK 角色 → WebUI 权限，未命中者无法登录）
 */

import { useState } from "react"
import { Plus, ShieldCheck, Trash2, Workflow } from "lucide-react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"

import { RoleBadge } from "@/components/Badges"
import { PageHeader } from "@/components/PageHeader"
import { EmptyState, ErrorState, InlineLoader } from "@/components/StateViews"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api } from "@/lib/api"
import { queryKeys, useAdminRoles, useKookRoles, useRoleMappings } from "@/lib/queries"
import { toastError, toastSuccess } from "@/lib/toast"

const WEB_ROLES = ["admin", "staff", "readonly"] as const

export function RoleMappingPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const adminRolesQuery = useAdminRoles()
  const mappingsQuery = useRoleMappings()
  const kookRolesQuery = useKookRoles()

  const [adminDialogOpen, setAdminDialogOpen] = useState(false)
  const [mappingDialogOpen, setMappingDialogOpen] = useState(false)

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: queryKeys.adminRoles })
    void queryClient.invalidateQueries({ queryKey: queryKeys.roleMappings })
  }

  const addAdminRole = useMutation({
    mutationFn: (payload: { roleId: string; roleName: string }) => api.post("/roles/admin", payload),
    onSuccess: () => {
      toastSuccess(t("roles.addSuccess"))
      setAdminDialogOpen(false)
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const removeAdminRole = useMutation({
    mutationFn: (roleId: string) => api.del(`/roles/admin/${encodeURIComponent(roleId)}`),
    onSuccess: () => {
      toastSuccess(t("roles.deleteSuccess"))
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const upsertMapping = useMutation({
    mutationFn: (payload: { kookRoleId: string; kookRoleName: string; webRole: string }) =>
      api.put("/roles/mappings", payload),
    onSuccess: () => {
      toastSuccess(t("roles.addSuccess"))
      setMappingDialogOpen(false)
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const removeMapping = useMutation({
    mutationFn: (id: number) => api.del(`/roles/mappings/${id}`),
    onSuccess: () => {
      toastSuccess(t("roles.deleteSuccess"))
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const kookRoles = kookRolesQuery.data?.items ?? []

  return (
    <div className="space-y-5">
      <PageHeader title={t("roles.title")} description={t("roles.description")} />

      {kookRolesQuery.data && !kookRolesQuery.data.available ? (
        <Alert>
          <ShieldCheck className="size-4" />
          <AlertDescription>{kookRolesQuery.data.note}</AlertDescription>
        </Alert>
      ) : null}

      {/* 全局管理员角色 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("roles.adminTitle")}</CardTitle>
          <CardDescription>{t("roles.adminDesc")}</CardDescription>
          <CardAction>
            <Button size="sm" onClick={() => setAdminDialogOpen(true)}>
              <Plus className="size-4" />
              {t("roles.adminAdd")}
            </Button>
          </CardAction>
        </CardHeader>
        <CardContent className="px-0">
          {adminRolesQuery.isError ? (
            <div className="px-6">
              <ErrorState error={adminRolesQuery.error} onRetry={() => void adminRolesQuery.refetch()} />
            </div>
          ) : adminRolesQuery.isPending ? (
            <InlineLoader />
          ) : (adminRolesQuery.data?.items.length ?? 0) === 0 ? (
            <EmptyState title={t("roles.adminEmpty")} icon={<ShieldCheck className="size-6" />} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("roles.colRoleId")}</TableHead>
                  <TableHead>{t("roles.colRoleName")}</TableHead>
                  <TableHead className="text-right">{t("common.actions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {adminRolesQuery.data?.items.map((role) => (
                  <TableRow key={role.id}>
                    <TableCell className="font-mono text-xs">{role.roleId}</TableCell>
                    <TableCell>{role.roleName || "—"}</TableCell>
                    <TableCell className="text-right">
                      <Button
                        variant="ghost"
                        size="icon"
                        disabled={removeAdminRole.isPending}
                        onClick={() => removeAdminRole.mutate(role.roleId)}
                      >
                        <Trash2 className="size-4" />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* 登录角色映射 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("roles.mappingTitle")}</CardTitle>
          <CardDescription>{t("roles.mappingDesc")}</CardDescription>
          <CardAction>
            <Button size="sm" onClick={() => setMappingDialogOpen(true)}>
              <Plus className="size-4" />
              {t("roles.mappingAdd")}
            </Button>
          </CardAction>
        </CardHeader>
        <CardContent className="px-0">
          {mappingsQuery.isError ? (
            <div className="px-6">
              <ErrorState error={mappingsQuery.error} onRetry={() => void mappingsQuery.refetch()} />
            </div>
          ) : mappingsQuery.isPending ? (
            <InlineLoader />
          ) : (mappingsQuery.data?.items.length ?? 0) === 0 ? (
            <EmptyState title={t("roles.mappingEmpty")} icon={<Workflow className="size-6" />} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("roles.colKookRole")}</TableHead>
                  <TableHead>{t("roles.colWebRole")}</TableHead>
                  <TableHead className="text-right">{t("common.actions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {mappingsQuery.data?.items.map((mapping) => (
                  <TableRow key={mapping.id}>
                    <TableCell>
                      <div className="space-y-0.5">
                        <p className="text-sm">{mapping.kookRoleName || "—"}</p>
                        <p className="text-muted-foreground font-mono text-xs">{mapping.kookRoleId}</p>
                      </div>
                    </TableCell>
                    <TableCell>
                      <RoleBadge role={mapping.webRole} />
                    </TableCell>
                    <TableCell className="text-right">
                      <Button
                        variant="ghost"
                        size="icon"
                        disabled={removeMapping.isPending}
                        onClick={() => removeMapping.mutate(mapping.id)}
                      >
                        <Trash2 className="size-4" />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <AddAdminRoleDialog
        open={adminDialogOpen}
        onOpenChange={setAdminDialogOpen}
        pending={addAdminRole.isPending}
        kookRoles={kookRoles}
        onSubmit={(payload) => addAdminRole.mutate(payload)}
      />

      <AddMappingDialog
        open={mappingDialogOpen}
        onOpenChange={setMappingDialogOpen}
        pending={upsertMapping.isPending}
        kookRoles={kookRoles}
        onSubmit={(payload) => upsertMapping.mutate(payload)}
      />
    </div>
  )
}

function AddAdminRoleDialog({
  open,
  onOpenChange,
  pending,
  kookRoles,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  pending: boolean
  kookRoles: { id: string; name: string }[]
  onSubmit: (payload: { roleId: string; roleName: string }) => void
}) {
  const { t } = useTranslation()
  const [roleId, setRoleId] = useState("")
  const [roleName, setRoleName] = useState("")

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        onOpenChange(next)
        if (!next) {
          setRoleId("")
          setRoleName("")
        }
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("roles.adminAdd")}</DialogTitle>
          <DialogDescription>{t("roles.adminDesc")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          {kookRoles.length > 0 ? (
            <div className="space-y-2">
              <Label>{t("roles.colKookRole")}</Label>
              <Select
                value={roleId}
                onValueChange={(value) => {
                  setRoleId(value)
                  const matched = kookRoles.find((role) => role.id === value)
                  if (matched) setRoleName(matched.name)
                }}
              >
                <SelectTrigger>
                  <SelectValue placeholder={t("roles.mappingRoleIdPlaceholder")} />
                </SelectTrigger>
                <SelectContent>
                  {kookRoles.map((role) => (
                    <SelectItem key={role.id} value={role.id}>
                      {role.name} · {role.id}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          ) : null}
          <div className="space-y-2">
            <Label htmlFor="admin-role-id">{t("roles.colRoleId")}</Label>
            <Input
              id="admin-role-id"
              value={roleId}
              onChange={(event) => setRoleId(event.target.value)}
              placeholder={t("roles.mappingRoleIdPlaceholder")}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="admin-role-name">{t("roles.colRoleName")}</Label>
            <Input
              id="admin-role-name"
              value={roleName}
              onChange={(event) => setRoleName(event.target.value)}
              placeholder={t("roles.mappingNamePlaceholder")}
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
            {t("common.cancel")}
          </Button>
          <Button disabled={pending || !roleId} onClick={() => onSubmit({ roleId: roleId.trim(), roleName: roleName.trim() })}>
            {pending ? t("common.saving") : t("common.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function AddMappingDialog({
  open,
  onOpenChange,
  pending,
  kookRoles,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  pending: boolean
  kookRoles: { id: string; name: string }[]
  onSubmit: (payload: { kookRoleId: string; kookRoleName: string; webRole: string }) => void
}) {
  const { t } = useTranslation()
  const [kookRoleId, setKookRoleId] = useState("")
  const [kookRoleName, setKookRoleName] = useState("")
  const [webRole, setWebRole] = useState<string>("staff")

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        onOpenChange(next)
        if (!next) {
          setKookRoleId("")
          setKookRoleName("")
          setWebRole("staff")
        }
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("roles.mappingAdd")}</DialogTitle>
          <DialogDescription>{t("roles.mappingDesc")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          {kookRoles.length > 0 ? (
            <div className="space-y-2">
              <Label>{t("roles.colKookRole")}</Label>
              <Select
                value={kookRoleId}
                onValueChange={(value) => {
                  setKookRoleId(value)
                  const matched = kookRoles.find((role) => role.id === value)
                  if (matched) setKookRoleName(matched.name)
                }}
              >
                <SelectTrigger>
                  <SelectValue placeholder={t("roles.mappingRoleIdPlaceholder")} />
                </SelectTrigger>
                <SelectContent>
                  {kookRoles.map((role) => (
                    <SelectItem key={role.id} value={role.id}>
                      {role.name} · {role.id}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          ) : null}
          <div className="space-y-2">
            <Label htmlFor="mapping-role-id">{t("roles.colRoleId")}</Label>
            <Input
              id="mapping-role-id"
              value={kookRoleId}
              onChange={(event) => setKookRoleId(event.target.value)}
              placeholder={t("roles.mappingRoleIdPlaceholder")}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="mapping-role-name">{t("roles.colRoleName")}</Label>
            <Input
              id="mapping-role-name"
              value={kookRoleName}
              onChange={(event) => setKookRoleName(event.target.value)}
              placeholder={t("roles.mappingNamePlaceholder")}
            />
          </div>
          <div className="space-y-2">
            <Label>{t("roles.colWebRole")}</Label>
            <Select value={webRole} onValueChange={setWebRole}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {WEB_ROLES.map((role) => (
                  <SelectItem key={role} value={role}>
                    {t(`role.${role}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={pending || !kookRoleId}
            onClick={() => onSubmit({ kookRoleId: kookRoleId.trim(), kookRoleName: kookRoleName.trim(), webRole })}
          >
            {pending ? t("common.saving") : t("common.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
