/** 机器人状态：连接情况、运行信息、业务配置与可选频道/角色。 */

import { Activity, AlertTriangle, Bot, CheckCircle2, Database, Hash, RefreshCw, ShieldCheck } from "lucide-react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"

import { PageHeader } from "@/components/PageHeader"
import { useAuth } from "@/lib/auth"
import { api } from "@/lib/api"
import { toastError, toastSuccess } from "@/lib/toast"
import { EmptyState, ErrorState, InlineLoader } from "@/components/StateViews"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Separator } from "@/components/ui/separator"
import { formatBytes, formatDateTime, formatDuration } from "@/lib/format"
import { useKookChannels, useKookRoles, useRuntimeInfo } from "@/lib/queries"

export function BotStatusPage() {
  const { t, i18n } = useTranslation()
  const { me } = useAuth()
  const queryClient = useQueryClient()
  const runtimeQuery = useRuntimeInfo()
  const rolesQuery = useKookRoles()
  const channelsQuery = useKookChannels()

  const runtime = runtimeQuery.data
  const bot = runtime?.bot
  const isAdmin = me?.user.role === "admin"

  const restartMutation = useMutation({
    mutationFn: () => api.post<{ ok: boolean }>("/bot/restart"),
    onSuccess: () => {
      toastSuccess(t("bot.restartSuccess"))
      void queryClient.invalidateQueries({ queryKey: ["runtime"] })
      void queryClient.invalidateQueries({ queryKey: ["kook-roles"] })
      void queryClient.invalidateQueries({ queryKey: ["kook-channels"] })
    },
    onError: (error) => toastError(error),
  })

  return (
    <div className="space-y-5">
      <PageHeader title={t("bot.title")} description={t("bot.description")} />

      {runtimeQuery.isError ? (
        <ErrorState error={runtimeQuery.error} onRetry={() => void runtimeQuery.refetch()} />
      ) : runtimeQuery.isPending ? (
        <InlineLoader />
      ) : runtime ? (
        <>
          {runtime.dryRun ? (
            <Alert>
              <AlertTriangle className="size-4" />
              <AlertTitle>{t("bot.dryRun")}</AlertTitle>
              <AlertDescription>{t("bot.dryRunHint")}</AlertDescription>
            </Alert>
          ) : null}

          {runtime.configuration.missingRequired.length > 0 ? (
            <Alert variant="destructive">
              <AlertTriangle className="size-4" />
              <AlertTitle>{t("bot.missingTitle")}</AlertTitle>
              <AlertDescription>
                <p>{t("bot.missingDesc")}</p>
                <ul className="mt-2 list-inside list-disc text-xs">
                  {runtime.configuration.missingRequired.map((key) => (
                    <li key={key}>{t(`bot.missingItems.${key}`, { defaultValue: key })}</li>
                  ))}
                </ul>
              </AlertDescription>
            </Alert>
          ) : (
            <Alert>
              <CheckCircle2 className="size-4" />
              <AlertDescription>{t("settings.missingNone")}</AlertDescription>
            </Alert>
          )}

          <div className="grid gap-4 lg:grid-cols-3">
            <Card>
              <CardHeader>
                <CardTitle className="flex items-center gap-2 text-base">
                  <Bot className="size-4" />
                  {t("bot.connection")}
                </CardTitle>
              </CardHeader>
              <CardContent className="space-y-2 text-sm">
                <Row
                  label={t("bot.connection")}
                  value={
                    <Badge variant="outline" className={runtime.botConnected ? "border-status-open/40 text-status-open" : "text-muted-foreground"}>
                      {runtime.botConnected ? t("bot.connected") : t("bot.disconnected")}
                    </Badge>
                  }
                />
                <Row label={t("bot.version")} value={<span className="font-mono text-xs">{runtime.version}</span>} />
                <Row label={t("bot.goVersion")} value={<span className="font-mono text-xs">{runtime.goVersion}</span>} />
                <Row label={t("bot.uptime")} value={formatDuration(runtime.uptimeSeconds)} />
                <Row label={t("bot.timezone")} value={runtime.ticketTimezone} />
              </CardContent>
            </Card>

            <Card>
              <CardHeader className="flex-row items-start justify-between">
                <div className="space-y-1">
                  <CardTitle className="flex items-center gap-2 text-base">
                    <Bot className="size-4" />
                    {t("bot.liveTitle")}
                  </CardTitle>
                  <CardDescription>
                    {bot?.botName
                      ? `${bot.botName} · ${bot.guildName || bot.guildId || "—"}`
                      : runtime.bot
                        ? t("bot.notRunning")
                        : t("bot.noBot")}
                  </CardDescription>
                </div>
                {isAdmin ? (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={restartMutation.isPending}
                    onClick={() => restartMutation.mutate()}
                  >
                    <RefreshCw className={restartMutation.isPending ? "size-4 animate-spin" : "size-4"} />
                    {restartMutation.isPending ? t("bot.restarting") : t("bot.restart")}
                  </Button>
                ) : null}
              </CardHeader>
              <CardContent className="space-y-2 text-sm">
                <Row
                  label={t("bot.connection")}
                  value={
                    <Badge
                      variant="outline"
                      className={bot?.connected ? "border-status-open/40 text-status-open" : "text-muted-foreground"}
                    >
                      {bot?.running
                        ? bot.connected
                          ? t("bot.connected")
                          : t("bot.notRunning")
                        : t("bot.disconnected")}
                    </Badge>
                  }
                />
                <Row label={t("bot.eventsHandled")} value={String(bot?.eventsHandled ?? 0)} />
                {bot?.sessionId ? (
                  <Row label={t("bot.sessionId")} value={<span className="font-mono text-xs">{bot.sessionId}</span>} />
                ) : null}
                {bot?.connectedAt ? (
                  <Row label={t("bot.startingAt")} value={formatDateTime(bot.connectedAt, i18n.language)} />
                ) : null}
                {bot?.lastEventAt ? (
                  <Row label={t("bot.lastEventAt")} value={formatDateTime(bot.lastEventAt, i18n.language)} />
                ) : null}
                {bot?.lastError ? (
                  <Alert variant="destructive">
                    <AlertTriangle className="size-4" />
                    <AlertDescription className="break-words">{bot.lastError}</AlertDescription>
                  </Alert>
                ) : null}
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle className="flex items-center gap-2 text-base">
                  <Activity className="size-4" />
                  {t("bot.sse")}
                </CardTitle>
                <CardDescription>{t("dashboard.description")}</CardDescription>
              </CardHeader>
              <CardContent className="space-y-2 text-sm">
                <Row label={t("bot.sseSubscribers")} value={String(runtime.sseSubscribers)} />
                <Separator />
                <Row label={t("bot.sessionIdle")} value={formatDuration(runtime.sessionIdleSecs)} />
                <Row label={t("bot.sessionMax")} value={formatDuration(runtime.sessionMaxSecs)} />
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle className="flex items-center gap-2 text-base">
                  <Database className="size-4" />
                  {t("bot.database")}
                </CardTitle>
              </CardHeader>
              <CardContent className="space-y-2 text-sm">
                <Row
                  label={t("bot.dbPath")}
                  value={<span className="font-mono text-xs break-all">{runtime.database.path}</span>}
                />
                <Row label={t("bot.dbSize")} value={formatBytes(runtime.database.sizeBytes)} />
              </CardContent>
            </Card>
          </div>

          <Card>
            <CardHeader>
              <CardTitle className="text-base">{t("bot.configTitle")}</CardTitle>
            </CardHeader>
            <CardContent className="grid gap-2 text-sm sm:grid-cols-2">
              <Row label={t("settings.guildLabel")} value={runtime.configuration.guildId || t("common.none")} mono />
              <Row label={t("settings.categoryLabel")} value={runtime.configuration.categoryId || t("common.none")} mono />
              <Row label={t("settings.logLabel")} value={runtime.configuration.logChannelId || t("common.none")} mono />
              <Row label={t("settings.debugLabel")} value={runtime.configuration.debugChannelId || t("common.none")} mono />
              <Row label={t("settings.outdateLabel")} value={String(runtime.configuration.outdateHours)} />
              <Row
                label={t("settings.tokenLabel")}
                value={runtime.configuration.hasKookToken ? runtime.configuration.kookTokenMasked : t("common.none")}
                mono
              />
            </CardContent>
          </Card>
        </>
      ) : null}

      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <ShieldCheck className="size-4" />
              {t("bot.rolesTitle")}
            </CardTitle>
            <CardDescription>{rolesQuery.data?.note ?? t("roles.adminDesc")}</CardDescription>
          </CardHeader>
          <CardContent>
            {rolesQuery.isPending ? (
              <InlineLoader />
            ) : (rolesQuery.data?.items.length ?? 0) === 0 ? (
              <EmptyState />
            ) : (
              <ul className="space-y-2 text-sm">
                {rolesQuery.data?.items.map((role) => (
                  <li key={role.id} className="flex items-center justify-between gap-2">
                    <span>{role.name}</span>
                    <span className="text-muted-foreground font-mono text-xs">{role.id}</span>
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <Hash className="size-4" />
              {t("bot.channelsTitle")}
            </CardTitle>
            <CardDescription>{channelsQuery.data?.note ?? t("settings.categoryHint")}</CardDescription>
          </CardHeader>
          <CardContent>
            {channelsQuery.isPending ? (
              <InlineLoader />
            ) : (channelsQuery.data?.items.length ?? 0) === 0 ? (
              <EmptyState />
            ) : (
              <ul className="space-y-2 text-sm">
                {channelsQuery.data?.items.map((channel) => (
                  <li key={channel.id} className="flex items-center justify-between gap-2">
                    <span className="truncate">
                      {channel.name}
                      {channel.kind ? (
                        <Badge variant="outline" className="ml-2 font-normal">
                          {channel.kind}
                        </Badge>
                      ) : null}
                    </span>
                    <span className="text-muted-foreground font-mono text-xs">{channel.id}</span>
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  )
}

function Row({ label, value, mono }: { label: string; value: React.ReactNode; mono?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <span className="text-muted-foreground shrink-0 text-xs">{label}</span>
      <span className={mono ? "min-w-0 text-right font-mono text-xs break-all" : "min-w-0 text-right"}>{value}</span>
    </div>
  )
}
