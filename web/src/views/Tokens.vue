<template>
<div class="page-card">
  <div class="page-bar">
    <a-space>
      <a-input v-model:value="newTokenName" placeholder="名称（如 cline）" style="width: 200px" @keyup.enter="createToken" />
      <a-button type="primary" @click="createToken">新建 Token</a-button>
    </a-space>
    <span />
  </div>

  <a-table :scroll="{ x: 'max-content' }" :data-source="store.tokens" :columns="columns" row-key="id" :pagination="false">
    <template #bodyCell="{ column, record: t }">
      <template v-if="column.key === 'token'">
        <a class="td-link mono" title="点击复制完整 Token" @click="revealToken(t)">{{ t.token }}</a>
      </template>
      <template v-else-if="column.key === 'enabled'">
        <a-tag :color="t.enabled ? 'success' : 'default'">{{ t.enabled ? '启用' : '禁用' }}</a-tag>
      </template>
      <template v-else-if="column.key === 'created_at'">{{ fmtTime(t.created_at) }}</template>
      <template v-else-if="column.key === 'ops'">
        <a-space>
          <a @click="toggleToken(t)">{{ t.enabled ? '禁用' : '启用' }}</a>
          <a class="err" @click="delToken(t)">删除</a>
        </a-space>
      </template>
    </template>
  </a-table>

  <!-- 新 token 弹窗 -->
  <a-modal v-model:open="createdTokenOpen" title="Token 已创建" :footer="null">
    <p class="err">这是唯一一次完整显示，请立即复制保存：</p>
    <pre class="token-full">{{ createdToken.token }}</pre>
    <div style="margin-top: 14px; display: flex; gap: 10px">
      <a-button type="primary" @click="copyCreated">复制</a-button>
      <a-button @click="createdTokenOpen = false">关闭</a-button>
    </div>
  </a-modal>
</div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { api } from '../api'
import { guard, loadTokens, store } from '../store'
import { fmtTime } from '../utils'

const columns = [
  { title: 'ID', dataIndex: 'id', width: 60 },
  { title: '名称', dataIndex: 'name' },
  { title: 'Token', key: 'token' },
  { title: '状态', key: 'enabled', width: 90 },
  { title: '创建时间', key: 'created_at', width: 170 },
  { title: '操作', key: 'ops', width: 120 },
]

const newTokenName = ref('')
const createdTokenOpen = ref(false)
const createdToken = reactive({ token: '' })

async function createToken() {
  if (!newTokenName.value.trim()) return
  await guard(async () => {
    const v = await api('/tokens', { method: 'POST', body: { name: newTokenName.value.trim() } })
    createdToken.token = v.token
    createdTokenOpen.value = true
    newTokenName.value = ''
    await loadTokens()
  })
}

async function toggleToken(t) {
  await guard(async () => {
    await api('/tokens/' + t.id, { method: 'PUT', body: { name: t.name, enabled: !t.enabled } })
    await loadTokens()
  })
}

function delToken(t) {
  Modal.confirm({
    title: `删除 token「${t.name}」？使用它的客户端会立即失效。`,
    okText: '删除',
    okButtonProps: { danger: true },
    onOk: () => guard(async () => {
      await api('/tokens/' + t.id, { method: 'DELETE' })
      await loadTokens()
    }),
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

function copyCreated() {
  copyText(createdToken.token)
}

async function revealToken(t) {
  await guard(async () => {
    const v = await api('/tokens/' + t.id)
    await copyText(v.token)
  })
}

onMounted(loadTokens)
</script>
