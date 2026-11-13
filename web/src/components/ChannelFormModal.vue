<template>
  <a-modal v-model:open="open" :title="form.id ? '编辑渠道' : '新建渠道'" :confirm-loading="saving" @ok="save">
    <a-form layout="vertical">
      <a-form-item label="名称"><a-input v-model:value="form.name" /></a-form-item>
      <a-form-item label="上游地址（base_url）">
        <a-input v-model:value="form.base_url" placeholder="https://api.example.com/v1" />
      </a-form-item>
      <a-form-item label="API Key"><a-input v-model:value="form.api_key" /></a-form-item>
      <a-form-item label="额度类型">
        <a-select v-model:value="form.quota_type">
          <a-select-option value="">无</a-select-option>
          <a-select-option v-for="t in store.quotaTypes" :key="t.value" :value="t.value">{{ t.label }}</a-select-option>
        </a-select>
      </a-form-item>
      <a-form-item label="优先级（越大越优先）">
        <a-input-number v-model:value="form.priority" :min="0" style="width: 100%" />
      </a-form-item>
      <a-form-item label="测试模型（连通性测试用，选便宜的）">
        <a-select v-model:value="form.test_model" allow-clear placeholder="-- 未配置 --">
          <a-select-option v-for="opt in testModelOptions" :key="opt" :value="opt">{{ opt }}</a-select-option>
        </a-select>
      </a-form-item>
      <a-form-item label="备注"><a-input v-model:value="form.remark" placeholder="选填，如用途、供应商说明" /></a-form-item>
      <a-form-item><a-checkbox v-model:checked="form.enabled">启用</a-checkbox></a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup>
import { computed, reactive, ref } from 'vue'
import { api } from '../api'
import { guard, store } from '../store'

const emit = defineEmits(['saved'])
const open = ref(false)
const saving = ref(false)

const emptyForm = () => ({
  id: null, name: '', base_url: '', api_key: '', quota_type: '',
  priority: 0, enabled: true, test_model: '', remark: '',
})
const form = reactive(emptyForm())

const testModelOptions = computed(() =>
  form.id ? store.models.filter(m => m.channel_id === form.id).map(m => m.name) : [],
)

function show(c) {
  if (c) {
    Object.assign(form, {
      id: c.id, name: c.name, base_url: c.base_url, api_key: c.api_key,
      quota_type: c.quota_type || '', priority: c.priority, enabled: c.enabled,
      test_model: c.test_model, remark: c.remark,
    })
  } else {
    Object.assign(form, emptyForm())
  }
  open.value = true
}

async function save() {
  saving.value = true
  try {
    await guard(async () => {
      const { id, ...body } = form
      if (id) await api('/channels/' + id, { method: 'PUT', body })
      else await api('/channels', { method: 'POST', body })
      open.value = false
      emit('saved')
    })
  } finally {
    saving.value = false
  }
}

defineExpose({ show })
</script>
