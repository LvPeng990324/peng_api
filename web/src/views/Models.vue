<template>
<div class="page-card">
  <div class="page-bar">
    <span class="dim">渠道下的模型实体（上游真实模型名）</span>
    <a-button type="primary" @click="openModelForm(null)">新建模型</a-button>
  </div>

  <a-table :scroll="{ x: 'max-content' }" :data-source="store.models" :columns="columns" row-key="id" :pagination="false">
    <template #bodyCell="{ column, record: m }">
      <template v-if="column.key === 'name'"><span class="mono">{{ m.name }}</span></template>
      <template v-else-if="column.key === 'mappings'">
        <span class="mono wrap">{{ (m.mappings || []).join(', ') || '-' }}</span>
      </template>
      <template v-else-if="column.key === 'created_at'">{{ fmtTime(m.created_at) }}</template>
      <template v-else-if="column.key === 'ops'">
        <a-space>
          <a @click="openModelForm(m)">编辑</a>
          <a class="err" @click="delModel(m)">删除</a>
        </a-space>
      </template>
    </template>
  </a-table>

  <a-modal v-model:open="modelFormOpen" :title="modelForm.id ? '编辑模型' : '新建模型'" @ok="saveModel">
    <a-form layout="vertical">
      <a-form-item label="所属渠道">
        <a-select v-model:value="modelForm.channel_id" :disabled="!!modelForm.id">
          <a-select-option v-for="c in store.channels" :key="c.id" :value="c.id">{{ c.name }}</a-select-option>
        </a-select>
      </a-form-item>
      <a-form-item label="名称">
        <a-input v-model:value="modelForm.name" placeholder="上游模型名，如 glm-5.3" />
      </a-form-item>
    </a-form>
  </a-modal>
</div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { Modal } from 'ant-design-vue'
import { api } from '../api'
import { guard, loadMappings, loadModels, store } from '../store'
import { fmtTime } from '../utils'

const columns = [
  { title: 'ID', dataIndex: 'id', width: 60 },
  { title: '名称', key: 'name' },
  { title: '所属渠道', dataIndex: 'channel_name', customRender: ({ text }) => text || '-' },
  { title: '绑定标准名', key: 'mappings' },
  { title: '创建时间', key: 'created_at', width: 170 },
  { title: '操作', key: 'ops', width: 120 },
]

const modelFormOpen = ref(false)
const modelForm = reactive({ id: null, channel_id: null, name: '' })

function openModelForm(m) {
  if (m) {
    Object.assign(modelForm, { id: m.id, channel_id: m.channel_id, name: m.name })
  } else {
    Object.assign(modelForm, { id: null, channel_id: store.channels.length ? store.channels[0].id : null, name: '' })
  }
  modelFormOpen.value = true
}

async function saveModel() {
  await guard(async () => {
    if (modelForm.id) {
      await api('/models/' + modelForm.id, { method: 'PUT', body: { name: modelForm.name } })
    } else {
      await api('/models', { method: 'POST', body: { channel_id: modelForm.channel_id, name: modelForm.name } })
    }
    modelFormOpen.value = false
    await loadModels()
  })
}

function delModel(m) {
  Modal.confirm({
    title: `删除模型「${m.name}」？与标准名的绑定关系会一并移除。`,
    okText: '删除',
    okButtonProps: { danger: true },
    onOk: () => guard(async () => {
      await api('/models/' + m.id, { method: 'DELETE' })
      await Promise.all([loadModels(), loadMappings()])
    }),
  })
}

onMounted(loadModels)
</script>
