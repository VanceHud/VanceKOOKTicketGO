/**
 * 统一的提示与错误呈现。
 *
 * 原则：错误信息优先使用服务端返回的 message（已是面向用户的中文/英文文案），
 * 并附带 requestId 便于与访问日志对照排查；服务端不会返回内部堆栈。
 */

import { toast } from "sonner"

import { ApiError } from "@/lib/api"
import i18n from "@/lib/i18n"

export function toastError(error: unknown) {
  const t = i18n.t
  if (error instanceof ApiError) {
    if (error.code === "unauthenticated") {
      toast.error(t("errors.unauthorized"))
      return
    }
    if (error.code === "forbidden") {
      toast.error(t("errors.forbidden"))
      return
    }
    const requestId = error.requestId ? `（${t("errors.requestId", { id: error.requestId })}）` : ""
    toast.error(`${error.message}${requestId}`)
    return
  }
  if (error instanceof TypeError) {
    toast.error(t("errors.network"))
    return
  }
  toast.error(t("errors.unknown"))
}

export function toastSuccess(message: string) {
  toast.success(message)
}
