/**
 * 侧边栏导航。
 *
 * 说明：这里按角色隐藏无权限入口只是体验优化，
 * 真正的权限校验由服务端中间件强制执行（越权请求会返回 403）。
 */

import {
  BarChart3,
  Bot,
  Gamepad2,
  LayoutDashboard,
  LayoutPanelLeft,
  ScrollText,
  Settings,
  ShieldCheck,
  SmilePlus,
  Ticket,
  Users,
} from "lucide-react"
import { useTranslation } from "react-i18next"
import { NavLink } from "react-router"

import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
} from "@/components/ui/sidebar"
import { UserMenu } from "@/components/UserMenu"

interface NavItem {
  to: string
  labelKey: string
  icon: typeof LayoutDashboard
  minRole?: "admin" | "staff"
  end?: boolean
}

interface NavGroup {
  labelKey: string
  items: NavItem[]
}

const NAV_GROUPS: NavGroup[] = [
  {
    labelKey: "nav.sectionMain",
    items: [
      { to: "/", labelKey: "nav.dashboard", icon: LayoutDashboard, end: true },
      { to: "/tickets", labelKey: "nav.tickets", icon: Ticket },
      { to: "/stats", labelKey: "nav.stats", icon: BarChart3 },
    ],
  },
  {
    labelKey: "nav.sectionConfig",
    items: [
      { to: "/panels", labelKey: "nav.panels", icon: LayoutPanelLeft },
      { to: "/emoji-roles", labelKey: "nav.emoji", icon: SmilePlus },
      { to: "/roles", labelKey: "nav.roles", icon: ShieldCheck, minRole: "admin" },
    ],
  },
  {
    labelKey: "nav.sectionSystem",
    items: [
      { to: "/bot", labelKey: "nav.bot", icon: Bot },
      { to: "/activity", labelKey: "nav.activity", icon: Gamepad2 },
      { to: "/users", labelKey: "nav.users", icon: Users, minRole: "admin" },
      { to: "/settings", labelKey: "nav.settings", icon: Settings, minRole: "admin" },
      { to: "/audit", labelKey: "nav.audit", icon: ScrollText, minRole: "admin" },
    ],
  },
]

const ROLE_RANK: Record<string, number> = { admin: 3, staff: 2, readonly: 1 }

export function AppSidebar({ role }: { role: string }) {
  const { t } = useTranslation()
  const rank = ROLE_RANK[role] ?? 0

  return (
    <Sidebar collapsible="icon">
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" asChild>
              <NavLink to="/">
                <div className="bg-primary text-primary-foreground flex size-8 shrink-0 items-center justify-center rounded-lg">
                  <Ticket className="size-4" />
                </div>
                <div className="grid flex-1 text-left text-sm leading-tight">
                  <span className="truncate font-semibold">{t("app.name")}</span>
                  <span className="text-muted-foreground truncate text-xs">{t("app.tagline")}</span>
                </div>
              </NavLink>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent>
        {NAV_GROUPS.map((group) => {
          const items = group.items.filter((item) => (item.minRole ? rank >= ROLE_RANK[item.minRole] : true))
          if (items.length === 0) return null
          return (
            <SidebarGroup key={group.labelKey}>
              <SidebarGroupLabel>{t(group.labelKey)}</SidebarGroupLabel>
              <SidebarMenu>
                {items.map((item) => (
                  <SidebarMenuItem key={item.to}>
                    <SidebarMenuButton asChild tooltip={t(item.labelKey)}>
                      <NavLink to={item.to} end={item.end}>
                        <item.icon className="size-4" />
                        <span>{t(item.labelKey)}</span>
                      </NavLink>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            </SidebarGroup>
          )
        })}
      </SidebarContent>

      <SidebarFooter>
        <UserMenu />
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  )
}
