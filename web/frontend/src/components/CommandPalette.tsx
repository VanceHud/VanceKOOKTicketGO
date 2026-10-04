/**
 * Cmd/Ctrl+K 命令面板。
 *
 * 能力：跳转页面、按编号/昵称/内容搜索工单、对当前打开的工单执行锁定/关闭/重开、
 * 切换主题与语言、登出。工单操作通过路由 query 参数交给详情页处理，
 * 这样确认对话框与权限判断只保留一处实现。
 */

import { useEffect, useMemo, useState } from "react"
import {
  ArrowRight,
  Bot,
  Gamepad2,
  LayoutDashboard,
  LayoutPanelLeft,
  LocateFixed,
  LockKeyhole,
  LogOut,
  Moon,
  ScrollText,
  Settings,
  ShieldCheck,
  SmilePlus,
  Sunrise,
  Ticket,
  UnlockKeyhole,
  Users,
  XCircle,
} from "lucide-react"
import { useQuery } from "@tanstack/react-query"
import { useTheme } from "next-themes"
import { useTranslation } from "react-i18next"
import { useLocation, useNavigate } from "react-router"

import { StatusBadge } from "@/components/Badges"
import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
  CommandShortcut,
} from "@/components/ui/command"
import { api } from "@/lib/api"
import { useAuth } from "@/lib/auth"
import { changeLanguage, type SupportedLanguage } from "@/lib/i18n"
import type { ListResponse, Ticket as TicketType } from "@/lib/types"
import { toastError } from "@/lib/toast"

const NAV_ITEMS = [
  { to: "/", labelKey: "nav.dashboard", icon: LayoutDashboard },
  { to: "/tickets", labelKey: "nav.tickets", icon: Ticket },
  { to: "/panels", labelKey: "nav.panels", icon: LayoutPanelLeft },
  { to: "/emoji-roles", labelKey: "nav.emoji", icon: SmilePlus },
  { to: "/roles", labelKey: "nav.roles", icon: ShieldCheck },
  { to: "/bot", labelKey: "nav.bot", icon: Bot },
  { to: "/activity", labelKey: "nav.activity", icon: Gamepad2 },
  { to: "/users", labelKey: "nav.users", icon: Users },
  { to: "/settings", labelKey: "nav.settings", icon: Settings },
  { to: "/audit", labelKey: "nav.audit", icon: ScrollText },
] as const

interface CommandPaletteProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function CommandPalette({ open, onOpenChange }: CommandPaletteProps) {
  const { t, i18n } = useTranslation()
  const navigate = useNavigate()
  const location = useLocation()
  const { setTheme, resolvedTheme } = useTheme()
  const { logout } = useAuth()

