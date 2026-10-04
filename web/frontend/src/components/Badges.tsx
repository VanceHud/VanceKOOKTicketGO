/** 工单状态与账号角色的语义化徽标。 */

import { useTranslation } from "react-i18next"

import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { roleColorClass, statusColorClass } from "@/lib/format"
import type { TicketStatus } from "@/lib/types"

export function StatusBadge({ status, className }: { status: TicketStatus; className?: string }) {
  const { t } = useTranslation()
  return (
    <Badge variant="outline" className={cn("gap-1.5 border font-normal", statusColorClass(status), className)}>
      <span className="size-1.5 shrink-0 rounded-full bg-current" />
      {t(`status.${status}`)}
    </Badge>
  )
}

export function RoleBadge({ role, className }: { role: string; className?: string }) {
  const { t } = useTranslation()
  return (
    <Badge variant="outline" className={cn("border font-normal", roleColorClass(role), className)}>
      {t(`role.${role}`, { defaultValue: role })}
    </Badge>
  )
}
