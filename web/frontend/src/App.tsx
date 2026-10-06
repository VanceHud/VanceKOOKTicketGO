/** 路由与访问控制。 */

import { Suspense, lazy } from "react"
import { Navigate, Outlet, Route, Routes, useLocation } from "react-router"

import { AppShell } from "@/components/AppShell"
import { FullScreenLoader } from "@/components/StateViews"
import { TooltipProvider } from "@/components/ui/tooltip"
import { useAuth } from "@/lib/auth"

// 路由级懒加载：首屏只加载登录/外壳所需代码，图表等重依赖随页面按需加载。
const lazyPage = <T extends Record<string, unknown>>(loader: () => Promise<T>, key: keyof T) =>
  lazy(async () => {
    const module = await loader()
    return { default: module[key] as React.ComponentType }
  })

const AccountPage = lazyPage(() => import("@/routes/Account"), "AccountPage")
const LoginPage = lazyPage(() => import("@/routes/Login"), "LoginPage")
const ChangePasswordPage = lazyPage(() => import("@/routes/ChangePassword"), "ChangePasswordPage")
const DashboardPage = lazyPage(() => import("@/routes/Dashboard"), "DashboardPage")
const StatsPage = lazyPage(() => import("@/routes/Stats"), "StatsPage")
const TicketsPage = lazyPage(() => import("@/routes/Tickets"), "TicketsPage")
const TicketDetailPage = lazyPage(() => import("@/routes/TicketDetail"), "TicketDetailPage")
const TicketTypesPage = lazyPage(() => import("@/routes/TicketTypes"), "TicketTypesPage")
const EmojiRolesPage = lazyPage(() => import("@/routes/EmojiRoles"), "EmojiRolesPage")
const RoleMappingPage = lazyPage(() => import("@/routes/RoleMapping"), "RoleMappingPage")
const UsersPage = lazyPage(() => import("@/routes/Users"), "UsersPage")
const BotStatusPage = lazyPage(() => import("@/routes/BotStatus"), "BotStatusPage")
const ActivityPage = lazyPage(() => import("@/routes/Activity"), "ActivityPage")
const SettingsPage = lazyPage(() => import("@/routes/Settings"), "SettingsPage")
const AuditPage = lazyPage(() => import("@/routes/Audit"), "AuditPage")
const NotFoundPage = lazyPage(() => import("@/routes/NotFound"), "NotFoundPage")

/** 需要登录的路由：未登录跳登录页，未改密跳改密页。 */
function RequireAuth() {
  const { isAuthenticated, isLoading, mustChangePassword } = useAuth()
  const location = useLocation()

  if (isLoading) {
    return <FullScreenLoader />
  }
  if (!isAuthenticated) {
    return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />
  }
  if (mustChangePassword && location.pathname !== "/change-password") {
    return <Navigate to="/change-password" replace />
  }
  return <Outlet />
}

export function App() {
  return (
    <TooltipProvider delayDuration={200}>
      <Suspense fallback={<FullScreenLoader />}>
        <Routes>
        <Route path="/login" element={<LoginPage />} />

        <Route element={<RequireAuth />}>
          {/* 强制改密页：不套用侧边栏，避免在受限状态下暴露导航 */}
          <Route path="/change-password" element={<ChangePasswordPage />} />

          <Route element={<AppShell />}>
            <Route path="/" element={<DashboardPage />} />
            <Route path="/tickets" element={<TicketsPage />} />
            <Route path="/stats" element={<StatsPage />} />
            <Route path="/tickets/:no" element={<TicketDetailPage />} />
            <Route path="/account" element={<AccountPage />} />
            <Route path="/types" element={<TicketTypesPage />} />
            <Route path="/emoji-roles" element={<EmojiRolesPage />} />
            <Route path="/roles" element={<RoleMappingPage />} />
            <Route path="/users" element={<UsersPage />} />
            <Route path="/bot" element={<BotStatusPage />} />
            <Route path="/activity" element={<ActivityPage />} />
            <Route path="/settings" element={<SettingsPage />} />
            <Route path="/audit" element={<AuditPage />} />
            <Route path="*" element={<NotFoundPage />} />
          </Route>
          </Route>
        </Routes>
      </Suspense>
    </TooltipProvider>
  )
}
