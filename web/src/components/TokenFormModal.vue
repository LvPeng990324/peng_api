<template>
  <a-modal v-model:open="open" title="编辑 Token" :confirm-loading="saving" @ok="save">
    <a-form layout="vertical">
      <a-form-item label="名称">
        <a-input v-model:value="form.name" placeholder="如 cline" @keyup.enter="save" />
      </a-form-item>
      <a-form-item label="启用">
        <a-switch v-model:checked="form.enabled" checked-children="启用" un-checked-children="禁用" />
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup>
import { reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import { api } from '../api'
import { guard } from '../store'

const emit = defineEmits(['saved'])
const open = ref(false)
const saving = ref(false)

const form = reactive({ id: null, name: '', enabled: true })

function show(t) {
  Object.assign(form, { id: t.id, name: t.name, enabled: t.enabled })
  open.value = true
}

async function save() {
  if (!form.name.trim()) {
    message.warning('名称不能为空')
    return
  }
  saving.value = true
  try {
    await guard(async () => {
      await api('/tokens/' + form.id, {
        method: 'PUT',
        body: { name: form.name.trim(), enabled: form.enabled },
      })
      open.value = false
      emit('saved')
    })
  } finally {
    saving.value = false
  }
}

defineExpose({ show })
</script>
