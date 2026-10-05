/**
 * 统计看板：处理效率分位、时段分布、客服处理量与来源分析。
 *
 * 所有分桶都由后端按业务时区（默认北京时间）计算，前端只做展示，
 * 避免“面板按北京时间、图表按浏览器时区”这类口径不一致。
 */

import { useState } from "react"
import { BarChart3, Clock, MessageSquare, Timer, TrendingDown, TrendingUp, Users } from "lucide-react"
import { useTranslation } from "react-i18next"
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts"
import { Link } from "react-router"

import { PageHeader } from "@/components/PageHeader"
import { EmptyState, ErrorState, InlineLoader } from "@/components/StateViews"
import { StatCard } from "@/components/StatCard"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@/components/ui/chart"
import { Progress } from "@/components/ui/progress"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { formatDateTime, formatDuration } from "@/lib/format"
import { timezoneLabel } from "@/lib/timezone"
import { useAnalytics } from "@/lib/queries"
import type { DurationStats } from "@/lib/types"

const RANGE_OPTIONS = [7, 14, 30, 90] as const

function percent(value: number): string {
  return `${(value * 100).toFixed(1)}%`
}

export function StatsPage() {
  const { t, i18n } = useTranslation()
  const [days, setDays] = useState<number>(30)
  const query = useAnalytics(days)

  const tzLabel = timezoneLabel(i18n.language)

  const hourlyConfig: ChartConfig = {
    opened: { label: t("stats.compareOpened"), color: "var(--chart-1)" },
    closed: { label: t("stats.compareClosed"), color: "var(--chart-2)" },
  }

  const hourlyData =
    query.data?.hourly.map((bucket) => ({
      hour: `${String(bucket.hour).padStart(2, "0")}:00`,
      opened: bucket.opened,
      closed: bucket.closed,
    })) ?? []

  return (
    <div className="space-y-5">
      <PageHeader title={t("stats.title")} description={t("stats.description")}>
        <Select value={String(days)} onValueChange={(value) => setDays(Number(value))}>
          <SelectTrigger className="w-40" aria-label={t("stats.range")}>
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
        <Button variant="outline" size="sm" onClick={() => void query.refetch()}>
          {t("common.refresh")}
        </Button>
      </PageHeader>

      <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
        <Clock className="size-3.5" />
        {t("stats.timezoneNote", { timezone: tzLabel })}
      </p>

      {query.isError ? (
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      ) : query.isPending ? (
        <InlineLoader />
      ) : !query.data || (query.data.total === 0 && query.data.resolution.count === 0 && query.data.firstReply.count === 0 && query.data.archivedMessages === 0) ? (
        <Card>
          <CardContent>
            <EmptyState title={t("stats.empty")} icon={<BarChart3 className="size-6" />} />
          </CardContent>
        </Card>
      ) : (
        <>
          {/* KPI */}
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            <StatCard
              label={t("stats.kpiTotal")}
              value={query.data.total}
              hint={`${t("stats.kpiClosedRate")}: ${percent(query.data.closedRate)}`}
              icon={<BarChart3 className="size-4" />}
            />
            <StatCard
              label={t("stats.kpiMessages")}
              value={query.data.archivedMessages}
              hint={`${t("stats.kpiAvgMessages")}: ${query.data.avgMessagesPerTicket.toFixed(1)}`}
              icon={<MessageSquare className="size-4" />}
            />
            <StatCard
              label={t("stats.compareTitle")}
              value={`${query.data.openedToday} / ${query.data.openedYesterday}`}
              hint={`${t("stats.today")} ${t("stats.compareOpened")} / ${t("stats.yesterday")} ${t("stats.compareOpened")}`}
              icon={query.data.openedToday >= query.data.openedYesterday ? <TrendingUp className="size-4" /> : <TrendingDown className="size-4" />}
            />
          </div>

          {/* 时长分位 */}
          <div className="grid gap-4 md:grid-cols-2">
            <DurationCard
              title={t("stats.kpiFirstReply")}
              icon={<Clock className="size-4" />}
              stats={query.data.firstReply}
            />
            <DurationCard
              title={t("stats.kpiResolution")}
              icon={<Timer className="size-4" />}
              stats={query.data.resolution}
            />
          </div>

          {/* 时段分布 */}
          <Card>
            <CardHeader>
              <CardTitle className="text-base">{t("stats.hourlyTitle")}</CardTitle>
              <CardDescription>{t("stats.hourlyDesc")}</CardDescription>
            </CardHeader>
            <CardContent>
              <ChartContainer config={hourlyConfig} className="h-[280px] w-full">
                <BarChart data={hourlyData} margin={{ left: 4, right: 12, top: 8 }}>
                  <CartesianGrid vertical={false} />
                  <XAxis dataKey="hour" tickLine={false} axisLine={false} tickMargin={8} fontSize={10} interval={1} />
                  <YAxis tickLine={false} axisLine={false} width={28} fontSize={11} allowDecimals={false} />
                  <ChartTooltip content={<ChartTooltipContent />} />
                  <ChartLegend content={<ChartLegendContent />} />
                  <Bar dataKey="opened" fill="var(--color-opened)" radius={[3, 3, 0, 0]} />
                  <Bar dataKey="closed" fill="var(--color-closed)" radius={[3, 3, 0, 0]} />
                </BarChart>
              </ChartContainer>
            </CardContent>
          </Card>

          <div className="grid gap-4 lg:grid-cols-2">
            {/* 客服处理量 */}
            <Card>
              <CardHeader>
                <CardTitle className="flex items-center gap-2 text-base">
                  <Users className="size-4" />
                  {t("stats.closersTitle")}
                </CardTitle>
                <CardDescription>{t("stats.closersDesc")}</CardDescription>
              </CardHeader>
              <CardContent className="px-0">
                {query.data.closers.length === 0 ? (
                  <EmptyState className="py-8" />
                ) : (
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>{t("stats.colCloser")}</TableHead>
                        <TableHead className="text-right">{t("stats.colClosedCount")}</TableHead>
                        <TableHead className="text-right">{t("stats.colAvgResolution")}</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {query.data.closers.map((closer) => (
                        <TableRow key={closer.name}>
                          <TableCell className="max-w-40 truncate">{closer.name}</TableCell>
                          <TableCell className="text-right tabular-nums">{closer.closed}</TableCell>
                          <TableCell className="text-muted-foreground text-right tabular-nums">
                            {formatDuration(closer.avgResolutionSeconds)}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                )}
              </CardContent>
            </Card>

            {/* 来源面板 */}
            <Card>
              <CardHeader>
                <CardTitle className="flex items-center gap-2 text-base">
                  <BarChart3 className="size-4" />
                  {t("stats.sourcesTitle")}
                </CardTitle>
                <CardDescription>{t("stats.sourcesDesc")}</CardDescription>
              </CardHeader>
              <CardContent className="px-0">
                {query.data.sources.length === 0 ? (
                  <EmptyState className="py-8" />
                ) : (
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>{t("stats.colSource")}</TableHead>
                        <TableHead className="text-right">{t("stats.colOpened")}</TableHead>
                        <TableHead className="text-right">{t("stats.colCloseRate")}</TableHead>
                        <TableHead />
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {query.data.sources.map((source) => {
                        const [channelID, channelName] = source.channelId.includes("|")
                          ? source.channelId.split("|")
                          : [source.channelId, source.channelId]
                        return (
                          <TableRow key={source.channelId}>
                            <TableCell>
                              <div className="space-y-0.5">
                                <p className="text-sm">{channelName}</p>
                                <p className="text-muted-foreground font-mono text-xs">{channelID}</p>
                              </div>
                            </TableCell>
                            <TableCell className="text-right tabular-nums">{source.opened}</TableCell>
                            <TableCell className="text-right tabular-nums">{percent(source.closedRate)}</TableCell>
                            <TableCell className="w-24">
                              <Progress value={source.closedRate * 100} className="h-1.5" />
                            </TableCell>
                          </TableRow>
                        )
                      })}
                    </TableBody>
                  </Table>
                )}
              </CardContent>
            </Card>
          </div>

          <p className="text-muted-foreground text-xs">
            {formatDateTime(query.data.generatedAt, i18n.language)} ·{" "}
            <Link to="/dashboard" className="hover:text-primary underline-offset-4 hover:underline">
              {t("dashboard.title")}
            </Link>
          </p>
        </>
      )}
    </div>
  )
}

/** 时长分位卡片：平均 / P50 / P90。 */
function DurationCard({ title, icon, stats }: { title: string; icon: React.ReactNode; stats: DurationStats }) {
  const { t } = useTranslation()
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          {icon}
          {title}
        </CardTitle>
      </CardHeader>
      <CardContent className="grid gap-3 sm:grid-cols-3">
        <Metric label={t("stats.avgLabel")} value={stats.count > 0 ? formatDuration(stats.avgSeconds) : "—"} />
        <Metric label={t("stats.p50Label")} value={stats.count > 0 ? formatDuration(stats.p50Seconds) : "—"} />
        <Metric label={t("stats.p90Label")} value={stats.count > 0 ? formatDuration(stats.p90Seconds) : "—"} />
        <p className="text-muted-foreground sm:col-span-3 text-xs">
          {t("common.total", { count: stats.count })}
        </p>
      </CardContent>
    </Card>
  )
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="space-y-1">
      <p className="text-muted-foreground text-xs">{label}</p>
      <p className="text-lg font-semibold tabular-nums">{value}</p>
    </div>
  )
}
