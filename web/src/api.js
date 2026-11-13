// fetch 封装：统一前缀、{error} 解析、err.status 挂状态码。
// 401 的登录态切换由 resources.guard 统一处理，这里不持有全局回调。
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
    throw err
  }
  return data
}
