export function fmtTime(s) {
  if (!s) return '-'
  const d = new Date(s)
  return isNaN(d) ? s : d.toLocaleString()
}

// SSE 解析：把流式响应拼成可读内容
export function parseSSE(body) {
  if (!body) return null
  const lines = body.split('\n')
  const dataLines = []
  for (const line of lines) {
    const t = line.trim()
    if (t.startsWith('data:')) {
      const payload = t.slice(5).trim()
      if (payload && payload !== '[DONE]') dataLines.push(payload)
    }
  }
  if (!dataLines.length) return null
  let content = '', reasoning = ''
  let hasDelta = false
  for (const dl of dataLines) {
    try {
      const obj = JSON.parse(dl)
      const choices = obj.choices
      if (!choices || !choices.length) continue
      const delta = choices[0].delta || choices[0].message || {}
      if (delta.content) content += delta.content
      if (delta.reasoning_content) reasoning += delta.reasoning_content
      if (choices[0].delta) hasDelta = true
    } catch (e) { /* 忽略解析失败的行 */ }
  }
  if (!hasDelta && !content && !reasoning) return null
  return { content, reasoning }
}

export function sseText(s) {
  const r = parseSSE(s)
  if (!r) return pretty(s)
  let out = ''
  if (r.reasoning) out += '【思考过程】\n' + r.reasoning + '\n\n'
  if (r.content) out += '【回答内容】\n' + r.content
  return out.trim() || '(空)'
}

export function pretty(s) {
  if (!s) return '(空)'
  try { return JSON.stringify(JSON.parse(s), null, 2) } catch (e) { return s }
}

// 消息内容可能是纯文本，也可能是多模态部分数组（[{"type":"text","text":"…"}]）；
// 非文本部分只保留类型占位，避免把 base64 图片之类的巨量内容铺进详情。
function contentText(c) {
  if (c == null) return ''
  if (typeof c === 'string') return c
  if (Array.isArray(c)) {
    return c.map((p) => {
      if (typeof p === 'string') return p
      if (!p || typeof p !== 'object') return ''
      if (typeof p.text === 'string') return p.text
      return '[' + (p.type || '非文本内容') + ']'
    }).filter(Boolean).join('\n')
  }
  if (typeof c === 'object') return typeof c.text === 'string' ? c.text : JSON.stringify(c)
  return String(c)
}

// 请求体解析：只抽 user 轮的内容，跳过 system 提示词、tools 定义与工具结果，
// 首行给出 messages 的角色分布，方便判断这次请求到底带了什么。
export function requestText(s) {
  if (!s) return '(空)'
  let body
  try { body = JSON.parse(s) } catch (e) { return s }
  const messages = Array.isArray(body && body.messages) ? body.messages : null
  if (!messages) return pretty(s)

  const counts = {}
  for (const m of messages) {
    const role = (m && m.role) || 'unknown'
    counts[role] = (counts[role] || 0) + 1
  }
  let head = body.model ? 'model ' + body.model + ' · ' : ''
  head += 'messages ' + messages.length + ' 条：' +
    Object.keys(counts).map((r) => r + ' ' + counts[r]).join(' / ')

  const users = messages.filter((m) => m && m.role === 'user')
  if (!users.length) return head + '\n\n没有 user 消息，可点「查看原文」看完整请求体'
  const blocks = users.map((m, i) => {
    const text = contentText(m.content).trim()
    return `【用户消息 ${i + 1}/${users.length}】\n` + (text || '(空)')
  })
  return head + '\n\n' + blocks.join('\n\n')
}

// ---- 日志展示格式化：列表与详情共用同一套实现 ----

export function fmtDur(ms) {
  if (ms == null) return '-'
  if (ms >= 1000) {
    const s = ms / 1000
    const r = s >= 100 ? Math.round(s) : Math.round(s * 10) / 10
    return r + 's'
  }
  return ms + 'ms'
}

// 速度 = 输出 tokens / 总耗时(s)
export function tokSpeed(l) {
  if (l.completion_tokens == null || !l.latency_ms) return ''
  const tps = Math.round(l.completion_tokens / (l.latency_ms / 1000))
  return tps > 0 ? tps + 't/s' : ''
}

// 输入行：命中缓存+未命中=总输入；无缓存数据时只显示总输入
export function fmtPromptLine(l) {
  const p = l.prompt_tokens
  const hit = l.prompt_cache_hit_tokens
  if (hit != null && hit > 0 && p != null) return `${hit}+${p - hit}=${p}`
  return String(p ?? '-')
}

// 缓存命中率 = 命中缓存 token / 输入总 token * 100%
export function cacheRate(l) {
  const p = l.prompt_tokens
  const hit = l.prompt_cache_hit_tokens
  if (hit == null || hit <= 0 || !p) return ''
  return ((hit / p) * 100).toFixed(2)
}

// 详情元信息里的一行式 token 摘要
export function fmtTokens(l) {
  let s = String(l.prompt_tokens)
  if (l.prompt_cache_hit_tokens) s += `(${l.prompt_cache_hit_tokens})`
  return s + '+' + l.completion_tokens
}

// ---- 日志状态与进行中计时：列表与详情共用 ----

export function statusLabel(s) {
  return s === 'processing' ? '处理中' : s
}

export function statusTagColor(s) {
  if (s === 'success') return 'success'
  if (s === 'processing') return 'processing'
  return 'error'
}

export function statusTextColor(s) {
  if (s === 'success') return '#52c41a'
  if (s === 'processing') return '#1677ff'
  return '#f87171'
}

// processing 行没有 latency_ms，用请求开始时间在前端本地计时
export function fmtElapsed(createdAt, now = Date.now()) {
  const t = new Date(createdAt).getTime()
  if (!Number.isFinite(t)) return '-'
  return fmtDur(Math.max(0, now - t))
}

// 进行中流式行的近似输出 token（按 SSE chunk 累计，不是精确 usage），标注「约」避免误读
export function liveOutput(l) {
  if (l.status !== 'processing' || l.live_completion_tokens == null) return ''
  return '约 ' + l.live_completion_tokens
}

// 日志行的表格 key：进行中的行没有自增 id，用 live_key
export function logRowKey(l) {
  return l.live_key || l.id
}
