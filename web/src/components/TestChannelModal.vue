<template>
  <a-modal v-model:open="open" :title="'连通性测试：' + state.channel" width="720px" :footer="null">
    <a-spin :spinning="state.loading">
      <template v-if="!state.loading && state.result">
        <p>
          <a-tag :color="state.result.success ? 'success' : 'error'">
            {{ state.result.success ? '成功' : '失败' }}
          </a-tag>
          耗时 {{ state.result.latency_ms }}ms
          <template v-if="state.result.http_status"> · HTTP {{ state.result.http_status }}</template>
        </p>
        <p v-if="state.result.error" class="err">{{ state.result.error }}</p>
        <h4>请求</h4>
        <pre>{{ pretty(state.result.request_body) }}</pre>
        <h4>响应</h4>
        <pre>{{ pretty(state.result.response_body || '') }}</pre>
      </template>
    </a-spin>
  </a-modal>
</template>

<script setup>
import { reactive, ref } from 'vue'
import { api } from '../api'
import { guard } from '../store'
import { pretty } from '../utils'

const open = ref(false)
const state = reactive({ loading: false, channel: '', result: null })

async function show(c) {
  state.loading = true
  state.channel = c.name
  state.result = null
  open.value = true
  await guard(async () => {
    const v = await api('/channels/' + c.id + '/test', { method: 'POST' })
    state.loading = false
    state.result = v
  })
  if (state.loading) open.value = false // 失败已由 guard 提示
}

defineExpose({ show })
</script>
