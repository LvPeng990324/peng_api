<template>
  <a-modal v-model:open="visible" :title="form.id ? '编辑模型映射' : '新建模型映射'" :confirm-loading="saving" @ok="save">
    <a-form layout="vertical">
      <a-form-item label="标准名">
        <a-input v-model:value="form.name" placeholder="对外暴露的标准模型名，如 glm-5.3" />
      </a-form-item>
      <a-form-item label="上下文长度（可空）">
        <a-input-number v-model:value="form.context_length" :min="0" style="width: 100%" />
      </a-form-item>
      <a-form-item label="最大输出 tokens（可空）">
        <a-input-number v-model:value="form.max_output_tokens" :min="0" style="width: 100%" />
      </a-form-item>
      <a-form-item label="强制参数覆盖（可选，转发时覆盖下游同名字段）">
        <div v-for="(row, i) in form.overrides" :key="i" style="display: flex; gap: 8px; margin-bottom: 8px">
          <a-input v-model:value="row.key" placeholder="参数名，如 temperature" style="width: 42%" />
          <a-input v-model:value="row.value" placeholder="值，如 1 / true / 字符串" style="flex: 1" />
          <a-button type="text" danger @click="form.overrides.splice(i, 1)">删除</a-button>
        </div>
        <a-button type="dashed" block @click="form.overrides.push({ key: '', value: '' })">添加参数</a-button>
        <p class="dim" style="margin-top: 4px">数值与 true/false 会按原类型发送，其余按字符串发送</p>
      </a-form-item>
      <a-form-item label="绑定模型">
        <div v-for="g in modelGroups" :key="g.channel" style="margin-bottom: 8px">
          <div class="dim" style="margin-bottom: 4px">{{ g.channel }}</div>
          <a-checkbox
            v-for="opt in g.options"
            :key="opt.value"
            :checked="form.model_ids.includes(opt.value)"
            :style="{ marginRight: '8px' }"
            @change="toggleModelId(opt.value)"
          >{{ opt.label }}</a-checkbox>
        </div>
        <p v-if="!modelGroups.length" class="dim">暂无模型实体，请先在渠道下创建模型</p>
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup>
import { computed, reactive, ref } from 'vue'
import { api } from '../api'
import { guard, store } from '../store'

const emit = defineEmits(['saved'])
const visible = ref(false)
const saving = ref(false)
const emptyForm = () => ({
  id: null, name: '', context_length: null, max_output_tokens: null, model_ids: [], overrides: [],
})
const form = reactive(emptyForm())

const modelGroups = computed(() => {
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

// 文本 → 原始类型：数字/true/false/null 按字面量，JSON 可解析的按 JSON，其余按字符串
function parseOverrideValue(text) {
  const t = String(text).trim()
  if (t === 'true') return true
  if (t === 'false') return false
  if (t === 'null') return null
  if (t !== '' && !Number.isNaN(Number(t))) return Number(t)
  try { return JSON.parse(t) } catch { return text }
}

function overrideKeys(m) {
  return Object.keys(m.parameter_overrides || {})
}

function toggleModelId(id) {
  const i = form.model_ids.indexOf(id)
  if (i === -1) form.model_ids.push(id)
  else form.model_ids.splice(i, 1)
}

function show(m) {
  if (m) {
    Object.assign(form, {
      id: m.id, name: m.name,
      context_length: m.context_length, max_output_tokens: m.max_output_tokens,
      model_ids: (m.bound_models || []).map(x => x.id),
      overrides: overrideKeys(m).map(k => ({ key: k, value: JSON.stringify(m.parameter_overrides[k]) ?? '' })),
    })
  } else {
    Object.assign(form, emptyForm())
  }
  visible.value = true
}

async function save() {
  saving.value = true
  try {
    await guard(async () => {
      const parameter_overrides = {}
      for (const row of form.overrides) {
        const key = row.key.trim()
        if (key) parameter_overrides[key] = parseOverrideValue(row.value)
      }
      const body = {
        name: form.name,
        context_length: form.context_length || null,
        max_output_tokens: form.max_output_tokens || null,
        model_ids: form.model_ids,
        parameter_overrides,
      }
      if (form.id) await api('/mappings/' + form.id, { method: 'PUT', body })
      else await api('/mappings', { method: 'POST', body })
      visible.value = false
      emit('saved')
    })
  } finally {
    saving.value = false
  }
}

defineExpose({ show })
</script>
