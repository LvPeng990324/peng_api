<template>
  <a-modal v-model:open="visible" :title="'绑定模型：' + state.name" :footer="null">
    <p class="dim" style="margin: 0 0 10px">
      顺序 = 请求进来后实际尝试顺序：渠道优先级高者优先，同渠道按模型 ID 升序；渠道已禁用的模型不参与调用，排在末尾。
    </p>
    <a-table :scroll="{ x: 'max-content' }" :data-source="state.models" :columns="columns" row-key="id" :pagination="false" size="small">
      <template #bodyCell="{ column, record: m }">
        <template v-if="column.key === 'order'">
          <span v-if="m.order" class="order-num">{{ m.order }}</span>
          <span v-else class="dim">-</span>
        </template>
        <template v-else-if="column.key === 'name'">
          <span class="mono" :class="{ dim: !m.channel_enabled }">{{ m.name }}</span>
        </template>
        <template v-else-if="column.key === 'channel'">
          {{ m.channel_name }}
          <span v-if="!m.channel_enabled" class="err" style="margin-left: 6px; font-size: 12px">渠道已禁用</span>
        </template>
      </template>
    </a-table>
  </a-modal>
</template>

<script setup>
import { reactive, ref } from 'vue'

const visible = ref(false)
const state = reactive({ name: '', models: [] })
const columns = [
  { title: '调用顺序', key: 'order', width: 90 },
  { title: 'ID', dataIndex: 'id', width: 60 },
  { title: '模型', key: 'name' },
  { title: '所属渠道', key: 'channel' },
  { title: '渠道优先级', dataIndex: 'channel_priority', width: 100 },
]

function show(m) {
  state.name = m.name
  // 后端已按 c.enabled DESC, c.priority DESC, m.id ASC 排好；这里只给参与调用的项编号
  let rank = 0
  state.models = (m.bound_models || []).map(b => {
    const enabled = b.channel_enabled !== false
    return { ...b, channel_enabled: enabled, order: enabled ? ++rank : null }
  })
  visible.value = true
}

defineExpose({ show })
</script>
