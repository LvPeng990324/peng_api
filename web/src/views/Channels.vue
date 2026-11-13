<template>
<div class="page-card">
  <div class="page-bar">
    <span class="dim">按优先级排序（大在前）</span>
    <a-button type="primary" @click="formModal.show(null)">新建渠道</a-button>
  </div>

  <a-table :scroll="{ x: 'max-content' }" :data-source="sortedChannels" :columns="columns" row-key="id" :pagination="false">
    <template #bodyCell="{ column, record: c }">
      <template v-if="column.key === 'name'">
        {{ c.name }}
        <div v-if="c.remark" class="ch-remark">{{ c.remark }}</div>
      </template>
      <div v-else-if="column.key === 'quota'" class="quota-cell">
        <span v-if="!c.quota_type">-</span>
        <span v-else-if="quotaFor(c).loading" class="dim">加载中…</span>
        <span v-else-if="quotaFor(c).error" class="err" :title="quotaFor(c).error">获取失败</span>
        <template v-else-if="quotaFor(c).quota">
          <span v-if="c.quota_type === 'openrouter'">剩余：{{ quotaFor(c).quota.remaining ?? '-' }}</span>
          <span v-else-if="c.quota_type === 'deepseek'">余额：{{ quotaFor(c).quota.balance ?? '-' }}</span>
          <div v-else-if="c.quota_type === 'kimi'">
            <div>5h：{{ formatPercent(quotaFor(c).quota.five_hour) }}</div>
            <div>7d：{{ formatPercent(quotaFor(c).quota.weekly) }}</div>
          </div>
        </template>
        <span v-else>-</span>
      </div>
      <template v-else-if="column.key === 'base_url'">
        <span class="mono wrap">{{ c.base_url }}</span>
      </template>
      <template v-else-if="column.key === 'models'">
        <a class="td-link" @click="modelsModal.show(c)">{{ channelModels(c).length }}</a>
      </template>
      <template v-else-if="column.key === 'enabled'">
        <a-tag :color="c.enabled ? 'success' : 'default'">{{ c.enabled ? '正常' : '已停用' }}</a-tag>
      </template>
      <template v-else-if="column.key === 'ops'">
        <a-space wrap>
          <a @click="formModal.show(c)">编辑</a>
          <a @click="fetchModal.show(c)">获取模型</a>
          <a @click="testModal.show(c)">测试</a>
          <a @click="toggleChannel(c)">{{ c.enabled ? '停用' : '启用' }}</a>
          <a class="err" @click="delChannel(c)">删除</a>
        </a-space>
      </template>
    </template>
  </a-table>

  <ChannelFormModal ref="formModal" @saved="reload" />
  <ChannelModelsModal ref="modelsModal" />
  <FetchModelsModal ref="fetchModal" @saved="reload" />
  <TestChannelModal ref="testModal" />
</div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { Modal } from 'ant-design-vue'
import { api } from '../api'
import {
  channelModels, ensureLoaded, guard, loadChannels, loadModels,
  quotaFor, sortedChannels,
} from '../store'
import ChannelFormModal from '../components/ChannelFormModal.vue'
import ChannelModelsModal from '../components/ChannelModelsModal.vue'
import FetchModelsModal from '../components/FetchModelsModal.vue'
import TestChannelModal from '../components/TestChannelModal.vue'

const columns = [
  { title: 'ID', dataIndex: 'id', width: 60 },
  { title: '名称', dataIndex: 'name', key: 'name' },
  { title: '额度', key: 'quota', width: 110 },
  { title: '地址', key: 'base_url' },
  { title: '优先级', dataIndex: 'priority', width: 80 },
  { title: '模型数', key: 'models', width: 80 },
  { title: '状态', key: 'enabled', width: 90 },
  { title: '操作', key: 'ops', width: 260 },
]

const formModal = ref(null)
const modelsModal = ref(null)
const fetchModal = ref(null)
const testModal = ref(null)

function formatPercent(value) {
  if (value === null || value === undefined) return '-'
  return Number(value.toFixed(2)) + '%'
}

async function reload() {
  await Promise.all([loadChannels(), loadModels()])
}

function delChannel(c) {
  Modal.confirm({
    title: `删除渠道「${c.name}」？`,
    okText: '删除',
    okButtonProps: { danger: true },
    onOk: () => guard(async () => {
      await api('/channels/' + c.id, { method: 'DELETE' })
      await reload()
    }),
  })
}

async function toggleChannel(c) {
  await guard(async () => {
    await api('/channels/' + c.id + '/toggle', { method: 'POST', body: { enabled: !c.enabled } })
    await loadChannels()
  })
}

onMounted(() => ensureLoaded('channels'))
</script>

<style scoped>
.quota-cell {
  font-size: 12px;
  line-height: 1.4;
  white-space: nowrap;
}
</style>
