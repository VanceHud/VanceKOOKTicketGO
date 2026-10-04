/** 审计日志（仅管理员）：登录、配置变更与工单操作的时间线。 */

import { useState } from "react"
import { Clock, ScrollText } from "lucide-react"
import { useTranslation } from "react-i18next"

import { PageHeader } from "@/components/PageHeader"
import { Pagination } from "@/components/Pagination"
import { EmptyState, ErrorState, InlineLoader } from "@/components/StateViews"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { formatDateTime } from "@/lib/format"
import { timezoneLabel } from "@/lib/timezone"
import { useAuditLogs } from "@/lib/queries"
import { cn } from "@/lib/utils"

const PAGE_SIZE = 20

const ACTION_OPTIONS = [
  "auth.login.success",
  "auth.login.failed",
  "auth.login.locked",
  "auth.password.changed",
  "auth.logout",
  "settings.update",
  "user.create",
  "user.update",
  "user.delete",
  "role.admin.add",
  "role.admin.remove",
  "role.mapping.upsert",
  "role.mapping.delete",
  "ticket.close",
  "ticket.lock",
  "ticket.reopen",
  "ticket.note",
  "ticket.export",
] as const

function actionTone(action: string): string {
  if (action.includes("failed") || action.includes("locked") || action.includes("blocked")) {
    return "border-destructive/30 text-destructive"
  }
  if (action.startsWith("auth.login")) return "border-status-open/30 text-status-open"
  if (action.startsWith("ticket.")) return "border-primary/30 text-primary"
  return "text-muted-foreground"
}

export function AuditPage() {
  const { t, i18n } = useTranslation()
  const [page, setPage] = useState(1)
  const [action, setAction] = useState("")
  const [actor, setActor] = useState("")
  const [target, setTarget] = useState("")

  const query = useAuditLogs({ page, pageSize: PAGE_SIZE, action, actor, target })
  const items = query.data?.items ?? []

  return (
    <div className="space-y-4">
      <PageHeader title={t("audit.title")} description={t("audit.description")} />

      <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
        <Clock className="size-3.5" />
        {t("stats.timezoneNote", { timezone: timezoneLabel(i18n.language) })}
      </p>

      <Card>
        <CardContent className="grid gap-3 pt-6 sm:grid-cols-3">
          <Select
            value={action || "all"}
            onValueChange={(value) => {
              setAction(value === "all" ? "" : value)
              setPage(1)
            }}
          >
            <SelectTrigger aria-label={t("audit.filterAction")}>
              <SelectValue placeholder={t("audit.filterAction")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("common.all")}</SelectItem>
              {ACTION_OPTIONS.map((value) => (
                <SelectItem key={value} value={value}>
                  {t(`audit.actions.${value}`, { defaultValue: value })}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Input
            placeholder={t("audit.filterActor")}
            value={actor}
            onChange={(event) => {
              setActor(event.target.value)
              setPage(1)
            }}
          />
          <div className="flex gap-2">
            <Input
              placeholder={t("audit.filterTarget")}
              value={target}
              onChange={(event) => {
                setTarget(event.target.value)
                setPage(1)
              }}
            />
            <Button
              variant="outline"
              onClick={() => {
                setAction("")
                setActor("")
                setTarget("")
                setPage(1)
              }}
            >
              {t("common.reset")}
            </Button>
          </div>
        </CardContent>
      </Card>

      {query.isError ? (
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      ) : query.isPending ? (
        <InlineLoader />
      ) : items.length === 0 ? (
        <Card>
          <CardContent>
            <EmptyState title={t("audit.empty")} icon={<ScrollText className="size-6" />} />
          </CardContent>
        </Card>
      ) : (
        <Card className="py-0">
          <CardContent className="px-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("audit.colTime")}</TableHead>
                  <TableHead>{t("audit.colAction")}</TableHead>
                  <TableHead>{t("audit.colActor")}</TableHead>
                  <TableHead className="hidden md:table-cell">{t("audit.colTarget")}</TableHead>
                  <TableHead className="hidden lg:table-cell">{t("audit.colDetail")}</TableHead>
                  <TableHead className="hidden xl:table-cell">{t("audit.colIp")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((entry) => (
                  <TableRow key={entry.id}>
                    <TableCell className="text-muted-foreground text-xs whitespace-nowrap">
                      {formatDateTime(entry.createdAt, i18n.language)}
                    </TableCell>
                    <TableCell>
                      <Badge variant="outline" className={cn("font-normal", actionTone(entry.action))}>
                        {t(`audit.actions.${entry.action}`, { defaultValue: entry.action })}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-sm">{entry.actor || "—"}</TableCell>
                    <TableCell className="hidden font-mono text-xs md:table-cell">{entry.target || "—"}</TableCell>
                    <TableCell className="hidden max-w-80 text-xs lg:table-cell">
                      <span className="line-clamp-2">{entry.detail || "—"}</span>
                    </TableCell>
                    <TableCell className="text-muted-foreground hidden font-mono text-xs xl:table-cell">
                      {entry.ip || "—"}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
            <div className="px-4">
              <Pagination page={page} pageSize={PAGE_SIZE} total={query.data?.total ?? 0} onPageChange={setPage} />
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
