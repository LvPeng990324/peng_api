<template>
<div class="page-card">
  <div class="page-bar">
    <span class="dim">按优先级排序（大在前）</span>
    <a-button type="primary" @click="formModal.show(null)">新建渠道</a-button>
  </div>

  <a-table
    :scroll="{ x: 'max-content' }"
    :data-source="sortedChannels"
    :columns="columns"
    row-key="id"
    :pagination="false"
    :custom-row="customRow"
  >
    <template #headerCell="{ title, column }">
      <span v-if="column.key === 'quota'" class="quota-header">
        {{ title }}
        <a-tooltip title="刷新所有渠道额度">
          <a-button size="small" shape="circle" @click="refreshQuotas" :disabled="refreshing">
            <span v-if="refreshing" class="icon-spin"><ReloadOutlined /></span>
            <span v-else><ReloadOutlined /></span>
          </a-button>
        </a-tooltip>
      </span>
      <span v-else>{{ title }}</span>
    </template>
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
          <div v-else-if="c.quota_type === 'new-api'">
            {{ fmtQuota(quotaFor(c).quota.remaining, quotaFor(c).quota) }} /
            {{ fmtQuota(quotaFor(c).quota.limit, quotaFor(c).quota) }}
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
import { ReloadOutlined } from '@ant-design/icons-vue'
import { api } from '../api'
import {
  channelModels, ensureLoaded, guard, loadChannels, loadModels,
  quotaFor, refreshChannelQuotas, sortedChannels,
} from '../store'
import ChannelFormModal from '../components/ChannelFormModal.vue'
import ChannelModelsModal from '../components/ChannelModelsModal.vue'
import FetchModelsModal from '../components/FetchModelsModal.vue'
import TestChannelModal from '../components/TestChannelModal.vue'

const columns = [
  { title: 'ID', dataIndex: 'id', width: 60 },
  { title: '名称', dataIndex: 'name', key: 'name' },
  { title: '额度', key: 'quota', width: 120 },
  { title: '地址', key: 'base_url' },
  { title: '优先级', dataIndex: 'priority', width: 80 },
  { title: '模型数', key: 'models', width: 80 },
  { title: '状态', key: 'enabled', width: 90, className: 'col-status' },
  { title: '操作', key: 'ops', width: 260 },
]

// 已停用的渠道整行文字加删除线（状态列的状态标签与操作列除外）
const customRow = (record) => ({ class: record.enabled === false ? 'ch-row-off' : '' })

const formModal = ref(null)
const modelsModal = ref(null)
const fetchModal = ref(null)
const testModal = ref(null)
const refreshing = ref(false)

async function refreshQuotas() {
  refreshing.value = true
  try {
    await refreshChannelQuotas()
  } finally {
    refreshing.value = false
  }
}

function formatPercent(value) {
  if (value === null || value === undefined) return '-'
  return Number(value.toFixed(2)) + '%'
}

// new-api 的额度是 quota 单位，按后端下发的 quota_per_unit（500000 quota = ¥1）折算成金额数字，
// 单元格里只显示「剩余 / 总额」，不重复货币符号。
function fmtQuota(value, quota) {
  if (value === null || value === undefined) return '-'
  const perUnit = quota.quota_per_unit
  if (!perUnit) return String(value)
  return (value / perUnit).toLocaleString('zh-CN', {
    minimumFractionDigits: 2, maximumFractionDigits: 2,
  })
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
/* 已停用的渠道：整行文字加删除线并压暗；状态列的标签与最后一列「操作」保持正常 */
:deep(.ch-row-off) > td:not(.col-status):not(:last-child) {
  text-decoration: line-through;
  color: #94a3b8;
}
.quota-cell {
  font-size: 12px;
  line-height: 1.4;
  white-space: nowrap;
}
.quota-header {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.icon-spin {
  display: inline-block;
  animation: iconSpin 1s linear infinite;
}
@keyframes iconSpin {
  to { transform: rotate(360deg); }
}
</style>
