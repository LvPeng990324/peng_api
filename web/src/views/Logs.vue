<template>
<div class="page-card">
  <a-form layout="inline" style="margin-bottom: 14px; row-gap: 10px" @submit.prevent>
    <a-form-item><a-input v-model:value="logFilter.model" placeholder="模型（模糊）" allow-clear /></a-form-item>
    <a-form-item><a-input v-model:value="logFilter.agent" placeholder="Agent（模糊）" allow-clear /></a-form-item>
    <a-form-item>
      <a-select v-model:value="logFilter.status" placeholder="全部状态" allow-clear style="width: 120px">
        <a-select-option value="success">success</a-select-option>
        <a-select-option value="processing">processing</a-select-option>
        <a-select-option value="failed">failed</a-select-option>
      </a-select>
    </a-form-item>
    <a-form-item>
      <a-select v-model:value="logFilter.token_id" placeholder="全部 token" allow-clear style="width: 140px">
        <a-select-option v-for="t in store.tokens" :key="t.id" :value="String(t.id)">{{ t.name }}</a-select-option>
      </a-select>
    </a-form-item>
    <a-form-item>
      <a-select v-model:value="logFilter.channel_id" placeholder="全部渠道" allow-clear style="width: 140px">
        <a-select-option v-for="c in store.channels" :key="c.id" :value="String(c.id)">{{ c.name }}</a-select-option>
      </a-select>
    </a-form-item>
    <a-form-item>
      <a-date-picker v-model:value="logFilter.start_time" show-time placeholder="开始时间" style="width: 190px" />
    </a-form-item>
    <a-form-item>
      <a-date-picker v-model:value="logFilter.end_time" show-time placeholder="结束时间" style="width: 190px" />
    </a-form-item>
    <a-form-item>
      <a-button type="primary" html-type="submit" :loading="logsLoading" @click="searchLogs">查询</a-button>
    </a-form-item>
    <a-form-item>
      <a-checkbox v-model:checked="autoRefresh" @change="onAutoRefreshChange">自动刷新（仅首页）</a-checkbox>
    </a-form-item>
  </a-form>

  <a-table :scroll="{ x: 'max-content' }" :data-source="logs.data" :columns="columns" row-key="id" :pagination="false" size="middle" :loading="logsLoading">
    <template #bodyCell="{ column, record: l }">
      <template v-if="column.key === 'created_at'">{{ fmtTime(l.created_at) }}</template>
      <template v-else-if="column.key === 'model'">
        <span class="mono model-cell" :title="l.model_requested + (l.model_canonical && l.model_canonical !== l.model_requested ? ' → ' + l.model_canonical : '')">
          {{ l.model_requested }}<template v-if="l.model_canonical && l.model_canonical !== l.model_requested"> → {{ l.model_canonical }}</template>
        </span>
        <div v-if="l.model_actual" class="ch-remark mono" :title="l.model_actual">{{ l.model_actual }}</div>
      </template>
      <template v-else-if="column.key === 'channel'">
        {{ l.channel_name || '-' }}<span class="dim">#{{ l.attempt }}</span>
      </template>
      <template v-else-if="column.key === 'token_name'">{{ l.token_name || '-' }}</template>
      <template v-else-if="column.key === 'agent'">{{ l.agent || '-' }}</template>
      <template v-else-if="column.key === 'status'">
        <a-tag :color="statusTagColor(l.status)">{{ statusLabel(l.status) }}</a-tag>
      </template>
      <template v-else-if="column.key === 'http_status'">{{ l.http_status ?? '-' }}</template>
      <template v-else-if="column.key === 'latency_ms'">
        <div v-if="l.status === 'processing'" class="latency-cell">
          <div>已运行 {{ fmtElapsed(l.created_at, now) }}</div>
        </div>
        <div v-else class="latency-cell">
          <div v-if="l.first_byte_ms > 0">首字 {{ fmtDur(l.first_byte_ms) }}</div>
          <div>耗时 {{ fmtDur(l.latency_ms) }}</div>
          <div v-if="tokSpeed(l)">速度 {{ tokSpeed(l) }}</div>
        </div>
      </template>
      <template v-else-if="column.key === 'tokens'">
        <div v-if="l.prompt_tokens != null" class="tokens-cell">
          <div>输入 {{ fmtPromptLine(l) }}</div>
          <div v-if="cacheRate(l)">命中 {{ cacheRate(l) }}%</div>
          <div>输出 {{ l.completion_tokens ?? '-' }}</div>
        </div>
        <template v-else>-</template>
      </template>
      <template v-else-if="column.key === 'ops'">
        <a @click="detailModal.open(l)">详情</a>
      </template>
    </template>
  </a-table>

  <a-pagination
    v-model:current="logs.page"
    :total="logs.total"
    :page-size="logs.size"
    show-less-items
    style="margin-top: 14px; text-align: right"
    @change="onPageChange"
  />

  <LogDetailModal ref="detailModal" />
