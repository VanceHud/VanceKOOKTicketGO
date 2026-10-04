/**
 * 通用状态视图：加载、错误、空数据。
 *
 * 错误视图会展示 requestId，便于把界面报错与服务端访问日志对应起来。
 */

import type { ReactNode } from "react"
import { AlertTriangle, Inbox, Loader2 } from "lucide-react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { ApiError } from "@/lib/api"
import { cn } from "@/lib/utils"

export function InlineLoader({ className, label }: { className?: string; label?: string }) {
  const { t } = useTranslation()
  return (
    <div className={cn("text-muted-foreground flex items-center justify-center gap-2 py-10 text-sm", className)}>
      <Loader2 className="size-4 animate-spin" />
      {label ?? t("common.loading")}
    </div>
  )
}

export function FullScreenLoader() {
  const { t } = useTranslation()
  return (
    <div className="bg-background flex min-h-svh items-center justify-center">
      <div className="text-muted-foreground flex items-center gap-2 text-sm">
        <Loader2 className="size-4 animate-spin" />
        {t("common.loading")}
      </div>
    </div>
  )
}

export function ErrorState({ error, onRetry, className }: { error: unknown; onRetry?: () => void; className?: string }) {
  const { t } = useTranslation()
  const apiError = error instanceof ApiError ? error : null

  return (
    <Card className={cn("border-destructive/30", className)}>
      <CardContent className="flex flex-col items-center gap-3 py-10 text-center">
        <AlertTriangle className="text-destructive size-6" />
        <div className="space-y-1">
          <p className="text-sm font-medium">{apiError?.message ?? t("common.error")}</p>
          {apiError?.requestId ? (
            <p className="text-muted-foreground text-xs">{t("errors.requestId", { id: apiError.requestId })}</p>
          ) : null}
        </div>
        {onRetry ? (
          <Button variant="outline" size="sm" onClick={onRetry}>
            {t("common.retry")}
          </Button>
        ) : null}
      </CardContent>
    </Card>
  )
}

export function EmptyState({ title, description, icon, className }: { title?: string; description?: string; icon?: ReactNode; className?: string }) {
  const { t } = useTranslation()
  return (
    <div className={cn("text-muted-foreground flex flex-col items-center gap-2 py-12 text-center", className)}>
      {icon ?? <Inbox className="size-6" />}
      <p className="text-sm font-medium">{title ?? t("common.empty")}</p>
      {description ? <p className="text-xs">{description}</p> : null}
    </div>
  )
}
