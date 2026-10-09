<template>
<div class="page-card">
  <div class="page-bar">
    <span class="dim">按优先级排序（大在前）</span>
    <a-button type="primary" @click="openChannelForm(null)">新建渠道</a-button>
  </div>

  <a-table :scroll="{ x: 'max-content' }" :data-source="sortedChannels" :columns="columns" row-key="id" :pagination="false">
    <template #bodyCell="{ column, record: c }">
      <template v-if="column.key === 'name'">
        {{ c.name }}
        <div v-if="c.remark" class="ch-remark">{{ c.remark }}</div>
      </template>
      <template v-else-if="column.key === 'base_url'">
        <span class="mono wrap">{{ c.base_url }}</span>
      </template>
      <template v-else-if="column.key === 'models'">
        <a class="td-link" @click="showChannelModels(c)">{{ channelModels(c).length }}</a>
      </template>
      <template v-else-if="column.key === 'enabled'">
        <a-tag :color="c.enabled ? 'success' : 'default'">{{ c.enabled ? '正常' : '已停用' }}</a-tag>
      </template>
      <template v-else-if="column.key === 'ops'">
        <a-space wrap>
          <a @click="openChannelForm(c)">编辑</a>
          <a @click="fetchModels(c)">获取模型</a>
          <a @click="testChannel(c)">测试</a>
          <a @click="toggleChannel(c)">{{ c.enabled ? '停用' : '启用' }}</a>
          <a class="err" @click="delChannel(c)">删除</a>
        </a-space>
      </template>
    </template>
  </a-table>

  <!-- 渠道编辑弹窗 -->
  <a-modal v-model:open="chFormOpen" :title="chForm.id ? '编辑渠道' : '新建渠道'" @ok="saveChannel">
    <a-form layout="vertical">
      <a-form-item label="名称"><a-input v-model:value="chForm.name" /></a-form-item>
      <a-form-item label="上游地址（base_url）">
        <a-input v-model:value="chForm.base_url" placeholder="https://api.example.com/v1" />
      </a-form-item>
      <a-form-item label="API Key"><a-input v-model:value="chForm.api_key" /></a-form-item>
      <a-form-item label="优先级（越大越优先）">
        <a-input-number v-model:value="chForm.priority" :min="0" style="width: 100%" />
      </a-form-item>
      <a-form-item label="测试模型（连通性测试用，选便宜的）">
        <a-select v-model:value="chForm.test_model" allow-clear placeholder="-- 未配置 --">
          <a-select-option v-for="opt in testModelOptions" :key="opt" :value="opt">{{ opt }}</a-select-option>
        </a-select>
      </a-form-item>
      <a-form-item label="备注"><a-input v-model:value="chForm.remark" placeholder="选填，如用途、供应商说明" /></a-form-item>
      <a-form-item><a-checkbox v-model:checked="chForm.enabled">启用</a-checkbox></a-form-item>
    </a-form>
  </a-modal>

  <!-- 渠道模型实体弹窗 -->
  <a-modal v-model:open="chModelsDlgOpen" :title="'模型实体：' + chModelsDlg.channel" :footer="null">
    <a-table :scroll="{ x: 'max-content' }" :data-source="chModelsDlg.models" :columns="chModelsColumns" row-key="name" :pagination="false" size="small">
      <template #bodyCell="{ column, record: m }">
        <template v-if="column.key === 'name'"><span class="mono">{{ m.name }}</span></template>
        <template v-else-if="column.key === 'bound'"><span class="mono">{{ m.bound }}</span></template>
      </template>
    </a-table>
  </a-modal>

  <!-- 获取模型弹窗 -->
  <a-modal v-model:open="fetchDlgOpen" :title="'获取到的上游模型：' + fetchDlg.channelName" width="860px" :footer="null">
    <p class="dim">勾选要创建为模型实体的上游模型（「可增加」组默认勾选）。「已有」项已是该渠道的模型实体，仅作展示。</p>
    <a-input v-model:value="fetchDlg.filter" placeholder="按模型名筛选…" style="margin-bottom: 14px" />

    <h4>可增加（{{ newRows.length }}）</h4>
    <a-table :scroll="{ x: 'max-content' }" :data-source="newRows" :columns="fetchColumns" row-key="name" :pagination="false" size="small">
      <template #bodyCell="{ column, record: r }">
        <template v-if="column.key === 'checked'">
          <a-checkbox v-model:checked="r.checked" />
        </template>
        <template v-else-if="column.key === 'name'"><span class="mono">{{ r.name }}</span></template>
      </template>
    </a-table>

    <h4 style="margin-top: 18px">已有（{{ existingRows.length }}）</h4>
    <a-table :scroll="{ x: 'max-content' }" :data-source="existingRows" :columns="fetchColumnsExisting" row-key="name" :pagination="false" size="small">
      <template #bodyCell="{ column, record: r }">
        <template v-if="column.key === 'name'"><span class="mono">{{ r.name }}</span></template>
      </template>
    </a-table>

    <div style="margin-top: 18px; display: flex; gap: 10px">
      <a-button type="primary" @click="saveFetch">创建选中模型</a-button>
      <a-button @click="fetchDlgOpen = false">取消</a-button>
    </div>
  </a-modal>

  <!-- 测试结果弹窗 -->
  <a-modal v-model:open="testResultOpen" :title="'连通性测试：' + testResult.channel" width="720px" :footer="null">
    <a-spin :spinning="testResult.loading">
      <template v-if="!testResult.loading">
        <p>
          <a-tag :color="testResult.result.success ? 'success' : 'error'">
            {{ testResult.result.success ? '成功' : '失败' }}
          </a-tag>
          耗时 {{ testResult.result.latency_ms }}ms
          <template v-if="testResult.result.http_status"> · HTTP {{ testResult.result.http_status }}</template>
        </p>
        <p v-if="testResult.result.error" class="err">{{ testResult.result.error }}</p>
        <h4>请求</h4>
        <pre>{{ pretty(testResult.result.request_body) }}</pre>
        <h4>响应</h4>
        <pre>{{ pretty(testResult.result.response_body || '') }}</pre>
      </template>
    </a-spin>
  </a-modal>
