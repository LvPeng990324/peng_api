// fetch 封装：统一前缀、{error} 解析、err.status 挂状态码
let onUnauthorized = null

// App 注入 401 处理（回登录页）
export function setUnauthorizedHandler(fn) {
  onUnauthorized = fn
}

export async function api(path, opts = {}) {
  const r = await fetch('/api' + path, {
    method: opts.method || 'GET',
    headers: { 'Content-Type': 'application/json' },
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
  })
  let data = {}
  try { data = await r.json() } catch (e) { /* 空响应 */ }
  if (!r.ok) {
    const err = new Error(data.error || ('HTTP ' + r.status))
    err.status = r.status
    if (r.status === 401 && onUnauthorized) onUnauthorized()
    throw err
  }
  return data
}
