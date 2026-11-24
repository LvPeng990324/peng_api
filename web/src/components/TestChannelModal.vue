<template>
  <a-modal
    v-model:open="open"
    :title="'连通性测试：' + state.channel"
    width="760px"
    ok-text="开始测试"
    cancel-text="关闭"
    :confirm-loading="state.loading"
    :ok-button-props="{ disabled: !form.model || !form.userPrompt.trim() }"
    @ok="run"
  >
    <a-alert
      v-if="!modelOptions.length"
      type="warning"
      show-icon
      message="该渠道下还没有模型实体，先用「获取模型」添加再测试"
      style="margin-bottom: 16px"
    />
    <a-form layout="vertical">
      <a-form-item label="测试模型">
        <a-select
          v-model:value="form.model"
          :options="modelOptions"
          placeholder="-- 选择本渠道下的模型 --"
          show-search
        />
      </a-form-item>
      <a-form-item label="系统提示词（留空则不发送 system 消息）">
        <a-textarea v-model:value="form.systemPrompt" :rows="2" />
      </a-form-item>
      <a-form-item label="用户提示词">
        <a-textarea v-model:value="form.userPrompt" :rows="2" />
      </a-form-item>
    </a-form>

    <template v-if="state.result">
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
  </a-modal>
</template>

<script setup>
import { computed, reactive, ref } from 'vue'
import { api } from '../api'
import { guard, store } from '../store'
import { pretty } from '../utils'

// 预设的简短测试内容：一条要求简短回答的系统指令 + 一个极短的提问，够验证链路又几乎不花钱。
const DEFAULT_SYSTEM_PROMPT = '你是一个连通性测试助手，请用一句话简短作答。'
const DEFAULT_USER_PROMPT = '你好，请回复 pong。'

const open = ref(false)
const state = reactive({ loading: false, channelId: 0, channel: '', result: null })
// 提示词跨次打开保留（方便换渠道复测），模型每次按当前渠道重选
const form = reactive({ model: '', systemPrompt: DEFAULT_SYSTEM_PROMPT, userPrompt: DEFAULT_USER_PROMPT })

const modelOptions = computed(() =>
  store.models
    .filter(m => m.channel_id === state.channelId)
    .map(m => ({ value: m.name, label: m.name })),
)

function show(c) {
  state.channelId = c.id
  state.channel = c.name
  state.result = null
  state.loading = false
  form.model = modelOptions.value.length ? modelOptions.value[0].value : ''
  open.value = true
}

async function run() {
  state.loading = true
  state.result = null
  try {
    const v = await guard(() => api('/channels/' + state.channelId + '/test', {
      method: 'POST',
      body: { model: form.model, system_prompt: form.systemPrompt, user_prompt: form.userPrompt },
    }))
    if (v) state.result = v // 失败已由 guard 提示，弹窗保持打开便于改参数重试
  } finally {
    state.loading = false
  }
}

defineExpose({ show })
</script>
