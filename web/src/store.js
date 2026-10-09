import { computed, reactive } from 'vue'
import { message } from 'ant-design-vue'
import { api } from './api'

// 跨页面共享状态：登录态、当前 tab、各资源集合
export const store = reactive({
  authed: false,
  tab: 'channels',
  channels: [],
  models: [],
  mappings: [],
  tokens: [],
})

export const tabs = [
  { key: 'channels', label: '渠道' },
  { key: 'models', label: '模型' },
  { key: 'mappings', label: '模型映射' },
  { key: 'tokens', label: 'Token' },
  { key: 'logs', label: '日志' },
]

export const sortedChannels = computed(
  // 优先级（大在前）→ id（小在前）
  () => [...store.channels].sort((a, b) => b.priority - a.priority || a.id - b.id),
)

export function channelModels(c) {
  return store.models.filter(m => m.channel_id === c.id)
}

export async function guard(fn) {
  try {
    await fn()
  } catch (e) {
    if (e.status === 401) return // 回登录态由 api 的 401 回调处理
    message.error(e.message)
  }
}

export async function loadChannels() {
  await guard(async () => { store.channels = (await api('/channels')).data || [] })
}
export async function loadModels() {
  await guard(async () => { store.models = (await api('/models')).data || [] })
}
export async function loadMappings() {
  await guard(async () => { store.mappings = (await api('/mappings')).data || [] })
}
export async function loadTokens() {
  await guard(async () => { store.tokens = (await api('/tokens')).data || [] })
}

export async function loadAll() {
  const [ch, m, mp, tk] = await Promise.all([
    api('/channels'), api('/models'), api('/mappings'), api('/tokens'),
  ])
  store.channels = ch.data || []
  store.models = m.data || []
  store.mappings = mp.data || []
  store.tokens = tk.data || []
}
