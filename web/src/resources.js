import { reactive } from 'vue'
import { message } from 'ant-design-vue'
import { api } from './api'
import { loaded, store } from './state'

// 错误反馈与登录态切换是 UI 职责，和加载逻辑分开。
function reportError(e) {
  if (e.status === 401) {
    store.authed = false
    return
  }
  message.error(e.message)
}

// guard 捕获异常做反馈，并把 fn 的结果原样返回，调用方可以感知失败。
export async function guard(fn) {
  try {
    return await fn()
  } catch (e) {
    reportError(e)
    return false
  }
}

const loaders = {
  channels: async () => {
    store.channels = (await api('/channels')).data || []
    await loadChannelQuotas()
  },
  models: async () => { store.models = (await api('/models')).data || [] },
  mappings: async () => { store.mappings = (await api('/mappings')).data || [] },
  tokens: async () => { store.tokens = (await api('/tokens')).data || [] },
}

const pending = {}

async function load(name) {
  await guard(async () => {
    await loaders[name]()
    loaded[name] = true
  })
}

// ensureLoaded 幂等且合并并发请求：App 与各页面同时挂载时只会发一次请求。
export async function ensureLoaded(name) {
  if (loaded[name]) return
  if (!pending[name]) {
    pending[name] = load(name).finally(() => { delete pending[name] })
  }
  return pending[name]
}

export async function loadChannels() { await load('channels') }
export async function loadModels() { await load('models') }
export async function loadMappings() { await load('mappings') }
export async function loadTokens() { await load('tokens') }

export async function loadQuotaTypes() {
  await guard(async () => { store.quotaTypes = (await api('/quota-types')).data || [] })
}

export async function loadInitial() {
  await Promise.all([
    ensureLoaded('channels'), ensureLoaded('models'),
    ensureLoaded('mappings'), ensureLoaded('tokens'), loadQuotaTypes(),
  ])
  // guard 会吞掉异常，用 loaded 标记让 App 知道是否全部就绪。
  return Object.values(loaded).every(Boolean)
}

async function loadChannelQuotas() {
  const channels = store.channels.filter(c => c.quota_type)
  await Promise.all(channels.map(async c => {
    const state = reactive({ quota: null, error: '', loading: true })
    store.quotaState[c.id] = state
    try {
      state.quota = (await api('/channels/' + c.id + '/quota')).data || null
    } catch (e) {
      state.error = e.message
    } finally {
      state.loading = false
    }
  }))
}
