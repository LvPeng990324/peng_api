import { reactive } from 'vue'

// 全局共享状态：按用途分成「服务端数据」「额度 UI 状态」「会话/导航」「加载标记」，
// 不再把加载状态挂到服务端返回的对象上。
export const store = reactive({
  authed: false,
  tab: 'channels',
  channels: [],
  models: [],
  mappings: [],
  tokens: [],
  quotaTypes: [],
  quotaState: {},
})

export const loaded = reactive({
  channels: false,
  models: false,
  mappings: false,
  tokens: false,
})
