/**
 * 工单列表。
 *
 * 筛选条件保存在 URL query 中，便于分享与前进后退；
 * 数据分页在服务端完成，表格只负责渲染与列定义（TanStack Table v8）。
 */

import { useEffect, useMemo, useState } from "react"
import { FilterX, Search } from "lucide-react"
import { flexRender, getCoreRowModel, useReactTable, type ColumnDef } from "@tanstack/react-table"
import { useTranslation } from "react-i18next"
import { Link, useNavigate, useSearchParams } from "react-router"

import { StatusBadge } from "@/components/Badges"
import { PageHeader } from "@/components/PageHeader"
import { Pagination } from "@/components/Pagination"
import { EmptyState, ErrorState, InlineLoader } from "@/components/StateViews"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { formatDateTime } from "@/lib/format"
import { useTickets } from "@/lib/queries"
import type { Ticket } from "@/lib/types"

const PAGE_SIZE = 20
const STATUS_FILTERS = ["open", "locked", "closed", "pending", "failed"] as const

export function TicketsPage() {
  const { t, i18n } = useTranslation()
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()

  const page = Math.max(1, Number(searchParams.get("page") ?? "1") || 1)
  const q = searchParams.get("q") ?? ""
  const status = searchParams.get("status") ?? ""
  const from = searchParams.get("from") ?? ""
  const to = searchParams.get("to") ?? ""

  const [term, setTerm] = useState(q)
  useEffect(() => {
    setTerm(q)
  }, [q])

  // 输入防抖：300ms 后把关键词写回 URL，避免每次按键都请求。
  useEffect(() => {
    if (term === q) return
    const timer = window.setTimeout(() => {
      updateParams({ q: term, page: "1" })
    }, 300)
    return () => window.clearTimeout(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [term])

  function updateParams(patch: Record<string, string>) {
    const next = new URLSearchParams(searchParams)
    for (const [key, value] of Object.entries(patch)) {
      if (value) {
        next.set(key, value)
      } else {
        next.delete(key)
      }
    }
    setSearchParams(next, { replace: true })
  }

  const query = useTickets({ page, pageSize: PAGE_SIZE, q, status, from, to })

  const columns = useMemo<ColumnDef<Ticket>[]>(
    () => [
      {
        accessorKey: "no",
        header: t("tickets.colNo"),
        cell: ({ row }) => (
          <Link to={`/tickets/${row.original.no}`} className="hover:text-primary font-mono text-xs">
            {row.original.no}
          </Link>
        ),
      },
      {
        accessorKey: "userName",
        header: t("tickets.colUser"),
        cell: ({ row }) => <span className="block max-w-40 truncate">{row.original.userName}</span>,
      },
      {
        accessorKey: "status",
        header: t("tickets.colStatus"),
        cell: ({ row }) => <StatusBadge status={row.original.status} />,
      },
      {
        accessorKey: "messageCount",
        header: t("tickets.colMessages"),
        cell: ({ row }) => <span className="tabular-nums">{row.original.messageCount}</span>,
      },
      {
        accessorKey: "startedAt",
        header: t("tickets.colStarted"),
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs">{formatDateTime(row.original.startedAt, i18n.language)}</span>
        ),
      },
      {
        accessorKey: "closedAt",
        header: t("tickets.colClosed"),
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs">{formatDateTime(row.original.closedAt, i18n.language)}</span>
        ),
      },
      {
        id: "actions",
        header: "",
        cell: ({ row }) => (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => navigate(`/tickets/${row.original.no}`)}
            aria-label={t("tickets.view")}
          >
            {t("tickets.view")}
          </Button>
        ),
      },
    ],
    [t, i18n.language, navigate],
  )

  const data = query.data?.items ?? []
  const table = useReactTable({
    data,
    columns,
    getCoreRowModel: getCoreRowModel(),
    manualPagination: true,
  })

  const hasFilters = Boolean(q || status || from || to)

  return (
    <div className="space-y-4">
      <PageHeader title={t("tickets.title")} description={t("tickets.description")} />

      {/* 筛选区 */}
      <Card>
        <CardContent className="grid gap-3 pt-6 sm:grid-cols-2 lg:grid-cols-4">
          <div className="relative">
            <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
            <Input
              className="pl-8"
              placeholder={t("tickets.searchPlaceholder")}
              value={term}
              onChange={(event) => setTerm(event.target.value)}
            />
          </div>

          <Select value={status || "all"} onValueChange={(value) => updateParams({ status: value === "all" ? "" : value, page: "1" })}>
            <SelectTrigger aria-label={t("tickets.selectStatus")}>
              <SelectValue placeholder={t("tickets.selectStatus")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("common.all")}</SelectItem>
              {STATUS_FILTERS.map((value) => (
                <SelectItem key={value} value={value}>
                  {t(`status.${value}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Input
            type="date"
            aria-label={t("tickets.dateFrom")}
            value={from}
            onChange={(event) => updateParams({ from: event.target.value, page: "1" })}
          />
          <div className="flex gap-2">
            <Input
              type="date"
              aria-label={t("tickets.dateTo")}
              value={to}
              onChange={(event) => updateParams({ to: event.target.value, page: "1" })}
            />
            <Button
              variant="outline"
              size="icon"
              aria-label={t("tickets.clearFilters")}
              disabled={!hasFilters}
              onClick={() => {
                setTerm("")
                setSearchParams(new URLSearchParams(), { replace: true })
              }}
            >
              <FilterX className="size-4" />
            </Button>
          </div>
        </CardContent>
      </Card>

      {/* 列表 */}
      {query.isError ? (
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      ) : query.isPending ? (
        <InlineLoader />
      ) : data.length === 0 ? (
        <Card>
          <CardContent>
            <EmptyState title={t("tickets.empty")} />
          </CardContent>
        </Card>
      ) : (
        <Card className="py-0">
          <CardContent className="px-0">
            {/* 桌面端表格 */}
            <div className="hidden md:block">
              <Table>
                <TableHeader>
                  {table.getHeaderGroups().map((headerGroup) => (
                    <TableRow key={headerGroup.id}>
                      {headerGroup.headers.map((header) => (
                        <TableHead key={header.id}>
                          {header.isPlaceholder ? null : flexRender(header.column.columnDef.header, header.getContext())}
                        </TableHead>
                      ))}
                    </TableRow>
                  ))}
                </TableHeader>
                <TableBody>
                  {table.getRowModel().rows.map((row) => (
                    <TableRow key={row.id}>
                      {row.getVisibleCells().map((cell) => (
                        <TableCell key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</TableCell>
                      ))}
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>

            {/* 移动端卡片列表 */}
            <div className="divide-y md:hidden">
              {data.map((ticket) => (
                <Link key={ticket.no} to={`/tickets/${ticket.no}`} className="hover:bg-muted/50 block space-y-2 p-4">
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-mono text-xs">{ticket.no}</span>
                    <StatusBadge status={ticket.status} />
                  </div>
                  <div className="flex items-center justify-between gap-2 text-sm">
                    <span className="truncate font-medium">{ticket.userName}</span>
                    <span className="text-muted-foreground shrink-0 text-xs">
                      {t("tickets.colMessages")}: {ticket.messageCount}
                    </span>
                  </div>
                  <p className="text-muted-foreground text-xs">{formatDateTime(ticket.startedAt, i18n.language)}</p>
                </Link>
              ))}
            </div>

            <div className="px-4">
              <Pagination
                page={page}
                pageSize={PAGE_SIZE}
                total={query.data?.total ?? 0}
                onPageChange={(next) => updateParams({ page: String(next) })}
              />
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
