<template>
<div class="page-card">
  <div class="page-bar">
    <span class="dim">对外暴露的标准模型名 → 绑定各渠道下的模型实体</span>
    <a-button type="primary" @click="formModal.show(null)">新建映射</a-button>
  </div>

  <a-table :scroll="{ x: 'max-content' }" :data-source="store.mappings" :columns="columns" row-key="id" :pagination="false">
    <template #bodyCell="{ column, record: m }">
      <template v-if="column.key === 'name'"><span class="mono">{{ m.name }}</span></template>
      <template v-else-if="column.key === 'context_length'">{{ m.context_length ?? '-' }}</template>
      <template v-else-if="column.key === 'max_output_tokens'">{{ m.max_output_tokens ?? '-' }}</template>
      <template v-else-if="column.key === 'overrides'">
        <span v-if="overrideKeys(m).length" :title="overrideKeys(m).map(k => k + '=' + JSON.stringify(m.parameter_overrides[k])).join('\n')">
          {{ overrideKeys(m).join(', ') }}
        </span>
        <span v-else class="dim">-</span>
      </template>
      <template v-else-if="column.key === 'bound'">
        <a class="td-link" @click="boundModal.show(m)">{{ (m.bound_models || []).length }}</a>
      </template>
      <template v-else-if="column.key === 'ops'">
        <a-space>
          <a @click="formModal.show(m)">编辑</a>
          <a class="err" @click="delMapping(m)">删除</a>
        </a-space>
      </template>
    </template>
  </a-table>

  <MappingFormModal ref="formModal" @saved="reload" />
  <BoundModelsModal ref="boundModal" />
</div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { Modal } from 'ant-design-vue'
import { api } from '../api'
import { ensureLoaded, guard, loadMappings, loadModels, store } from '../store'
import MappingFormModal from '../components/MappingFormModal.vue'
import BoundModelsModal from '../components/BoundModelsModal.vue'

const columns = [
  { title: 'ID', dataIndex: 'id', width: 60 },
  { title: '标准名', key: 'name' },
  { title: '上下文长度', key: 'context_length', width: 110 },
  { title: '最大输出', key: 'max_output_tokens', width: 100 },
  { title: '强制参数', key: 'overrides', width: 160 },
  { title: '绑定模型数', key: 'bound', width: 110 },
  { title: '操作', key: 'ops', width: 120 },
]

const formModal = ref(null)
const boundModal = ref(null)

function overrideKeys(m) {
  return Object.keys(m.parameter_overrides || {})
}

async function reload() {
  await Promise.all([loadMappings(), loadModels()])
}

function delMapping(m) {
  Modal.confirm({
    title: `删除映射「${m.name}」？与模型的绑定关系会一并移除。`,
    okText: '删除',
    okButtonProps: { danger: true },
    onOk: () => guard(async () => {
      await api('/mappings/' + m.id, { method: 'DELETE' })
      await reload()
    }),
  })
}

onMounted(() => ensureLoaded('mappings'))
</script>
