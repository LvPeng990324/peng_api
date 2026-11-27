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
          <template v-if="liveOutput(selAttempt)"> · 输出 {{ liveOutput(selAttempt) }}</template>
          <template v-if="tokSpeed(selAttempt)"> · 速度 {{ tokSpeed(selAttempt) }}</template>
        </p>
        <p v-if="selAttempt.error" class="err">{{ selAttempt.error }}</p>
        <h4>
          请求体
          <span class="detail-actions">
            <a class="detail-act detail-act-view" @click="state.viewReqRaw = !state.viewReqRaw">{{ state.viewReqRaw ? '只看用户消息' : '查看原文' }}</a>
            <a class="detail-act detail-act-copy" @click="copyText(state.viewReqRaw ? pretty(selAttempt.request_body) : requestText(selAttempt.request_body))">复制</a>
          </span>
        </h4>
        <pre>{{ state.viewReqRaw ? pretty(selAttempt.request_body) : requestText(selAttempt.request_body) }}</pre>
        <h4>
          响应体
          <span v-if="selAttempt.status !== 'processing'" class="detail-actions">
            <a class="detail-act detail-act-view" @click="state.viewRaw = !state.viewRaw">{{ state.viewRaw ? '查看拼接' : '查看原文' }}</a>
            <a class="detail-act detail-act-copy" @click="copyText(state.viewRaw ? pretty(selAttempt.response_body) : sseText(selAttempt.response_body))">复制</a>
          </span>
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
import { fmtDur, fmtTime, fmtTokens, liveOutput, pretty, requestText, sseText, statusLabel, statusTextColor, tokSpeed } from '../utils'

const visible = ref(false)
const state = reactive({ data: null, attempts: [], sel: null, viewRaw: false, viewReqRaw: false, loading: false })

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
    // 进行中的行还没有自增 id，按 live_key 取内存快照
    const v = log.live_key
      ? await api('/logs/live/' + encodeURIComponent(log.live_key))
      : await api('/logs/' + log.id)
    state.data = v.data
    state.attempts = v.attempts || [v.data]
    state.sel = v.data.id ?? 0
    state.viewRaw = false
    state.viewReqRaw = false
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

<style scoped>
/* h4 后面的两个操作做成小按钮，用颜色区分「切换视图」和「复制」 */
.detail-actions {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  margin-left: 10px;
  vertical-align: middle;
}
.detail-act {
  display: inline-flex;
  align-items: center;
  height: 24px;
  padding: 0 10px;
  border: 1px solid transparent;
  border-radius: 4px;
  font-size: 12px;
  line-height: 1;
  font-weight: 400;
  cursor: pointer;
  transition: background .15s, border-color .15s;
}
.detail-act-view {
  color: #93c5fd;
  background: rgba(59, 130, 246, .12);
  border-color: rgba(59, 130, 246, .4);
}
.detail-act-view:hover {
  color: #bfdbfe;
  background: rgba(59, 130, 246, .22);
  border-color: rgba(59, 130, 246, .65);
}
.detail-act-copy {
  color: #6ee7b7;
  background: rgba(16, 185, 129, .12);
  border-color: rgba(52, 211, 153, .4);
}
.detail-act-copy:hover {
  color: #a7f3d0;
  background: rgba(16, 185, 129, .22);
  border-color: rgba(52, 211, 153, .65);
}
</style>
