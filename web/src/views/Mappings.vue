<template>
<div class="page-card">
  <div class="page-bar">
    <span class="dim">对外暴露的标准模型名 → 绑定各渠道下的模型实体</span>
    <a-button type="primary" @click="openMappingForm(null)">新建映射</a-button>
  </div>

  <a-table :scroll="{ x: 'max-content' }" :data-source="store.mappings" :columns="columns" row-key="id" :pagination="false">
    <template #bodyCell="{ column, record: m }">
      <template v-if="column.key === 'name'"><span class="mono">{{ m.name }}</span></template>
      <template v-else-if="column.key === 'context_length'">{{ m.context_length ?? '-' }}</template>
      <template v-else-if="column.key === 'max_output_tokens'">{{ m.max_output_tokens ?? '-' }}</template>
      <template v-else-if="column.key === 'bound'">
        <a class="td-link" @click="showMappingModels(m)">{{ (m.bound_models || []).length }}</a>
      </template>
      <template v-else-if="column.key === 'ops'">
        <a-space>
          <a @click="openMappingForm(m)">编辑</a>
          <a class="err" @click="delMapping(m)">删除</a>
        </a-space>
      </template>
    </template>
  </a-table>

  <!-- 编辑弹窗 -->
  <a-modal v-model:open="mappingFormOpen" :title="mappingForm.id ? '编辑模型映射' : '新建模型映射'" @ok="saveMapping">
    <a-form layout="vertical">
      <a-form-item label="标准名">
        <a-input v-model:value="mappingForm.name" placeholder="对外暴露的标准模型名，如 glm-5.3" />
      </a-form-item>
      <a-form-item label="上下文长度（可空）">
        <a-input-number v-model:value="mappingForm.context_length" :min="0" style="width: 100%" />
      </a-form-item>
      <a-form-item label="最大输出 tokens（可空）">
        <a-input-number v-model:value="mappingForm.max_output_tokens" :min="0" style="width: 100%" />
      </a-form-item>
      <a-form-item label="绑定模型">
        <div v-for="g in mappingModelGroups" :key="g.channel" style="margin-bottom: 8px">
          <div class="dim" style="margin-bottom: 4px">{{ g.channel }}</div>
          <a-checkbox-group v-model:value="mappingForm.model_ids" :options="g.options" />
        </div>
        <p v-if="!mappingModelGroups.length" class="dim">暂无模型实体，请先在渠道下创建模型</p>
      </a-form-item>
    </a-form>
  </a-modal>

  <!-- 绑定模型弹窗 -->
  <a-modal v-model:open="mappingModelsDlgOpen" :title="'绑定模型：' + mappingModelsDlg.name" :footer="null">
    <a-table :scroll="{ x: 'max-content' }" :data-source="mappingModelsDlg.models" :columns="boundColumns" row-key="id" :pagination="false" size="small">
      <template #bodyCell="{ column, record: m }">
        <template v-if="column.key === 'name'"><span class="mono">{{ m.name }}</span></template>
      </template>
    </a-table>
  </a-modal>
</div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { Modal } from 'ant-design-vue'
import { api } from '../api'
import { guard, loadMappings, loadModels, store } from '../store'

const columns = [
  { title: 'ID', dataIndex: 'id', width: 60 },
  { title: '标准名', key: 'name' },
  { title: '上下文长度', key: 'context_length', width: 110 },
  { title: '最大输出', key: 'max_output_tokens', width: 100 },
  { title: '绑定模型数', key: 'bound', width: 110 },
  { title: '操作', key: 'ops', width: 120 },
]
const boundColumns = [
  { title: 'ID', dataIndex: 'id', width: 60 },
  { title: '模型', key: 'name' },
  { title: '所属渠道', dataIndex: 'channel_name' },
]

const mappingModelGroups = computed(() => {
  const groups = []
  const byChannel = new Map()
  for (const m of store.models) {
    const key = m.channel_name || '-'
    if (!byChannel.has(key)) {
      const g = { channel: key, options: [] }
      byChannel.set(key, g)
      groups.push(g)
    }
    byChannel.get(key).options.push({ label: m.name, value: m.id })
  }
  return groups
})

const mappingFormOpen = ref(false)
const mappingForm = reactive({ id: null, name: '', context_length: null, max_output_tokens: null, model_ids: [] })

function openMappingForm(m) {
  if (m) {
    Object.assign(mappingForm, {
      id: m.id, name: m.name,
      context_length: m.context_length, max_output_tokens: m.max_output_tokens,
      model_ids: (m.bound_models || []).map(x => x.id),
    })
  } else {
    Object.assign(mappingForm, { id: null, name: '', context_length: null, max_output_tokens: null, model_ids: [] })
  }
  mappingFormOpen.value = true
}

async function saveMapping() {
  await guard(async () => {
    const f = mappingForm
    const body = {
      name: f.name,
      context_length: f.context_length || null,
      max_output_tokens: f.max_output_tokens || null,
      model_ids: f.model_ids,
    }
    if (f.id) await api('/mappings/' + f.id, { method: 'PUT', body })
    else await api('/mappings', { method: 'POST', body })
    mappingFormOpen.value = false
    await Promise.all([loadMappings(), loadModels()])
  })
}

function delMapping(m) {
  Modal.confirm({
    title: `删除映射「${m.name}」？与模型的绑定关系会一并移除。`,
    okText: '删除',
    okButtonProps: { danger: true },
    onOk: () => guard(async () => {
      await api('/mappings/' + m.id, { method: 'DELETE' })
      await Promise.all([loadMappings(), loadModels()])
    }),
  })
}

const mappingModelsDlgOpen = ref(false)
const mappingModelsDlg = reactive({ name: '', models: [] })

function showMappingModels(m) {
  mappingModelsDlg.name = m.name
  mappingModelsDlg.models = m.bound_models || []
  mappingModelsDlgOpen.value = true
}

onMounted(loadMappings)
</script>
