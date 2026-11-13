<template>
  <a-modal v-model:open="open" :title="'模型实体：' + state.channel" :footer="null">
    <a-table :scroll="{ x: 'max-content' }" :data-source="state.models" :columns="columns" row-key="name" :pagination="false" size="small">
      <template #bodyCell="{ column, record: m }">
        <template v-if="column.key === 'name'"><span class="mono">{{ m.name }}</span></template>
        <template v-else-if="column.key === 'bound'"><span class="mono">{{ m.bound }}</span></template>
      </template>
    </a-table>
  </a-modal>
</template>

<script setup>
import { reactive, ref } from 'vue'
import { channelModels } from '../store'

const open = ref(false)
const state = reactive({ channel: '', models: [] })
const columns = [
  { title: '模型', key: 'name' },
  { title: '绑定标准名', key: 'bound' },
]

function show(c) {
  state.channel = c.name
  state.models = channelModels(c).map(m => ({
    name: m.name,
    bound: (m.mappings || []).join(', ') || '未绑定',
  }))
  open.value = true
}

defineExpose({ show })
</script>
