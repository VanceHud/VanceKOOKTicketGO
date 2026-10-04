/**
 * SSE 实时事件订阅。
 *
 * 后端在 /api/v1/events 推送工单与统计事件；这里只做一件简单的事：
 * 按事件类型让 TanStack Query 的对应缓存失效，从而自动重新拉取最新数据。
 * 断线由 EventSource 自动重连，页面无需手动刷新。
 */

import { useEffect, useState } from "react"
import { useQueryClient } from "@tanstack/react-query"

/** 后端推送的事件类型（与 internal/eventbus 保持一致）。 */
const EVENT_TYPES = [
  "ticket.created",
  "ticket.updated",
  "ticket.message",
  "ticket.note",
  "stats.invalidated",
  "bot.status",
] as const

interface EventPayload {
  type?: string
  ticketNo?: string
}

export function useRealtimeEvents(enabled: boolean): boolean {
  const queryClient = useQueryClient()
  const [connected, setConnected] = useState(false)

  useEffect(() => {
    if (!enabled) {
      setConnected(false)
      return
    }

    const source = new EventSource("/api/v1/events", { withCredentials: true })

    const onOpen = () => setConnected(true)
    const onError = () => setConnected(false)
    source.addEventListener("open", onOpen)
    source.addEventListener("error", onError)

    const invalidate = (event: Event) => {
      const message = event as MessageEvent<string>
      let payload: EventPayload = {}
      try {
        payload = JSON.parse(message.data) as EventPayload
      } catch {
        payload = {}
      }
      const type = payload.type ?? message.type

      switch (type) {
        case "ticket.created":
        case "ticket.updated":
          void queryClient.invalidateQueries({ queryKey: ["tickets"] })
          void queryClient.invalidateQueries({ queryKey: ["stats"] })
          if (payload.ticketNo) {
            void queryClient.invalidateQueries({ queryKey: ["ticket", payload.ticketNo] })
          }
          break
        case "ticket.message":
          void queryClient.invalidateQueries({ queryKey: ["tickets"] })
          if (payload.ticketNo) {
            void queryClient.invalidateQueries({ queryKey: ["messages", payload.ticketNo] })
            void queryClient.invalidateQueries({ queryKey: ["ticket", payload.ticketNo] })
          }
          break
        case "ticket.note":
          if (payload.ticketNo) {
            void queryClient.invalidateQueries({ queryKey: ["notes", payload.ticketNo] })
          }
          break
        default:
          void queryClient.invalidateQueries({ queryKey: ["stats"] })
      }
    }

    for (const type of EVENT_TYPES) {
      source.addEventListener(type, invalidate)
    }

    return () => {
      for (const type of EVENT_TYPES) {
        source.removeEventListener(type, invalidate)
      }
      source.removeEventListener("open", onOpen)
      source.removeEventListener("error", onError)
      source.close()
      setConnected(false)
    }
  }, [enabled, queryClient])

  return connected
}
