/** 仪表盘：KPI、趋势图、状态分布与最近工单。 */

import { useState } from "react"
import { Clock, Layers, MessageSquareReply, Ticket as TicketIcon, Timer, TrendingUp, UserCheck } from "lucide-react"
import { useTranslation } from "react-i18next"
import { Area, AreaChart, CartesianGrid, Cell, Pie, PieChart, XAxis, YAxis } from "recharts"
import { Link } from "react-router"

import { PageHeader } from "@/components/PageHeader"
import { StatusBadge } from "@/components/Badges"
import { EmptyState, ErrorState, InlineLoader } from "@/components/StateViews"
import { StatCard } from "@/components/StatCard"
import { Button } from "@/components/ui/button"
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@/components/ui/chart"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { formatDateTime, formatDuration, statusColorClass } from "@/lib/format"
import { useStats, useTickets } from "@/lib/queries"
import { cn } from "@/lib/utils"

const RANGE_OPTIONS = [7, 14, 30, 90] as const

export function DashboardPage() {
  const { t, i18n } = useTranslation()
  const [days, setDays] = useState<number>(7)

  const statsQuery = useStats(days)
  const recentQuery = useTickets({ page: 1, pageSize: 5 })

  const trendConfig: ChartConfig = {
    opened: { label: t("dashboard.trendOpened"), color: "var(--chart-1)" },
    closed: { label: t("dashboard.trendClosed"), color: "var(--chart-2)" },
  }

  const statusData = statsQuery.data
    ? Object.entries(statsQuery.data.statusCounts)
        .filter(([, count]) => count > 0)
        .map(([status, count]) => ({ status, count }))
    : []

  const statusColor = (status: string) => {
    switch (status) {
      case "open":
        return "var(--status-open)"
      case "locked":
        return "var(--status-locked)"
      case "closed":
        return "var(--status-closed)"
      case "failed":
        return "var(--status-failed)"
      default:
        return "var(--status-pending)"
    }
  }

  const statusLabel = (status: string) => t(`status.${status}` as const, { defaultValue: status })

  // 按状态名索引：饼图 tooltip 以 nameKey="status" 从 config 取已翻译的状态名，
  // 不能把带 {{count}} 占位符的文案（common.total）当静态 label，否则会渲染出字面量。
  const statusConfig: ChartConfig = Object.fromEntries(
    statusData.map((entry) => [
      entry.status,
      { label: statusLabel(entry.status), color: statusColor(entry.status) },
    ])
  )

  return (
    <div className="space-y-6">
      <PageHeader title={t("dashboard.title")} description={t("dashboard.description")}>
        <Select value={String(days)} onValueChange={(value) => setDays(Number(value))}>
          <SelectTrigger className="w-40" aria-label={t("dashboard.range")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {RANGE_OPTIONS.map((option) => (
              <SelectItem key={option} value={String(option)}>
                {t("dashboard.rangeDays", { days: option })}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button variant="outline" size="sm" onClick={() => void statsQuery.refetch()}>
          {t("common.refresh")}
        </Button>
      </PageHeader>

      {statsQuery.isError ? (
        <ErrorState error={statsQuery.error} onRetry={() => void statsQuery.refetch()} />
      ) : statsQuery.isPending ? (
        <InlineLoader />
      ) : statsQuery.data ? (
        <div className="space-y-6">
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
            <StatCard
              label={t("dashboard.kpiTotal")}
              value={statsQuery.data.total}
              hint={t("dashboard.kpiUniqueUsers") + `: ${statsQuery.data.uniqueUsers}`}
              icon={<TicketIcon className="size-4" />}
            />
            <StatCard
              label={t("dashboard.kpiActive")}
              value={statsQuery.data.open + statsQuery.data.locked}
              hint={`${t("status.open")} ${statsQuery.data.open} · ${t("status.locked")} ${statsQuery.data.locked}`}
              icon={<Layers className="size-4" />}
            />
            <StatCard
              label={t("dashboard.kpiOpenedToday")}
              value={statsQuery.data.openedToday}
              hint={`${t("dashboard.kpiClosedToday")}: ${statsQuery.data.closedToday}`}
              icon={<TrendingUp className="size-4" />}
            />
            <StatCard
              label={t("dashboard.kpiClosed")}
              value={statsQuery.data.closed}
              hint={t("dashboard.kpiClosedHint")}
              icon={<UserCheck className="size-4" />}
            />
            <StatCard
              label={t("dashboard.kpiFirstReply")}
              value={formatDuration(statsQuery.data.avgFirstReplySeconds)}
              icon={<MessageSquareReply className="size-4" />}
            />
            <StatCard
              label={t("dashboard.kpiResolution")}
              value={formatDuration(statsQuery.data.avgResolutionSeconds)}
              icon={<Timer className="size-4" />}
            />
            <StatCard
              label={t("dashboard.kpiLocked")}
              value={statsQuery.data.locked}
              icon={<Clock className="size-4" />}
            />
            <StatCard
              label={t("dashboard.range")}
              value={t("dashboard.rangeDays", { days: statsQuery.data.rangeDays })}
              hint={formatDateTime(statsQuery.data.generatedAt, i18n.language)}
              icon={<TrendingUp className="size-4" />}
            />
          </div>

          <div className="grid gap-4 lg:grid-cols-3">
            <Card className="lg:col-span-2">
              <CardHeader>
                <CardTitle className="text-base">{t("dashboard.trendTitle")}</CardTitle>
                <CardDescription>{t("dashboard.rangeDays", { days: statsQuery.data.rangeDays })}</CardDescription>
              </CardHeader>
              <CardContent>
                <ChartContainer config={trendConfig} className="h-[260px] w-full">
                  <AreaChart data={statsQuery.data.trend} margin={{ left: 4, right: 12, top: 8 }}>
                    <CartesianGrid vertical={false} />
                    <XAxis dataKey="date" tickLine={false} axisLine={false} tickMargin={8} fontSize={11} />
                    <YAxis tickLine={false} axisLine={false} width={28} fontSize={11} allowDecimals={false} />
                    <ChartTooltip content={<ChartTooltipContent />} />
                    <ChartLegend content={<ChartLegendContent />} />
                    <Area
                      dataKey="opened"
                      type="monotone"
                      fill="var(--color-opened)"
                      fillOpacity={0.25}
                      stroke="var(--color-opened)"
                      strokeWidth={2}
                    />
                    <Area
                      dataKey="closed"
                      type="monotone"
                      fill="var(--color-closed)"
                      fillOpacity={0.25}
                      stroke="var(--color-closed)"
                      strokeWidth={2}
                    />
                  </AreaChart>
                </ChartContainer>
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle className="text-base">{t("dashboard.statusTitle")}</CardTitle>
                <CardDescription>{t("dashboard.kpiTotal")}</CardDescription>
              </CardHeader>
              <CardContent>
                {statusData.length === 0 ? (
                  <EmptyState />
                ) : (
                  <div className="space-y-4">
                    <ChartContainer config={statusConfig} className="mx-auto h-[210px] w-full">
                      <PieChart>
                        <ChartTooltip content={<ChartTooltipContent nameKey="status" hideLabel />} />
                        <Pie data={statusData} dataKey="count" nameKey="status" innerRadius={55} outerRadius={90}>
                          {statusData.map((entry) => (
                            <Cell key={entry.status} fill={statusColor(entry.status)} />
                          ))}
                        </Pie>
                      </PieChart>
                    </ChartContainer>
                    {/* 自绘图例：preset 的 ChartLegendContent 不支持自定义文案，这里直接渲染状态名与数量 */}
                    <ul className="space-y-1.5 text-sm">
                      {statusData.map((entry) => (
                        <li key={entry.status} className="flex items-center gap-2">
                          <span
                            className="size-2.5 shrink-0 rounded-full"
                            style={{ backgroundColor: statusColor(entry.status) }}
                          />
                          <span>{statusLabel(entry.status)}</span>
                          <span className="text-muted-foreground ml-auto tabular-nums">{entry.count}</span>
                        </li>
                      ))}
                    </ul>
                  </div>
                )}
              </CardContent>
            </Card>
          </div>
        </div>
      ) : null}

      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("dashboard.recentTitle")}</CardTitle>
          <CardDescription>{t("tickets.description")}</CardDescription>
          <CardAction>
            <Button asChild variant="outline" size="sm">
              <Link to="/tickets">{t("dashboard.viewAll")}</Link>
            </Button>
          </CardAction>
        </CardHeader>
        <CardContent className="px-0">
          {recentQuery.isPending ? (
            <InlineLoader />
          ) : recentQuery.isError ? (
            <div className="px-6">
              <ErrorState error={recentQuery.error} onRetry={() => void recentQuery.refetch()} />
            </div>
          ) : (recentQuery.data?.items.length ?? 0) === 0 ? (
            <EmptyState title={t("dashboard.recentEmpty")} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("tickets.colNo")}</TableHead>
                  <TableHead>{t("tickets.colUser")}</TableHead>
                  <TableHead>{t("tickets.colStatus")}</TableHead>
                  <TableHead className="hidden sm:table-cell">{t("tickets.colMessages")}</TableHead>
                  <TableHead>{t("tickets.colStarted")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {recentQuery.data?.items.map((ticket) => (
                  <TableRow key={ticket.no} className="cursor-pointer">
                    <TableCell className="font-mono text-xs">
                      <Link to={`/tickets/${ticket.no}`} className="hover:text-primary">
                        {ticket.no}
                      </Link>
                    </TableCell>
                    <TableCell className="max-w-40 truncate">{ticket.userName}</TableCell>
                    <TableCell>
                      <StatusBadge status={ticket.status} />
                    </TableCell>
                    <TableCell className={cn("hidden tabular-nums sm:table-cell", statusColorClass(ticket.status))}>
                      {ticket.messageCount}
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs">
                      {formatDateTime(ticket.startedAt, i18n.language)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
