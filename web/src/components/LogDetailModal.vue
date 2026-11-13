<template>
  <a-modal v-model:open="visible" width="860px" :footer="null">
    <template #title>
      请求详情 <span class="dim mono">{{ state.data?.request_id }}</span>
    </template>
    <a-spin :spinning="state.loading" style="display: block; min-height: 160px">
      <a-radio-group v-model:value="state.sel" style="margin-bottom: 10px">
        <a-radio-button v-for="a in state.attempts" :key="a.id" :value="a.id">
          第{{ a.attempt }}次 {{ a.channel_name }}
          <span :style="{ color: statusTextColor(a.status) }">{{ statusLabel(a.status) }}</span>
        </a-radio-button>
      </a-radio-group>
      <template v-if="selAttempt">
        <p class="logs-detail-meta">
          HTTP {{ selAttempt.http_status ?? '-' }} · <template v-if="selAttempt.status === 'processing'">进行中</template><template v-else>耗时 {{ fmtDur(selAttempt.latency_ms) }}</template><template v-if="selAttempt.first_byte_ms > 0"> · 首字 {{ fmtDur(selAttempt.first_byte_ms) }}</template> ·
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
          <template v-if="selAttempt.status !== 'processing'">
            <a class="td-link" @click="state.viewRaw = !state.viewRaw">{{ state.viewRaw ? '查看拼接' : '查看原文' }}</a>
            <a class="td-link" @click="copyText(state.viewRaw ? pretty(selAttempt.response_body) : sseText(selAttempt.response_body))">复制</a>
          </template>
        </h4>
        <pre v-if="selAttempt.status === 'processing'">请求处理中，暂无响应体</pre>
        <pre v-else>{{ state.viewRaw ? pretty(selAttempt.response_body) : sseText(selAttempt.response_body) }}</pre>
      </template>
    </a-spin>
  </a-modal>
</template>

<script setup>
import { computed, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import { api } from '../api'
import { guard } from '../store'
import { fmtDur, fmtTime, fmtTokens, pretty, sseText, statusLabel, statusTextColor, tokSpeed } from '../utils'

const visible = ref(false)
const state = reactive({ data: null, attempts: [], sel: null, viewRaw: false, loading: false })

const selAttempt = computed(() => {
  if (!state.data) return null
  return state.attempts.find(a => a.id === state.sel) || state.data
})

async function open(log) {
  state.data = null
  state.attempts = []
  state.sel = null
  state.loading = true
  visible.value = true
  await guard(async () => {
    const v = await api('/logs/' + log.id)
    state.data = v.data
    state.attempts = v.attempts || [v.data]
    state.sel = v.data.id
    state.viewRaw = false
  })
  state.loading = false
  if (!state.data) visible.value = false // 失败已由 guard 提示
}

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text)
    message.success('已复制到剪贴板')
  } catch (e) {
    message.error('复制失败：' + e.message)
  }
}

defineExpose({ open })
</script>
