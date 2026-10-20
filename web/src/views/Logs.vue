<template>
<div class="page-card">
  <a-form layout="inline" style="margin-bottom: 14px; row-gap: 10px" @submit.prevent>
    <a-form-item><a-input v-model:value="logFilter.model" placeholder="模型（模糊）" allow-clear /></a-form-item>
    <a-form-item><a-input v-model:value="logFilter.agent" placeholder="Agent（模糊）" allow-clear /></a-form-item>
    <a-form-item>
      <a-select v-model:value="logFilter.status" placeholder="全部状态" allow-clear style="width: 120px">
        <a-select-option value="success">success</a-select-option>
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
      <a-button type="primary" html-type="submit" @click="searchLogs">查询</a-button>
    </a-form-item>
  </a-form>

  <a-table :scroll="{ x: 'max-content' }" :data-source="logs.data" :columns="columns" row-key="id" :pagination="false" size="middle">
    <template #bodyCell="{ column, record: l }">
      <template v-if="column.key === 'created_at'">{{ fmtTime(l.created_at) }}</template>
      <template v-else-if="column.key === 'model'">
        <span class="mono model-cell" :title="l.model_requested + (l.model_canonical && l.model_canonical !== l.model_requested ? ' → ' + l.model_canonical : '')">
          {{ l.model_requested }}<template v-if="l.model_canonical && l.model_canonical !== l.model_requested"> → {{ l.model_canonical }}</template>
        </span>
      </template>
      <template v-else-if="column.key === 'channel'">
        {{ l.channel_name || '-' }}<span class="dim">#{{ l.attempt }}</span>
      </template>
      <template v-else-if="column.key === 'token_name'">{{ l.token_name || '-' }}</template>
      <template v-else-if="column.key === 'agent'">{{ l.agent || '-' }}</template>
      <template v-else-if="column.key === 'status'">
        <a-tag :color="l.status === 'success' ? 'success' : 'error'">{{ l.status }}</a-tag>
      </template>
      <template v-else-if="column.key === 'http_status'">{{ l.http_status ?? '-' }}</template>
      <template v-else-if="column.key === 'latency_ms'">
        <div class="latency-cell">
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
        <a @click="openLogDetail(l)">详情</a>
      </template>
    </template>
  </a-table>

  <a-pagination
    v-model:current="logs.page"
    :total="logs.total"
    :page-size="logs.size"
    show-less-items
    style="margin-top: 14px; text-align: right"
    @change="loadLogs"
  />

  <!-- 详情弹窗 -->
  <a-modal v-model:open="logDetailOpen" width="860px" :footer="null">
    <template #title>
      请求详情 <span class="dim mono">{{ logDetail.data.request_id }}</span>
    </template>
    <a-radio-group v-model:value="logDetail.sel" style="margin-bottom: 10px">
      <a-radio-button v-for="a in logDetail.attempts" :key="a.id" :value="a.id">
        第{{ a.attempt }}次 {{ a.channel_name }}
        <span :style="{ color: a.status === 'success' ? '#52c41a' : '#f87171' }">{{ a.status }}</span>
      </a-radio-button>
    </a-radio-group>
    <template v-if="selAttempt">
      <p class="logs-detail-meta">
        HTTP {{ selAttempt.http_status ?? '-' }} · 耗时 {{ fmtDur(selAttempt.latency_ms) }}<template v-if="selAttempt.first_byte_ms > 0"> · 首字 {{ fmtDur(selAttempt.first_byte_ms) }}</template> ·
        {{ selAttempt.stream ? '流式' : '非流式' }} · {{ fmtTime(selAttempt.created_at) }}
        · agent {{ selAttempt.agent || '-' }}
        <template v-if="selAttempt.prompt_tokens != null"> · tokens {{ fmtTokens(selAttempt) }}</template>
        <template v-if="tokSpeed(selAttempt)"> · 速度 {{ tokSpeed(selAttempt) }}</template>
      </p>
      <p v-if="selAttempt.error" class="err">{{ selAttempt.error }}</p>
      <h4>请求体 <a class="td-link" @click="copyText(pretty(selAttempt.request_body))">复制</a></h4>
      <pre>{{ pretty(selAttempt.request_body) }}</pre>
      <h4>
        响应体
        <a class="td-link" @click="logDetail.viewRaw = !logDetail.viewRaw">{{ logDetail.viewRaw ? '查看拼接' : '查看原文' }}</a>
        <a class="td-link" @click="copyText(logDetail.viewRaw ? pretty(selAttempt.response_body) : sseText(selAttempt.response_body))">复制</a>
      </h4>
      <pre>{{ logDetail.viewRaw ? pretty(selAttempt.response_body) : sseText(selAttempt.response_body) }}</pre>
    </template>
  </a-modal>
</div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import { api } from '../api'
import { guard, store } from '../store'
import { fmtTime, fmtTokens, pretty, sseText } from '../utils'

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

async function loadLogs() {
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
}

function searchLogs() {
  logs.page = 1
  loadLogs()
}

// ---- 耗时/速度 ----
function fmtDur(ms) {
  if (ms == null) return '-'
  if (ms >= 1000) {
    const s = ms / 1000
    const r = s >= 100 ? Math.round(s) : Math.round(s * 10) / 10
    return r + 's'
  }
  return ms + 'ms'
}

// 速度 = 输出 tokens / 总耗时(s)
function tokSpeed(l) {
  if (l.completion_tokens == null || !l.latency_ms) return ''
  const tps = Math.round(l.completion_tokens / (l.latency_ms / 1000))
  return tps > 0 ? tps + 't/s' : ''
}

// 输入行：命中缓存+未命中=总输入；无缓存数据时只显示总输入
function fmtPromptLine(l) {
  const p = l.prompt_tokens
  const hit = l.prompt_cache_hit_tokens
  if (hit != null && hit > 0 && p != null) return `${hit}+${p - hit}=${p}`
  return String(p ?? '-')
}

// 缓存命中率 = 命中缓存 token / 输入总 token * 100%，保留两位小数
function cacheRate(l) {
  const p = l.prompt_tokens
  const hit = l.prompt_cache_hit_tokens
  if (hit == null || hit <= 0 || !p) return ''
  return ((hit / p) * 100).toFixed(2)
}

// ---- 详情 ----
const logDetailOpen = ref(false)
const logDetail = reactive({ data: null, attempts: [], sel: null, viewRaw: false })

const selAttempt = computed(() => {
  if (!logDetail.data) return null
  return logDetail.attempts.find(a => a.id === logDetail.sel) || logDetail.data
})

async function openLogDetail(l) {
  await guard(async () => {
    const v = await api('/logs/' + l.id)
    logDetail.data = v.data
    logDetail.attempts = v.attempts || [v.data]
    logDetail.sel = v.data.id
    logDetail.viewRaw = false
    logDetailOpen.value = true
  })
}

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text)
    message.success('已复制到剪贴板')
  } catch (e) {
    message.error('复制失败：' + e.message)
  }
}

onMounted(loadLogs)
</script>

<style scoped>
.latency-cell,
.tokens-cell {
  font-size: 12px;
  line-height: 1.4;
  white-space: nowrap;
}
</style>