</div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { api } from '../api'
import { channelModels, guard, loadChannels, loadModels, sortedChannels, store } from '../store'
import { pretty } from '../utils'

const columns = [
  { title: 'ID', dataIndex: 'id', width: 60 },
  { title: '名称', dataIndex: 'name', key: 'name' },
  { title: '地址', key: 'base_url' },
  { title: '优先级', dataIndex: 'priority', width: 80 },
  { title: '模型数', key: 'models', width: 80 },
  { title: '状态', key: 'enabled', width: 90 },
  { title: '操作', key: 'ops', width: 260 },
]
const chModelsColumns = [
  { title: '模型', key: 'name' },
  { title: '绑定标准名', key: 'bound' },
]
const fetchColumns = [
  { title: '', key: 'checked', width: 40 },
  { title: '上游模型', key: 'name' },
  { title: '上下文', dataIndex: 'context_length', width: 100, customRender: ({ text }) => text ?? '-' },
  { title: '最大输出', dataIndex: 'max_output_tokens', width: 100, customRender: ({ text }) => text ?? '-' },
]
const fetchColumnsExisting = fetchColumns.filter(c => c.key !== 'checked')

// ---- 渠道编辑 ----
const chFormOpen = ref(false)
const chForm = reactive({ id: null, name: '', base_url: '', api_key: '', priority: 0, enabled: true, test_model: '', remark: '' })
const testModelOptions = computed(() => {
  if (!chForm.id) return []
  return store.models.filter(m => m.channel_id === chForm.id).map(m => m.name)
})