</div>
</template>

<script setup>
import { onMounted, onUnmounted, reactive, ref } from 'vue'
import { api } from '../api'
import { guard, store } from '../store'
import {
  cacheRate, fmtDur, fmtElapsed, fmtPromptLine, fmtTime,
  statusLabel, statusTagColor, tokSpeed,
} from '../utils'
import LogDetailModal from '../components/LogDetailModal.vue'

const refreshInterval = 3000

const columns = [
  { title: '时间', key: 'created_at', width: 170 },
  { title: '模型', key: 'model', width: 220 },
  { title: '渠道', key: 'channel', width: 130 },
  { title: 'Token', key: 'token_name', width: 100 },
  { title: 'Agent', key: 'agent', width: 110 },
  { title: '状态', key: 'status', width: 90 },
  { title: 'HTTP', key: 'http_status', width: 70 },
  { title: '耗时', key: 'latency_ms', width: 90 },
  { title: 'tokens', key: 'tokens', width: 150 },
  { title: '操作', key: 'ops', width: 70 },
]

const logs = reactive({ data: [], total: 0, page: 1, size: 20 })
const logFilter = reactive({ model: '', status: '', agent: '', token_id: '', channel_id: '', start_time: null, end_time: null })
const logsLoading = ref(false)
const autoRefresh = ref(true)
const now = ref(Date.now())
const detailModal = ref(null)

let pollTimer = null
let clockTimer = null
let inFlight = false

// silent 刷新不置 loading，避免轮询时整表闪烁；inFlight 防止请求叠加
async function loadLogs({ silent = false } = {}) {
  if (inFlight) return
  inFlight = true
  if (!silent) logsLoading.value = true
  try {
    await guard(async () => {
      const q = new URLSearchParams({ page: logs.page, size: logs.size })
      if (logFilter.model) q.set('model', logFilter.model)
      if (logFilter.status) q.set('status', logFilter.status)
      if (logFilter.agent) q.set('agent', logFilter.agent)
      if (logFilter.token_id) q.set('token_id', logFilter.token_id)
      if (logFilter.channel_id) q.set('channel_id', logFilter.channel_id)
      if (logFilter.start_time) q.set('start_time', logFilter.start_time.toISOString())
      if (logFilter.end_time) q.set('end_time', logFilter.end_time.toISOString())
      const v = await api('/logs?' + q)
      logs.data = v.data || []
      logs.total = v.total
    })
  } finally {
    inFlight = false
    if (!silent) logsLoading.value = false
  }
}

function searchLogs() {
  logs.page = 1
  loadLogs()
}

function onPageChange() {
  loadLogs()
}

function onAutoRefreshChange() {
  if (autoRefresh.value && logs.page === 1) loadLogs({ silent: true })
}

function onVisibilityChange() {
  if (document.hidden) return
  now.value = Date.now()
  if (autoRefresh.value && logs.page === 1) loadLogs({ silent: true })
}

function startPolling() {
  stopPolling()
  pollTimer = setInterval(() => {
    if (!autoRefresh.value || document.hidden || logs.page !== 1) return
    loadLogs({ silent: true })
  }, refreshInterval)
  // processing 行没有 latency_ms，靠本地时钟推进「已运行」
  clockTimer = setInterval(() => { now.value = Date.now() }, 1000)
}

function stopPolling() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null }
  if (clockTimer) { clearInterval(clockTimer); clockTimer = null }
}

onMounted(() => {
  loadLogs()
  startPolling()
  document.addEventListener('visibilitychange', onVisibilityChange)
})
onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibilityChange)
})
</script>

<style scoped>
.latency-cell,
.tokens-cell {
  font-size: 12px;
  line-height: 1.4;
  white-space: nowrap;
}
</style>
