/** 危险操作确认对话框：支持可选备注输入（关闭工单时作为通知内容）。 */

import type { ReactNode } from "react"
import { Loader2 } from "lucide-react"
import { useTranslation } from "react-i18next"

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { cn } from "@/lib/utils"

interface ConfirmDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: string
  confirmLabel?: string
  destructive?: boolean
  pending?: boolean
  onConfirm: () => void
  /** 是否展示备注输入框。 */
  withNote?: boolean
  noteLabel?: string
  notePlaceholder?: string
  noteValue?: string
  onNoteChange?: (value: string) => void
  children?: ReactNode
}

export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  destructive,
  pending,
  onConfirm,
  withNote,
  noteLabel,
  notePlaceholder,
  noteValue,
  onNoteChange,
  children,
}: ConfirmDialogProps) {
  const { t } = useTranslation()

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>

        {withNote ? (
          <div className="space-y-2">
            <Label htmlFor="confirm-note">{noteLabel}</Label>
            <Textarea
              id="confirm-note"
              value={noteValue ?? ""}
              placeholder={notePlaceholder}
              maxLength={1000}
              rows={3}
              onChange={(event) => onNoteChange?.(event.target.value)}
            />
          </div>
        ) : null}

        {children}

        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>{t("common.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            disabled={pending}
            className={cn(destructive && "bg-destructive text-white hover:bg-destructive/90")}
            onClick={(event) => {
              // 阻止 Radix 的自动关闭，交由调用方在成功后再关闭。
              event.preventDefault()
              onConfirm()
            }}
          >
            {pending ? <Loader2 className="mr-2 size-4 animate-spin" /> : null}
            {confirmLabel ?? t("common.confirm")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
