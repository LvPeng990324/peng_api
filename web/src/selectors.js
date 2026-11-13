import { computed } from 'vue'
import { store } from './state'

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

export function quotaFor(c) {
  return store.quotaState[c.id] || {}
}
