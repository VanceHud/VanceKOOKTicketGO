/**
 * 账号管理（仅管理员）。
 *
 * 服务端已实现完整保护：不能修改/禁用自己的账号、必须保留至少一个可用管理员，
 * 角色变更 / 禁用 / 重置密码都会立刻吊销该账号的全部会话。
 */

import { useState } from "react"
import { KeyRound, Pencil, Plus, Trash2, Users } from "lucide-react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"

import { RoleBadge } from "@/components/Badges"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { PageHeader } from "@/components/PageHeader"
import { EmptyState, ErrorState, InlineLoader } from "@/components/StateViews"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
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
import { useAuth } from "@/lib/auth"
import { formatDateTime, formatRelative } from "@/lib/format"
import { queryKeys, useUsers } from "@/lib/queries"
import { toastError, toastSuccess } from "@/lib/toast"
import type { WebUserAccount } from "@/lib/types"

const ROLES = ["admin", "staff", "readonly"] as const

export function UsersPage() {
  const { t, i18n } = useTranslation()
  const queryClient = useQueryClient()
  const { me } = useAuth()
  const usersQuery = useUsers()

  const [createOpen, setCreateOpen] = useState(false)
  const [editing, setEditing] = useState<WebUserAccount | null>(null)
  const [resetting, setResetting] = useState<WebUserAccount | null>(null)
  const [deleting, setDeleting] = useState<WebUserAccount | null>(null)

  const invalidate = () => void queryClient.invalidateQueries({ queryKey: queryKeys.users })

  const createMutation = useMutation({
    mutationFn: (payload: { username: string; displayName: string; role: string; password: string }) =>
      api.post("/users", payload),
    onSuccess: () => {
      toastSuccess(t("users.createSuccess"))
      setCreateOpen(false)
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const updateMutation = useMutation({
    mutationFn: (payload: { id: number; body: Record<string, unknown> }) =>
      api.patch(`/users/${payload.id}`, payload.body),
    onSuccess: () => {
      toastSuccess(t("users.updateSuccess"))
      setEditing(null)
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const resetMutation = useMutation({
    mutationFn: (payload: { id: number; password: string }) =>
      api.patch(`/users/${payload.id}`, { password: payload.password }),
    onSuccess: () => {
      toastSuccess(t("users.resetSuccess"))
      setResetting(null)
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => api.del(`/users/${id}`),
    onSuccess: () => {
      toastSuccess(t("users.deleteSuccess"))
      setDeleting(null)
      invalidate()
    },
    onError: (error) => {
      toastError(error)
      setDeleting(null)
    },
  })

  const users = usersQuery.data?.items ?? []

  return (
    <div className="space-y-4">
      <PageHeader title={t("users.title")} description={t("users.description")}>
        <Button size="sm" onClick={() => setCreateOpen(true)}>
          <Plus className="size-4" />
          {t("users.create")}
        </Button>
      </PageHeader>

      {usersQuery.isError ? (
        <ErrorState error={usersQuery.error} onRetry={() => void usersQuery.refetch()} />
      ) : usersQuery.isPending ? (
        <InlineLoader />
      ) : users.length === 0 ? (
        <Card>
          <CardContent>
            <EmptyState icon={<Users className="size-6" />} />
          </CardContent>
        </Card>
      ) : (
        <Card className="py-0">
          <CardContent className="px-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("users.colUsername")}</TableHead>
                  <TableHead>{t("users.colRole")}</TableHead>
                  <TableHead className="hidden md:table-cell">{t("users.colBound")}</TableHead>
                  <TableHead className="hidden lg:table-cell">{t("users.colLastLogin")}</TableHead>
                  <TableHead>{t("users.colState")}</TableHead>
                  <TableHead className="text-right">{t("common.actions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {users.map((user) => {
                  const isSelf = user.id === me?.user.id
                  return (
                    <TableRow key={user.id}>
                      <TableCell>
                        <div className="space-y-0.5">
                          <p className="text-sm font-medium">{user.displayName || user.username}</p>
                          <p className="text-muted-foreground font-mono text-xs">@{user.username}</p>
                        </div>
                      </TableCell>
                      <TableCell>
                        <RoleBadge role={user.role} />
                      </TableCell>
                      <TableCell className="hidden md:table-cell">
                        {user.kookUserId ? (
                          <div className="space-y-0.5">
                            <Badge variant="secondary" className="font-normal">
                              {t("users.bound")}
                            </Badge>
                            <p className="text-muted-foreground font-mono text-xs">{user.kookUserId}</p>
                          </div>
                        ) : (
                          <span className="text-muted-foreground text-xs">{t("users.notBound")}</span>
                        )}
                      </TableCell>
                      <TableCell className="text-muted-foreground hidden text-xs lg:table-cell">
                        {user.lastLoginAt ? formatRelative(user.lastLoginAt, i18n.language) : t("common.none")}
                      </TableCell>
                      <TableCell>
                        <div className="flex flex-wrap gap-1">
                          <Badge variant={user.disabled ? "outline" : "secondary"} className="font-normal">
                            {user.disabled ? t("users.stateDisabled") : t("users.stateActive")}
                          </Badge>
                          {user.mustChangePassword ? (
                            <Badge variant="outline" className="border-status-locked/40 text-status-locked font-normal">
                              {t("users.mustChange")}
                            </Badge>
                          ) : null}
                        </div>
                      </TableCell>
                      <TableCell className="text-right">
                        <div className="flex justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            disabled={isSelf}
                            title={isSelf ? t("users.selfEditBlocked") : t("common.edit")}
                            onClick={() => setEditing(user)}
                          >
                            <Pencil className="size-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            title={t("users.resetTitle")}
                            onClick={() => setResetting(user)}
                          >
                            <KeyRound className="size-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            disabled={isSelf}
                            title={isSelf ? t("users.selfEditBlocked") : t("common.delete")}
                            onClick={() => setDeleting(user)}
                          >
                            <Trash2 className="size-4" />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      <CreateUserDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        pending={createMutation.isPending}
        onSubmit={(payload) => createMutation.mutate(payload)}
      />

      <EditUserDialog
        user={editing}
        onClose={() => setEditing(null)}
        pending={updateMutation.isPending}
        onSubmit={(id, body) => updateMutation.mutate({ id, body })}
      />

      <ResetPasswordDialog
        user={resetting}
        onClose={() => setResetting(null)}
        pending={resetMutation.isPending}
        onSubmit={(id, password) => resetMutation.mutate({ id, password })}
      />

      <ConfirmDialog
        open={Boolean(deleting)}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={t("users.deleteTitle")}
        description={t("users.deleteDesc")}
        confirmLabel={t("common.delete")}
        destructive
        pending={deleteMutation.isPending}
        onConfirm={() => deleting && deleteMutation.mutate(deleting.id)}
      />
    </div>
  )
}

function CreateUserDialog({
  open,
  onOpenChange,
  pending,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  pending: boolean
  onSubmit: (payload: { username: string; displayName: string; role: string; password: string }) => void
}) {
  const { t } = useTranslation()
  const [username, setUsername] = useState("")
  const [displayName, setDisplayName] = useState("")
  const [role, setRole] = useState<string>("staff")
  const [password, setPassword] = useState("")

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        onOpenChange(next)
        if (!next) {
          setUsername("")
          setDisplayName("")
          setPassword("")
          setRole("staff")
        }
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("users.createTitle")}</DialogTitle>
          <DialogDescription>{t("users.description")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="new-username">{t("users.createUsername")}</Label>
            <Input
              id="new-username"
              value={username}
              onChange={(event) => setUsername(event.target.value)}
              placeholder="helper01"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="new-display">{t("users.createDisplayName")}</Label>
            <Input
              id="new-display"
              value={displayName}
              onChange={(event) => setDisplayName(event.target.value)}
              placeholder={t("users.createDisplayName")}
            />
          </div>
          <div className="space-y-2">
            <Label>{t("users.createRole")}</Label>
            <Select value={role} onValueChange={setRole}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ROLES.map((value) => (
                  <SelectItem key={value} value={value}>
                    {t(`role.${value}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-2">
            <Label htmlFor="new-password">{t("users.createPassword")}</Label>
            <Input
              id="new-password"
              type="password"
              autoComplete="new-password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
            />
            <p className="text-muted-foreground text-xs">{t("changePassword.hint")}</p>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={pending || !username || !password}
            onClick={() => onSubmit({ username: username.trim(), displayName: displayName.trim(), role, password })}
          >
            {pending ? t("common.saving") : t("common.create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function EditUserDialog({
  user,
  onClose,
  pending,
  onSubmit,
}: {
  user: WebUserAccount | null
  onClose: () => void
  pending: boolean
  onSubmit: (id: number, body: Record<string, unknown>) => void
}) {
  const { t } = useTranslation()
  const [displayName, setDisplayName] = useState(user?.displayName ?? "")
  const [role, setRole] = useState<string>(user?.role ?? "staff")
  const [disabled, setDisabled] = useState(user?.disabled ?? false)

  return (
    <Dialog
      open={Boolean(user)}
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent
        onOpenAutoFocus={() => {
          // 每次打开时用最新值初始化表单
          setDisplayName(user?.displayName ?? "")
          setRole(user?.role ?? "staff")
          setDisabled(user?.disabled ?? false)
        }}
      >
        <DialogHeader>
          <DialogTitle>{t("users.editTitle")}</DialogTitle>
          <DialogDescription>@{user?.username}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="edit-display">{t("users.editDisplayName")}</Label>
            <Input id="edit-display" value={displayName} onChange={(event) => setDisplayName(event.target.value)} />
          </div>
          <div className="space-y-2">
            <Label>{t("users.editRole")}</Label>
            <Select value={role} onValueChange={setRole}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ROLES.map((value) => (
                  <SelectItem key={value} value={value}>
                    {t(`role.${value}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex items-center justify-between rounded-lg border p-3">
            <div className="space-y-0.5">
              <Label htmlFor="edit-disabled">{t("users.editDisabled")}</Label>
              <p className="text-muted-foreground text-xs">{t("users.editDisabledHint")}</p>
            </div>
            <Switch id="edit-disabled" checked={disabled} onCheckedChange={setDisabled} />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={pending}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={pending}
            onClick={() => user && onSubmit(user.id, { displayName: displayName.trim(), role, disabled })}
          >
            {pending ? t("common.saving") : t("common.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ResetPasswordDialog({
  user,
  onClose,
  pending,
  onSubmit,
}: {
  user: WebUserAccount | null
  onClose: () => void
  pending: boolean
  onSubmit: (id: number, password: string) => void
}) {
  const { t } = useTranslation()
  const [password, setPassword] = useState("")

  return (
    <Dialog
      open={Boolean(user)}
      onOpenChange={(open) => {
        if (!open) {
          setPassword("")
          onClose()
        }
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("users.resetTitle")}</DialogTitle>
          <DialogDescription>
            @{user?.username} · {t("users.resetDesc")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-2">
          <Label htmlFor="reset-password">{t("users.resetPassword")}</Label>
          <Input
            id="reset-password"
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
          <p className="text-muted-foreground text-xs">{t("changePassword.hint")}</p>
          <p className="text-muted-foreground text-xs">
            {user?.createdAt ? formatDateTime(user.createdAt) : null}
          </p>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={pending}>
            {t("common.cancel")}
          </Button>
          <Button disabled={pending || password.length === 0} onClick={() => user && onSubmit(user.id, password)}>
            {pending ? t("common.saving") : t("common.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