function openChannelForm(c) {
  if (c) {
    Object.assign(chForm, {
      id: c.id, name: c.name, base_url: c.base_url, api_key: c.api_key,
      priority: c.priority, enabled: c.enabled, test_model: c.test_model, remark: c.remark,
    })
  } else {
    Object.assign(chForm, { id: null, name: '', base_url: '', api_key: '', priority: 0, enabled: true, test_model: '', remark: '' })
  }
  chFormOpen.value = true
}

async function saveChannel() {
  await guard(async () => {
    const body = {
      name: chForm.name, base_url: chForm.base_url, api_key: chForm.api_key,
      priority: chForm.priority, enabled: chForm.enabled, test_model: chForm.test_model, remark: chForm.remark,
    }
    if (chForm.id) await api('/channels/' + chForm.id, { method: 'PUT', body })
    else await api('/channels', { method: 'POST', body })
    chFormOpen.value = false
    await loadChannels()
  })
}

function delChannel(c) {
  Modal.confirm({
    title: `删除渠道「${c.name}」？`,
    okText: '删除',
    okButtonProps: { danger: true },
    onOk: () => guard(async () => {
      await api('/channels/' + c.id, { method: 'DELETE' })
      await Promise.all([loadChannels(), loadModels()])
    }),
  })
}

async function toggleChannel(c) {
  await guard(async () => {
    await api('/channels/' + c.id + '/toggle', { method: 'POST', body: { enabled: !c.enabled } })
    await loadChannels()
  })
}

// ---- 渠道模型实体 ----
const chModelsDlgOpen = ref(false)
const chModelsDlg = reactive({ channel: '', models: [] })

function showChannelModels(c) {
  chModelsDlg.channel = c.name
  chModelsDlg.models = channelModels(c).map(m => ({
    name: m.name,
    bound: (m.mappings || []).join(', ') || '未绑定',
  }))
  chModelsDlgOpen.value = true
}

// ---- 获取模型 ----
const fetchDlgOpen = ref(false)
const fetchDlg = reactive({ channelID: null, channelName: '', filter: '', rows: [] })

const fetchRows = computed(() => {
  const q = (fetchDlg.filter || '').trim().toLowerCase()
  if (!q) return fetchDlg.rows
  return fetchDlg.rows.filter(r => r.name.toLowerCase().includes(q))
})
const existingRows = computed(() => fetchRows.value.filter(r => r.category === 'existing'))
const newRows = computed(() => fetchRows.value.filter(r => r.category === 'new'))

async function fetchModels(c) {
  await guard(async () => {
    const v = await api('/channels/' + c.id + '/fetch-models', { method: 'POST' })
    fetchDlg.channelID = c.id
    fetchDlg.channelName = c.name
    fetchDlg.filter = ''
    fetchDlg.rows = (v.data || []).map(r => ({
      checked: !r.exists,
      category: r.exists ? 'existing' : 'new',
      name: r.name,
      context_length: r.context_length,
      max_output_tokens: r.max_output_tokens,
    }))
    fetchDlgOpen.value = true
  })
}

async function saveFetch() {
  await guard(async () => {
    const names = fetchDlg.rows.filter(r => r.checked && r.category === 'new').map(r => r.name)
    fetchDlgOpen.value = false
    if (!names.length) return
    await api('/channels/' + fetchDlg.channelID + '/models', { method: 'POST', body: { names } })
    await Promise.all([loadChannels(), loadModels()])
  })
}

// ---- 连通性测试 ----
const testResultOpen = ref(false)
const testResult = reactive({ loading: false, channel: '', result: null })

async function testChannel(c) {
  testResult.loading = true
  testResult.channel = c.name
  testResult.result = null
  testResultOpen.value = true
  await guard(async () => {
    const v = await api('/channels/' + c.id + '/test', { method: 'POST' })
    testResult.loading = false
    testResult.result = v
  })
  if (testResult.loading) testResultOpen.value = false // 失败已由 guard 提示
}

onMounted(loadChannels)
</script>
