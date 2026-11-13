<template>
  <a-modal v-model:open="open" :title="'获取到的上游模型：' + state.channelName" width="860px" :footer="null">
    <a-spin :spinning="state.loading" style="display: block; min-height: 160px">
      <template v-if="state.loaded">
        <p class="dim">勾选要创建为模型实体的上游模型（「可增加」组默认不勾选，请手动勾选需要的模型）。「已有」项已是该渠道的模型实体，仅作展示。</p>
        <a-input v-model:value="state.filter" placeholder="按模型名筛选…" style="margin-bottom: 14px" />

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
          <a-button type="primary" :loading="saving" @click="saveFetch">创建选中模型</a-button>
          <a-button @click="open = false">取消</a-button>
        </div>
      </template>
    </a-spin>
  </a-modal>
</template>

<script setup>
import { computed, reactive, ref } from 'vue'
import { api } from '../api'
import { guard } from '../store'

const emit = defineEmits(['saved'])
const open = ref(false)
const saving = ref(false)
const state = reactive({ channelID: null, channelName: '', filter: '', rows: [], loading: false, loaded: false })

const fetchColumns = [
  { title: '', key: 'checked', width: 40 },
  { title: '上游模型', key: 'name' },
  { title: '上下文', dataIndex: 'context_length', width: 100, customRender: ({ text }) => text ?? '-' },
  { title: '最大输出', dataIndex: 'max_output_tokens', width: 100, customRender: ({ text }) => text ?? '-' },
]
const fetchColumnsExisting = fetchColumns.filter(c => c.key !== 'checked')

const filteredRows = computed(() => {
  const q = (state.filter || '').trim().toLowerCase()
  if (!q) return state.rows
  return state.rows.filter(r => r.name.toLowerCase().includes(q))
})
const existingRows = computed(() => filteredRows.value.filter(r => r.category === 'existing'))
const newRows = computed(() => filteredRows.value.filter(r => r.category === 'new'))

async function show(c) {
  Object.assign(state, {
    channelID: c.id, channelName: c.name, filter: '', rows: [], loading: true, loaded: false,
  })
  open.value = true
  await guard(async () => {
    const v = await api('/channels/' + c.id + '/fetch-models', { method: 'POST' })
    state.rows = (v.data || []).map(r => ({
      checked: false,
      category: r.exists ? 'existing' : 'new',
      name: r.name,
      context_length: r.context_length,
      max_output_tokens: r.max_output_tokens,
    }))
    state.loaded = true
  })
  state.loading = false
  if (!state.loaded) open.value = false // 失败已由 guard 提示
}

async function saveFetch() {
  await guard(async () => {
    const names = state.rows.filter(r => r.checked && r.category === 'new').map(r => r.name)
    if (!names.length) {
      open.value = false
      return
    }
    saving.value = true
    try {
      await api('/channels/' + state.channelID + '/models', { method: 'POST', body: { names } })
      open.value = false
      emit('saved')
    } finally {
      saving.value = false
    }
  })
}

defineExpose({ show })
</script>
