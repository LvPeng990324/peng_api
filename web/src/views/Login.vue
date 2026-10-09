<template>
<div class="login-wrap">
  <a-card class="login-card">
    <div class="logo">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M13 2 3 14h7l-1 8 10-12h-7l1-8z"/></svg>
      <h2>PengAPI</h2>
    </div>
    <a-input-password
      v-model:value="password"
      placeholder="管理密码"
      size="large"
      autofocus
      @keyup.enter="login"
    />
    <a-button type="primary" size="large" block :loading="loading" @click="login">登录</a-button>
    <p v-if="loginErr" class="err">{{ loginErr }}</p>
  </a-card>
</div>
</template>

<script setup>
import { ref } from 'vue'
import { api } from '../api'
import { loadAll, store } from '../store'

const password = ref('')
const loginErr = ref('')
const loading = ref(false)

async function login() {
  loginErr.value = ''
  loading.value = true
  try {
    await api('/login', { method: 'POST', body: { password: password.value } })
    store.authed = true
    password.value = ''
    await loadAll()
  } catch (e) {
    loginErr.value = '登录失败：' + e.message
  } finally {
    loading.value = false
  }
}
</script>
