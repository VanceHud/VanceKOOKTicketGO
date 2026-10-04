/**
 * 登录页。
 *
 * 三种方式：
 *  1. 账号密码（本期可用）；
 *  2. KOOK 一次性登录码（接口已就位，后续里程碑开放，调用后会返回明确提示）；
 *  3. 账号绑定（同样为后续里程碑开放）。
 */

import { useEffect, useState, type FormEvent } from "react"
import { AlertCircle, KeyRound, Link2, ShieldCheck, Ticket } from "lucide-react"
import { useTranslation } from "react-i18next"
import { useQueryClient } from "@tanstack/react-query"
import { useLocation, useNavigate } from "react-router"

import { LocaleToggle } from "@/components/LocaleToggle"
import { ThemeToggle } from "@/components/ThemeToggle"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { api, setCsrfToken } from "@/lib/api"
import { ME_QUERY_KEY, useAuth } from "@/lib/auth"
import type { MeResponse } from "@/lib/types"

type LoginTab = "password" | "code" | "bind"

export function LoginPage() {
  const { t } = useTranslation()
  const { isAuthenticated, login, loginPending, loginError, resetLoginError } = useAuth()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const location = useLocation()
  const from = (location.state as { from?: string } | null)?.from ?? "/"

  const [tab, setTab] = useState<LoginTab>("password")
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [code, setCode] = useState("")
  const [codePending, setCodePending] = useState(false)
  const [codeError, setCodeError] = useState<string | null>(null)

  useEffect(() => {
    if (isAuthenticated) {
      navigate(from, { replace: true })
    }
  }, [isAuthenticated, from, navigate])

  const submitPassword = async (event: FormEvent) => {
    event.preventDefault()
    resetLoginError()
    try {
      await login(username.trim(), password)
      navigate(from, { replace: true })
    } catch {
      // 错误详情通过 loginError 展示在表单上方
    }
  }

  const submitCode = async (event: FormEvent) => {
    event.preventDefault()
    setCodeError(null)
    setCodePending(true)
    try {
      const me = await api.post<MeResponse>("/auth/login-code", { code: code.trim() })
      // 与密码登录一致：写入 CSRF 令牌并刷新身份缓存
      setCsrfToken(me.csrfToken)
      queryClient.setQueryData(ME_QUERY_KEY, me)
      navigate(from, { replace: true })
    } catch (error) {
      setCodeError(error instanceof Error ? error.message : t("errors.unknown"))
    } finally {
      setCodePending(false)
    }
  }

  return (
    <div className="grid min-h-svh lg:grid-cols-[1.1fr_1fr]">
      {/* 左侧品牌与安全说明：小屏隐藏 */}
      <div className="bg-muted/40 relative hidden flex-col justify-between border-r p-10 lg:flex">
        <div className="flex items-center gap-3">
          <div className="bg-primary text-primary-foreground flex size-10 items-center justify-center rounded-xl">
            <Ticket className="size-5" />
          </div>
          <div>
            <p className="font-semibold">{t("app.name")}</p>
            <p className="text-muted-foreground text-xs">{t("app.tagline")}</p>
          </div>
        </div>

        <div className="space-y-6">
          <h2 className="text-2xl font-semibold tracking-tight">{t("login.subtitle")}</h2>
          <ul className="text-muted-foreground space-y-3 text-sm">
            <li className="flex gap-2">
              <ShieldCheck className="text-status-open mt-0.5 size-4 shrink-0" />
              {t("login.securityNotice")}
            </li>
            <li className="flex gap-2">
              <KeyRound className="mt-0.5 size-4 shrink-0" />
              {t("login.codeHint")}
            </li>
            <li className="flex gap-2">
              <Link2 className="mt-0.5 size-4 shrink-0" />
              {t("login.bindHint")}
            </li>
          </ul>
        </div>

        <p className="text-muted-foreground text-xs">{t("app.name")} · WebUI</p>
      </div>

      {/* 右侧登录表单 */}
      <div className="flex flex-col">
        <div className="flex items-center justify-end gap-1 p-4">
          <LocaleToggle />
          <ThemeToggle />
        </div>

        <div className="flex flex-1 items-center justify-center p-6">
          <Card className="w-full max-w-sm">
            <CardHeader className="space-y-1">
              <div className="mb-2 flex items-center gap-2 lg:hidden">
                <div className="bg-primary text-primary-foreground flex size-8 items-center justify-center rounded-lg">
                  <Ticket className="size-4" />
                </div>
                <span className="font-semibold">{t("app.name")}</span>
              </div>
              <CardTitle>{t("login.title")}</CardTitle>
              <CardDescription>{t("app.tagline")}</CardDescription>
            </CardHeader>

            <CardContent>
              <Tabs value={tab} onValueChange={(value) => setTab(value as LoginTab)}>
                <TabsList className="grid w-full grid-cols-3">
                  <TabsTrigger value="password">{t("login.tabPassword")}</TabsTrigger>
                  <TabsTrigger value="code">{t("login.tabCode")}</TabsTrigger>
                  <TabsTrigger value="bind">{t("login.tabBind")}</TabsTrigger>
                </TabsList>

                <TabsContent value="password" className="pt-4">
                  <form className="space-y-4" onSubmit={submitPassword}>
                    {loginError ? (
                      <Alert variant="destructive">
                        <AlertCircle className="size-4" />
                        <AlertDescription>{loginError.message}</AlertDescription>
                      </Alert>
                    ) : null}

                    <div className="space-y-2">
                      <Label htmlFor="username">{t("login.username")}</Label>
                      <Input
                        id="username"
                        name="username"
                        autoComplete="username"
                        autoFocus
                        required
                        value={username}
                        onChange={(event) => setUsername(event.target.value)}
                      />
                    </div>

                    <div className="space-y-2">
                      <Label htmlFor="password">{t("login.password")}</Label>
                      <Input
                        id="password"
                        name="password"
                        type="password"
                        autoComplete="current-password"
                        required
                        value={password}
                        onChange={(event) => setPassword(event.target.value)}
                      />
                    </div>

                    <Button type="submit" className="w-full" disabled={loginPending}>
                      {loginPending ? t("login.submitting") : t("login.submit")}
                    </Button>
                  </form>
                </TabsContent>

                <TabsContent value="code" className="space-y-4 pt-4">
                  <Alert>
                    <KeyRound className="size-4" />
                    <AlertDescription>{t("login.codeHint")}</AlertDescription>
                  </Alert>

                  <form className="space-y-4" onSubmit={submitCode}>
                    {codeError ? (
                      <Alert variant="destructive">
                        <AlertCircle className="size-4" />
                        <AlertDescription>{codeError}</AlertDescription>
                      </Alert>
                    ) : null}
                    <div className="space-y-2">
                      <Label htmlFor="login-code">{t("login.tabCode")}</Label>
                      <Input
                        id="login-code"
                        inputMode="numeric"
                        autoComplete="one-time-code"
                        placeholder={t("login.codePlaceholder")}
                        value={code}
                        onChange={(event) => setCode(event.target.value)}
                      />
                    </div>
                    <Button type="submit" className="w-full" variant="secondary" disabled={codePending || code.length === 0}>
                      {t("login.codeSubmit")}
                    </Button>
                  </form>
                </TabsContent>

                <TabsContent value="bind" className="space-y-4 pt-4">
                  <Alert>
                    <Link2 className="size-4" />
                    <AlertDescription>{t("login.bindHint")}</AlertDescription>
                  </Alert>
                </TabsContent>
              </Tabs>
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  )
}