  const [term, setTerm] = useState("")
  const [debounced, setDebounced] = useState("")

  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(term.trim()), 250)
    return () => window.clearTimeout(timer)
  }, [term])

  // 全局快捷键：Cmd+K / Ctrl+K
  useEffect(() => {
    const handler = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault()
        onOpenChange(!open)
      }
    }
    window.addEventListener("keydown", handler)
    return () => window.removeEventListener("keydown", handler)
  }, [open, onOpenChange])

  const activeTicketNo = useMemo(() => {
    const match = location.pathname.match(/^\/tickets\/([^/]+)$/)
    return match?.[1]
  }, [location.pathname])

  const ticketsQuery = useQuery({
    queryKey: ["command-tickets", debounced, activeTicketNo],
    queryFn: () => api.get<ListResponse<TicketType>>(`/tickets?pageSize=6&q=${encodeURIComponent(debounced)}`),
    enabled: open && debounced.length > 0,
    staleTime: 10_000,
  })

  const run = (action: () => void) => {
    onOpenChange(false)
    setTerm("")
    action()
  }

  const results = ticketsQuery.data?.items ?? []

  return (
    <CommandDialog open={open} onOpenChange={onOpenChange} title={t("command.title")} description={t("command.hint")}>
      {/* 注意：本 preset 的 CommandDialog 只负责弹窗外壳，cmdk 的上下文需要调用方自行包裹 */}
      <Command>
      <CommandInput placeholder={t("command.placeholder")} value={term} onValueChange={setTerm} />
      <CommandList>
        <CommandEmpty>{t("command.noResults")}</CommandEmpty>

        {debounced.length > 0 ? (
          <CommandGroup heading={t("command.groupTickets")}>
            {results.map((ticket) => (
              <CommandItem
                key={ticket.no}
                value={`${ticket.no} ${ticket.userName}`}
                onSelect={() => run(() => navigate(`/tickets/${ticket.no}`))}
              >
                <Ticket className="size-4" />
                <span className="font-mono text-xs">{ticket.no}</span>
                <span className="text-muted-foreground truncate">{ticket.userName}</span>
                <span className="ml-auto">
                  <StatusBadge status={ticket.status} />
                </span>
              </CommandItem>
            ))}
          </CommandGroup>
        ) : null}

        {activeTicketNo ? (
          <>
            <CommandGroup heading={t("command.groupActions")}>
              <CommandItem onSelect={() => run(() => navigate(`/tickets/${activeTicketNo}?action=lock`))}>
                <LockKeyhole className="size-4" />
                {t("command.actionLock")}
              </CommandItem>
              <CommandItem onSelect={() => run(() => navigate(`/tickets/${activeTicketNo}?action=reopen`))}>
                <UnlockKeyhole className="size-4" />
                {t("command.actionReopen")}
              </CommandItem>
              <CommandItem onSelect={() => run(() => navigate(`/tickets/${activeTicketNo}?action=close`))}>
                <XCircle className="size-4" />
                {t("command.actionClose")}
              </CommandItem>
              <CommandItem onSelect={() => run(() => navigate(`/tickets/${activeTicketNo}`))}>
                <LocateFixed className="size-4" />
                {t("ticket.backToList")}
                <ArrowRight className="ml-auto size-4" />
              </CommandItem>
            </CommandGroup>
            <CommandSeparator />
          </>
        ) : null}

        <CommandGroup heading={t("command.groupNavigation")}>
          {NAV_ITEMS.map((item) => (
            <CommandItem key={item.to} value={t(item.labelKey)} onSelect={() => run(() => navigate(item.to))}>
              <item.icon className="size-4" />
              {t(item.labelKey)}
            </CommandItem>
          ))}
        </CommandGroup>

        <CommandSeparator />

        <CommandGroup heading={t("command.groupPreferences")}>
          <CommandItem
            value={t("command.toggleTheme")}
            onSelect={() => run(() => setTheme(resolvedTheme === "dark" ? "light" : "dark"))}
          >
            {resolvedTheme === "dark" ? <Sunrise className="size-4" /> : <Moon className="size-4" />}
            {t("command.toggleTheme")}
          </CommandItem>
          <CommandItem
            value={t("command.toggleLanguage")}
            onSelect={() =>
              run(() => changeLanguage((i18n.language.startsWith("zh") ? "en-US" : "zh-CN") as SupportedLanguage))
            }
          >
            {t("command.toggleLanguage")}
          </CommandItem>
          <CommandItem
            value={t("command.logout")}
            onSelect={() =>
              run(() => {
                void logout()
                  .catch((error: unknown) => toastError(error))
                  .finally(() => navigate("/login", { replace: true }))
              })
            }
          >
            <LogOut className="size-4" />
            {t("command.logout")}
          </CommandItem>
        </CommandGroup>
      </CommandList>
      <div className="text-muted-foreground flex items-center gap-2 border-t px-3 py-2 text-xs">
        <CommandShortcut>⌘K</CommandShortcut>
        {t("command.hint")}
      </div>
      </Command>
    </CommandDialog>
  )
}
