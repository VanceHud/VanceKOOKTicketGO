/**
 * 应用外壳：侧边栏 + 顶栏 + 内容区。
 *
 * 顶栏包含：侧边栏折叠按钮、命令面板入口、实时连接状态、语言与主题切换、账号菜单。
 * SSE 连接在登录后建立，事件会自动刷新对应页面数据。
 */

import { useState } from "react"
import { Radio, Search } from "lucide-react"
import { useTranslation } from "react-i18next"
import { Outlet } from "react-router"

import { AppSidebar } from "@/components/AppSidebar"
import { CommandPalette } from "@/components/CommandPalette"
import { LocaleToggle } from "@/components/LocaleToggle"
import { ThemeToggle } from "@/components/ThemeToggle"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Separator } from "@/components/ui/separator"
import { SidebarInset, SidebarProvider, SidebarTrigger } from "@/components/ui/sidebar"
import { Toaster } from "@/components/ui/sonner"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { useAuth } from "@/lib/auth"
import { useRealtimeEvents } from "@/lib/sse"

export function AppShell() {
  const { me } = useAuth()
  const { t } = useTranslation()
  const [paletteOpen, setPaletteOpen] = useState(false)
  const connected = useRealtimeEvents(Boolean(me))

  return (
    <SidebarProvider>
      <AppSidebar role={me?.user.role ?? "readonly"} />
      <SidebarInset>
        <header className="bg-background/80 sticky top-0 z-10 flex h-14 shrink-0 items-center gap-2 border-b px-3 backdrop-blur">
          <Tooltip>
            <TooltipTrigger asChild>
              <SidebarTrigger />
            </TooltipTrigger>
            <TooltipContent>{t("nav.collapseSidebar")}</TooltipContent>
          </Tooltip>
          <Separator orientation="vertical" className="mx-1 h-5" />

          <Button
            variant="outline"
            size="sm"
            className="text-muted-foreground w-full max-w-64 justify-start gap-2 font-normal"
            onClick={() => setPaletteOpen(true)}
          >
            <Search className="size-4" />
            <span className="truncate">{t("command.placeholder")}</span>
            <kbd className="bg-muted text-muted-foreground ml-auto hidden rounded px-1.5 py-0.5 text-[10px] font-medium sm:inline-block">
              ⌘K
            </kbd>
          </Button>

          <div className="ml-auto flex items-center gap-1.5">
            {me?.server.dryRun ? (
              <Tooltip>
                <TooltipTrigger asChild>
                  <Badge variant="outline" className="border-status-locked/40 text-status-locked hidden sm:inline-flex">
                    {t("bot.dryRun")}
                  </Badge>
                </TooltipTrigger>
                <TooltipContent className="max-w-72">{t("bot.dryRunHint")}</TooltipContent>
              </Tooltip>
            ) : null}

            <Tooltip>
              <TooltipTrigger asChild>
                <Badge
                  variant="outline"
                  className={
                    connected
                      ? "border-status-open/40 text-status-open gap-1"
                      : "text-muted-foreground gap-1"
                  }
                >
                  <Radio className={connected ? "size-3" : "size-3 opacity-50"} />
                  {connected ? t("common.refreshEvery") : t("bot.disconnected")}
                </Badge>
              </TooltipTrigger>
              <TooltipContent>
                {connected ? t("bot.sseSubscribers") : t("bot.connection")}
              </TooltipContent>
            </Tooltip>

            <LocaleToggle />
            <ThemeToggle />
          </div>
        </header>

        <div className="flex-1 p-4 lg:p-6">
          <Outlet />
        </div>
      </SidebarInset>

      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
      <Toaster position="top-right" richColors closeButton />
    </SidebarProvider>
  )
}
