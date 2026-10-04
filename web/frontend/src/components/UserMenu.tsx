/** 侧边栏底部的账号菜单：展示角色、跳转改密、登出。 */

import { ChevronsUpDown, KeyRound, LogOut, UserCircle2 } from "lucide-react"
import { useTranslation } from "react-i18next"
import { useNavigate } from "react-router"

import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { SidebarMenu, SidebarMenuButton, SidebarMenuItem, useSidebar } from "@/components/ui/sidebar"
import { useAuth } from "@/lib/auth"
import { toastError } from "@/lib/toast"

export function UserMenu() {
  const { me, logout } = useAuth()
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { isMobile } = useSidebar()

  if (!me) return null

  const initial = (me.user.displayName || me.user.username).slice(0, 1).toUpperCase()

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton size="lg" className="data-[state=open]:bg-sidebar-accent">
              <Avatar className="size-8 rounded-lg">
                <AvatarFallback className="bg-primary/12 text-primary rounded-lg text-xs font-medium">{initial}</AvatarFallback>
              </Avatar>
              <div className="grid flex-1 text-left text-sm leading-tight">
                <span className="truncate font-medium">{me.user.displayName || me.user.username}</span>
                <span className="text-muted-foreground truncate text-xs">{t(`role.${me.user.role}`)}</span>
              </div>
              <ChevronsUpDown className="ml-auto size-4" />
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            className="w-56"
            side={isMobile ? "bottom" : "right"}
            align="end"
            sideOffset={4}
          >
            <DropdownMenuLabel className="space-y-0.5">
              <p className="truncate text-sm font-medium">{me.user.displayName || me.user.username}</p>
              <p className="text-muted-foreground truncate text-xs">
                @{me.user.username} · {t(`role.${me.user.role}`)}
              </p>
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={() => navigate("/account")}>
              <UserCircle2 className="size-4" />
              {t("account.title")}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => navigate("/change-password")}>
              <KeyRound className="size-4" />
              {t("nav.changePassword")}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onClick={async () => {
                try {
                  await logout()
                } catch (error) {
                  toastError(error)
                }
                navigate("/login", { replace: true })
              }}
            >
              <LogOut className="size-4" />
              {t("nav.logout")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  )
}
