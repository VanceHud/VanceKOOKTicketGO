/** 合并一批实时事件对应的缓存刷新，避免刷屏时反复取消、重发同一个查询。 */
export function createRealtimeBatch(invalidate: (key: string[]) => void, delay = 250) {
  const pending = new Map<string, string[]>()
  let timer: ReturnType<typeof setTimeout> | undefined

  const flush = () => {
    timer = undefined
    const keys = [...pending.values()]
    pending.clear()
    for (const key of keys) invalidate(key)
  }
  const add = (key: string[]) => pending.set(JSON.stringify(key), key)

  return {
    push(type: string, ticketNo?: string) {
      switch (type) {
        case "ticket.created":
        case "ticket.updated":
          add(["tickets"])
          add(["stats"])
          add(["analytics"])
          if (ticketNo) add(["ticket", ticketNo])
          break
        case "ticket.message":
          add(["tickets"])
          add(["stats"])
          add(["analytics"])
          if (ticketNo) {
            add(["messages", ticketNo])
            add(["ticket", ticketNo])
          }
          break
        case "ticket.note":
          if (ticketNo) add(["notes", ticketNo])
          break
        case "bot.status":
          add(["runtime"])
          break
        case "stats.invalidated":
          add(["stats"])
          add(["analytics"])
          break
      }
      if (pending.size && timer === undefined) timer = setTimeout(flush, delay)
    },
    dispose() {
      if (timer !== undefined) clearTimeout(timer)
      timer = undefined
      pending.clear()
    },
  }
}
