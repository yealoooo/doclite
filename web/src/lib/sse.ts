// SSE（text/event-stream）帧解析，按 WHATWG HTML 规范的"解析事件流"实现：
// - 行尾可以是 \r\n、\n 或 \r（\r\n 被拆在两块之间也能正确处理）；开头的 BOM 丢掉；
// - `:` 开头的是注释（server 的保活），忽略；
// - 字段 event、data（多行用 \n 连接）、id（含 \0 时忽略）；retry 和未知字段忽略（不自动重连）；
// - 空行派发事件；data 为空的事件不派发；流结束时没以空行收尾的半个事件丢弃。

export interface SseEvent {
  /** event 字段，没写时是 `message`。 */
  event: string
  data: string
  /** 最近一次的 id 字段（没有时为空串）。 */
  id: string
}

export class SseParser {
  private buffer = ''
  private started = false
  /** 上一块以 \r 结尾：如果下一块以 \n 开头，这个 \n 属于同一个换行。 */
  private pendingCR = false
  private dataLines: string[] = []
  private eventType = ''
  private lastId = ''

  /** 喂入一块文本，返回这一块里完整结束的事件。 */
  push(chunk: string): SseEvent[] {
    let text = chunk
    if (!this.started && text.length > 0) {
      this.started = true
      if (text.startsWith('\uFEFF')) text = text.slice(1)
    }
    // 上一块以 \r 结尾：这一块的第一个字符是 \n 的话，它和那个 \r 是同一个换行，丢掉。
    // 看过第一个字符就清掉标记（不管丢完之后还剩不剩内容）；空块什么也没看到，标记保留。
    if (this.pendingCR && text.length > 0) {
      if (text.startsWith('\n')) text = text.slice(1)
      this.pendingCR = false
    }
    this.buffer += text

    const events: SseEvent[] = []
    let lineStart = 0
    for (let i = 0; i < this.buffer.length; i++) {
      const c = this.buffer[i]
      if (c !== '\n' && c !== '\r') continue
      const line = this.buffer.slice(lineStart, i)
      if (c === '\r') {
        if (i + 1 < this.buffer.length) {
          if (this.buffer[i + 1] === '\n') i++
        } else {
          this.pendingCR = true
        }
      }
      lineStart = i + 1
      this.processLine(line, events)
    }
    this.buffer = this.buffer.slice(lineStart)
    return events
  }

  private processLine(line: string, events: SseEvent[]) {
    if (line === '') {
      this.dispatch(events)
      return
    }
    if (line.startsWith(':')) return
    const colon = line.indexOf(':')
    const field = colon === -1 ? line : line.slice(0, colon)
    let value = colon === -1 ? '' : line.slice(colon + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    switch (field) {
      case 'event':
        this.eventType = value
        break
      case 'data':
        this.dataLines.push(value)
        break
      case 'id':
        if (!value.includes('\0')) this.lastId = value
        break
      default:
        break
    }
  }

  private dispatch(events: SseEvent[]) {
    if (this.dataLines.length > 0) {
      events.push({
        event: this.eventType || 'message',
        data: this.dataLines.join('\n'),
        id: this.lastId,
      })
    }
    this.dataLines = []
    this.eventType = ''
  }
}
