<template>
<a-config-provider :locale="zhCN" :theme="{ algorithm: theme.darkAlgorithm }">
  <Login v-if="!store.authed" />
  <a-layout v-else class="layout">
    <a-layout-sider v-model:collapsed="siderCollapsed" class="sider" :width="220" breakpoint="lg" collapsed-width="0" :trigger="null">
      <div class="brand">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M13 2 3 14h7l-1 8 10-12h-7l1-8z"/></svg>
        <span>PengAPI</span>
      </div>
      <a-menu theme="dark" mode="inline" :selected-keys="[store.tab]" @click="onMenu">
        <a-menu-item v-for="t in tabs" :key="t.key">{{ t.label }}</a-menu-item>
        <a-menu-item key="__logout" class="logout-item">退出</a-menu-item>
      </a-menu>
    </a-layout-sider>
    <a-layout>
      <a-layout-header class="topbar">
        <button class="sider-toggle" aria-label="菜单" @click="siderCollapsed = !siderCollapsed">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M4 6h16M4 12h16M4 18h16"/></svg>
        </button>
        <h2>{{ tabLabel }}</h2>
      </a-layout-header>
      <a-layout-content class="content">
        <Channels v-if="store.tab === 'channels'" />
        <Models v-else-if="store.tab === 'models'" />
        <Mappings v-else-if="store.tab === 'mappings'" />
        <Tokens v-else-if="store.tab === 'tokens'" />
        <Logs v-else-if="store.tab === 'logs'" />
      </a-layout-content>
    </a-layout>
  </a-layout>
</a-config-provider>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import zhCN from 'ant-design-vue/es/locale/zh_CN'
import { theme } from 'ant-design-vue'
import { api, setUnauthorizedHandler } from './api'
import { guard, loadAll, store, tabs } from './store'
import Login from './views/Login.vue'
import Channels from './views/Channels.vue'
import Models from './views/Models.vue'
import Mappings from './views/Mappings.vue'
import Tokens from './views/Tokens.vue'
import Logs from './views/Logs.vue'

const siderCollapsed = ref(false)

// 窄屏（低于 sider 的 lg 断点）下切换侧边栏，展开态下点菜单项自动收起
const isNarrow = () => window.matchMedia('(max-width: 991.5px)').matches

const tabLabel = computed(() => (tabs.find(t => t.key === store.tab) || {}).label || '')

function onMenu({ key }) {
  if (key === '__logout') { logout(); return }
  store.tab = key
  if (isNarrow()) siderCollapsed.value = true
}

async function logout() {
  await guard(async () => { await api('/logout', { method: 'POST' }) })
  store.authed = false
}

onMounted(async () => {
  setUnauthorizedHandler(() => { store.authed = false })
  try {
    await api('/me')
    store.authed = true
    await guard(loadAll)
  } catch (e) { /* 未登录，停在登录页 */ }
})
</script>
