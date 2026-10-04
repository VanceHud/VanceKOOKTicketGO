/** 修改密码页：首次使用初始密码登录时会被强制跳转到此。 */

import { useState, type FormEvent } from "react"
import { AlertCircle, ShieldCheck, Ticket } from "lucide-react"
import { useQueryClient } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"
import { useNavigate } from "react-router"

import { LocaleToggle } from "@/components/LocaleToggle"
import { ThemeToggle } from "@/components/ThemeToggle"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { api, setCsrfToken } from "@/lib/api"
import { ME_QUERY_KEY, useAuth } from "@/lib/auth"
import type { MeResponse } from "@/lib/types"
import { toastError, toastSuccess } from "@/lib/toast"

export function ChangePasswordPage() {
  const { t } = useTranslation()
  const { me } = useAuth()
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const [currentPassword, setCurrentPassword] = useState("")
  const [newPassword, setNewPassword] = useState("")
  const [confirmPassword, setConfirmPassword] = useState("")
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const mustChange = Boolean(me?.user.mustChangePassword)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setError(null)

    if (newPassword !== confirmPassword) {
      setError(t("changePassword.mismatch"))
      return
    }

    setPending(true)
    try {
      const updated = await api.post<MeResponse>("/auth/password", { currentPassword, newPassword })
      // 服务端已吊销其它会话并签发新会话，这里同步新的 CSRF 令牌与用户信息。
      setCsrfToken(updated.csrfToken)
      queryClient.setQueryData(ME_QUERY_KEY, updated)
      toastSuccess(t("changePassword.success"))
      navigate("/", { replace: true })
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : t("errors.unknown"))
      toastError(caught)
    } finally {
      setPending(false)
    }
  }

  return (
    <div className="bg-muted/30 flex min-h-svh flex-col">
      <div className="flex items-center justify-between p-4">
        <div className="flex items-center gap-2">
          <div className="bg-primary text-primary-foreground flex size-8 items-center justify-center rounded-lg">
            <Ticket className="size-4" />
          </div>
          <span className="font-semibold">{t("app.name")}</span>
        </div>
        <div className="flex items-center gap-1">
          <LocaleToggle />
          <ThemeToggle />
        </div>
      </div>

      <div className="flex flex-1 items-center justify-center p-6">
        <Card className="w-full max-w-md">
          <CardHeader>
            <CardTitle>{t("changePassword.title")}</CardTitle>
            <CardDescription>{t("changePassword.subtitle")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            {mustChange ? (
              <Alert>
                <ShieldCheck className="size-4" />
                <AlertDescription>{t("changePassword.forced")}</AlertDescription>
              </Alert>
            ) : null}

            {error ? (
              <Alert variant="destructive">
                <AlertCircle className="size-4" />
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            ) : null}

            <form className="space-y-4" onSubmit={submit}>
              <div className="space-y-2">
                <Label htmlFor="current">{t("changePassword.current")}</Label>
                <Input
                  id="current"
                  type="password"
                  autoComplete="current-password"
                  required
                  value={currentPassword}
                  onChange={(event) => setCurrentPassword(event.target.value)}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="new">{t("changePassword.new")}</Label>
                <Input
                  id="new"
                  type="password"
                  autoComplete="new-password"
                  required
                  value={newPassword}
                  onChange={(event) => setNewPassword(event.target.value)}
                />
                <p className="text-muted-foreground text-xs">{t("changePassword.hint")}</p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="confirm">{t("changePassword.confirm")}</Label>
                <Input
                  id="confirm"
                  type="password"
                  autoComplete="new-password"
                  required
                  value={confirmPassword}
                  onChange={(event) => setConfirmPassword(event.target.value)}
                />
              </div>
              <Button type="submit" className="w-full" disabled={pending}>
                {pending ? t("common.saving") : t("changePassword.submit")}
              </Button>
            </form>
          </CardContent>
        </Card>
      </div>
    </div>
  )
}
