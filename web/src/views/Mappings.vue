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
      <template v-else-if="column.key === 'overrides'">
        <span v-if="overrideKeys(m).length" :title="overrideKeys(m).map(k => k + '=' + JSON.stringify(m.parameter_overrides[k])).join('\n')">
          {{ overrideKeys(m).join(', ') }}
        </span>
        <span v-else class="dim">-</span>
      </template>
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
  <a-modal v-model:open="mappingFormOpen" :title="mappingForm.id ? '编辑模型映射' : '新建模型映射'" :confirm-loading="mappingSaving" @ok="saveMapping">
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
      <a-form-item label="强制参数覆盖（可选，转发时覆盖下游同名字段）">
        <div v-for="(row, i) in mappingForm.overrides" :key="i" style="display: flex; gap: 8px; margin-bottom: 8px">
          <a-input v-model:value="row.key" placeholder="参数名，如 temperature" style="width: 42%" />
          <a-input v-model:value="row.value" placeholder="值，如 1 / true / 字符串" style="flex: 1" />
          <a-button type="text" danger @click="mappingForm.overrides.splice(i, 1)">删除</a-button>
        </div>
        <a-button type="dashed" block @click="mappingForm.overrides.push({ key: '', value: '' })">添加参数</a-button>
        <p class="dim" style="margin-top: 4px">数值与 true/false 会按原类型发送，其余按字符串发送</p>
      </a-form-item>
      <a-form-item label="绑定模型">
        <div v-for="g in mappingModelGroups" :key="g.channel" style="margin-bottom: 8px">
          <div class="dim" style="margin-bottom: 4px">{{ g.channel }}</div>
          <a-checkbox
            v-for="opt in g.options"
            :key="opt.value"
            :checked="mappingForm.model_ids.includes(opt.value)"
            :style="{ marginRight: '8px' }"
            @change="toggleModelId(opt.value)"
          >{{ opt.label }}</a-checkbox>
        </div>
        <p v-if="!mappingModelGroups.length" class="dim">暂无模型实体，请先在渠道下创建模型</p>
      </a-form-item>
    </a-form>
  </a-modal>

  <!-- 绑定模型弹窗：按请求进来后实际尝试的顺序展示 -->
  <a-modal v-model:open="mappingModelsDlgOpen" :title="'绑定模型：' + mappingModelsDlg.name" :footer="null">
    <p class="dim" style="margin: 0 0 10px">
      顺序 = 请求进来后实际尝试顺序：渠道优先级高者优先，同渠道按模型 ID 升序；渠道已禁用的模型不参与调用，排在末尾。
    </p>
    <a-table :scroll="{ x: 'max-content' }" :data-source="mappingModelsDlg.models" :columns="boundColumns" row-key="id" :pagination="false" size="small">
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
  { title: '强制参数', key: 'overrides', width: 160 },
  { title: '绑定模型数', key: 'bound', width: 110 },
  { title: '操作', key: 'ops', width: 120 },
]
const boundColumns = [
  { title: '调用顺序', key: 'order', width: 90 },
  { title: 'ID', dataIndex: 'id', width: 60 },
  { title: '模型', key: 'name' },
  { title: '所属渠道', key: 'channel' },
  { title: '渠道优先级', dataIndex: 'channel_priority', width: 100 },
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
const mappingForm = reactive({ id: null, name: '', context_length: null, max_output_tokens: null, model_ids: [], overrides: [] })
const mappingSaving = ref(false)

function overrideKeys(m) {
  return Object.keys(m.parameter_overrides || {})
}

// 文本 → 原始类型：数字/true/false/null 按字面量，JSON 可解析的（含带引号字符串、数组）按 JSON，其余按字符串
function parseOverrideValue(text) {
  const t = String(text).trim()
  if (t === 'true') return true
  if (t === 'false') return false
  if (t === 'null') return null
  if (t !== '' && !Number.isNaN(Number(t))) return Number(t)
  try { return JSON.parse(t) } catch { return text }
}

function toggleModelId(id) {
  const i = mappingForm.model_ids.indexOf(id)
  if (i === -1) mappingForm.model_ids.push(id)
  else mappingForm.model_ids.splice(i, 1)
}

function openMappingForm(m) {
  if (m) {
    Object.assign(mappingForm, {
      id: m.id, name: m.name,
      context_length: m.context_length, max_output_tokens: m.max_output_tokens,
      model_ids: (m.bound_models || []).map(x => x.id),
      overrides: overrideKeys(m).map(k => ({ key: k, value: JSON.stringify(m.parameter_overrides[k]) ?? '' })),
    })
  } else {
    Object.assign(mappingForm, { id: null, name: '', context_length: null, max_output_tokens: null, model_ids: [], overrides: [] })
  }
  mappingFormOpen.value = true
}

async function saveMapping() {
  mappingSaving.value = true
  try {
    await guard(async () => {
      const f = mappingForm
      const parameter_overrides = {}
      for (const row of f.overrides) {
        const key = row.key.trim()
        if (key) parameter_overrides[key] = parseOverrideValue(row.value)
      }
      const body = {
        name: f.name,
        context_length: f.context_length || null,
        max_output_tokens: f.max_output_tokens || null,
        model_ids: f.model_ids,
        parameter_overrides,
      }
      if (f.id) await api('/mappings/' + f.id, { method: 'PUT', body })
      else await api('/mappings', { method: 'POST', body })
      mappingFormOpen.value = false
      await Promise.all([loadMappings(), loadModels()])
    })
  } finally {
    mappingSaving.value = false
  }
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
  // 后端已按 c.enabled DESC, c.priority DESC, m.id ASC 排好；这里只给参与调用的项编号
  let rank = 0
  mappingModelsDlg.models = (m.bound_models || []).map(b => {
    const enabled = b.channel_enabled !== false // 兼容尚未返回该字段的旧版后端
    return { ...b, channel_enabled: enabled, order: enabled ? ++rank : null }
  })
  mappingModelsDlgOpen.value = true
}

onMounted(loadMappings)
</script>
