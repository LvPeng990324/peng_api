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

export function fmtTokens(l) {
  let s = String(l.prompt_tokens)
  if (l.prompt_cache_hit_tokens) s += `(${l.prompt_cache_hit_tokens})`
  return s + '+' + l.completion_tokens
}
