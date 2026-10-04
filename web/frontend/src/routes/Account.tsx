/**
 * 我的账号：账号信息、KOOK 身份绑定与改密入口。
 *
 * 绑定流程（与机器人侧配合）：
 *   用户在 KOOK 私聊机器人发送 /bind → 机器人签发一次性绑定码 →
 *   用户在本页输入绑定码 → 服务端把 KOOK 身份关联到当前账号。
 */

import { useState, type FormEvent } from "react"
import { KeyRound, Link2, ShieldCheck, UserCircle2 } from "lucide-react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"
import { Link } from "react-router"

import { RoleBadge } from "@/components/Badges"
import { PageHeader } from "@/components/PageHeader"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Separator } from "@/components/ui/separator"
import { api } from "@/lib/api"
import { ME_QUERY_KEY, useAuth } from "@/lib/auth"
import { formatDateTime, formatRelative } from "@/lib/format"
import { toastError, toastSuccess } from "@/lib/toast"
import type { MeUser } from "@/lib/types"

export function AccountPage() {
  const { t, i18n } = useTranslation()
  const { me } = useAuth()
  const queryClient = useQueryClient()
  const [code, setCode] = useState("")

  const bindMutation = useMutation({
    mutationFn: (bindCode: string) => api.post<{ user: MeUser }>("/auth/bind-code", { code: bindCode }),
    onSuccess: () => {
      toastSuccess(t("account.bindSuccess"))
      setCode("")
      void queryClient.invalidateQueries({ queryKey: ME_QUERY_KEY })
    },
    onError: (error) => toastError(error),
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (code.trim().length === 0) return
    bindMutation.mutate(code.trim())
  }

  if (!me) return null

  const kookUserId = me.user.kookUserName || me.user.username

  return (
    <div className="space-y-5">
      <PageHeader title={t("account.title")} description={t("account.description")} />

      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <UserCircle2 className="size-4" />
              {t("account.profileTitle")}
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-2 text-sm">
            <Row label={t("account.username")} value={<span className="font-mono text-xs">@{me.user.username}</span>} />
            <Row label={t("account.displayName")} value={me.user.displayName || "—"} />
            <Row label={t("account.role")} value={<RoleBadge role={me.user.role} />} />
            <Row
              label={t("account.kookBinding")}
              value={
                me.user.kookUserName ? (
                  <span className="text-status-open inline-flex items-center gap-1.5 text-xs">
                    <Link2 className="size-3.5" />
                    {t("account.bound")} · {me.user.kookUserName}
                  </span>
                ) : (
                  <span className="text-muted-foreground text-xs">{t("account.notBound")}</span>
                )
              }
            />
            <Separator />
            <Row
              label={t("users.colLastLogin")}
              value={me.user.lastLoginAt ? formatRelative(me.user.lastLoginAt, i18n.language) : "—"}
            />
            <Row label={t("users.colCreatedAt")} value={formatDateTime(me.user.createdAt, i18n.language)} />
            <p className="text-muted-foreground pt-1 text-xs">
              {t("bot.version")}: {me.server.version}
            </p>
          </CardContent>
        </Card>

        <div className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <Link2 className="size-4" />
                {t("account.bindTitle")}
              </CardTitle>
              <CardDescription>{t("account.bindDesc")}</CardDescription>
            </CardHeader>
            <CardContent className="space-y-3">
              {me.user.kookUserName ? (
                <Alert>
                  <ShieldCheck className="size-4" />
                  <AlertDescription>
                    {t("account.bound")}：{kookUserId}
                  </AlertDescription>
                </Alert>
              ) : null}
              <form className="space-y-3" onSubmit={submit}>
                <div className="space-y-2">
                  <Label htmlFor="bind-code">{t("account.bindPlaceholder")}</Label>
                  <Input
                    id="bind-code"
                    value={code}
                    maxLength={8}
                    autoComplete="one-time-code"
                    className="font-mono tracking-widest uppercase"
                    placeholder="ABC123"
                    onChange={(event) => setCode(event.target.value)}
                  />
                </div>
                <Button type="submit" disabled={bindMutation.isPending || code.trim().length === 0}>
                  {bindMutation.isPending ? t("common.saving") : t("account.bindSubmit")}
                </Button>
              </form>
              <p className="text-muted-foreground text-xs">{t("account.bindHint")}</p>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <KeyRound className="size-4" />
                {t("account.passwordTitle")}
              </CardTitle>
              <CardDescription>{t("account.passwordDesc")}</CardDescription>
            </CardHeader>
            <CardContent>
              <Button asChild variant="outline">
                <Link to="/change-password">{t("account.gotoPassword")}</Link>
              </Button>
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  )
}

function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <span className="text-muted-foreground shrink-0 text-xs">{label}</span>
      <span className="min-w-0 text-right">{value}</span>
    </div>
  )
}
