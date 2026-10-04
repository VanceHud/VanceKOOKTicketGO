/** 分页控件：与服务端分页参数（page / pageSize / total）配合使用。 */

import { ChevronLeft, ChevronRight } from "lucide-react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

interface PaginationProps {
  page: number
  pageSize: number
  total: number
  onPageChange: (page: number) => void
  className?: string
}

export function Pagination({ page, pageSize, total, onPageChange, className }: PaginationProps) {
  const { t } = useTranslation()
  const pages = Math.max(1, Math.ceil(total / Math.max(1, pageSize)))

  return (
    <div className={cn("flex flex-col items-center justify-between gap-3 py-3 sm:flex-row", className)}>
      <p className="text-muted-foreground text-xs">
        {t("common.total", { count: total })} · {t("common.page", { page, pages })}
      </p>
      <div className="flex items-center gap-2">
        <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => onPageChange(page - 1)}>
          <ChevronLeft className="size-4" />
          {t("common.prev")}
        </Button>
        <Button variant="outline" size="sm" disabled={page >= pages} onClick={() => onPageChange(page + 1)}>
          {t("common.next")}
          <ChevronRight className="size-4" />
        </Button>
      </div>
    </div>
  )
}
