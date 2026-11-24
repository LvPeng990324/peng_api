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
      <a-form-item v-if="quotaNeedsToken" label="访问令牌">
        <a-input v-model:value="form.quota_token" placeholder="额度接口鉴权用令牌，如 nap_xxx" />
      </a-form-item>
      <a-form-item label="优先级（越大越优先）">
        <a-input-number v-model:value="form.priority" :min="0" style="width: 100%" />
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
  id: null, name: '', base_url: '', api_key: '', quota_type: '', quota_token: '',
  priority: 0, enabled: true, remark: '',
})
const form = reactive(emptyForm())

// 额度接口需要专用令牌的类型（new-api）才显示访问令牌输入框，由后端 /api/quota-types 下发
const quotaNeedsToken = computed(() =>
  store.quotaTypes.some(t => t.value === form.quota_type && t.requires_token),
)

function show(c) {
  if (c) {
    Object.assign(form, {
      id: c.id, name: c.name, base_url: c.base_url, api_key: c.api_key,
      quota_type: c.quota_type || '', quota_token: c.quota_token || '',
      priority: c.priority, enabled: c.enabled, remark: c.remark,
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
