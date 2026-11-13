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
