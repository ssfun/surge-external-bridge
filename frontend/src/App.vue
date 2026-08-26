<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRoute, useRouter } from 'vue-router'
import { useDataStore } from '@/stores/data.js'
import { useRealtimeStore } from '@/stores/realtime.js'
import { useUIStore } from '@/stores/ui.js'
import { shouldRefreshInBackground } from '@/refreshPolicy.js'

const route = useRoute()
const router = useRouter()
const data = useDataStore()
const realtime = useRealtimeStore()
const ui = useUIStore()
const { overview } = storeToRefs(data)
const { toasts, announcement } = storeToRefs(ui)

const navigation = [
  ['overview', '00', '总览'],
  ['providers', '01', '订阅'],
  ['nodes', '02', '节点'],
  ['connections', '03', '连接'],
  ['logs', '04', '日志'],
  ['settings', '05', '设置'],
]

const gateway = computed(() => overview.value?.gateway || {})
const running = computed(() => gateway.value.state === 'running')
let refreshTimer = 0
let lastBackgroundError = ''
const bootError = ref('')

async function loadRoute(name, background = false) {
  try {
    await data.refreshRoute(name, { background })
    lastBackgroundError = ''
    if (!background) bootError.value = ''
  } catch (error) {
    if (!background && !overview.value) bootError.value = error.message
    if (!background || lastBackgroundError !== error.message) ui.toast(`刷新状态失败：${error.message}`, true)
    lastBackgroundError = error.message
  }
}

function scheduleBackgroundRefresh() {
  window.clearTimeout(refreshTimer)
  refreshTimer = window.setTimeout(async () => {
    if (shouldRefreshInBackground(document.hidden, realtime.paused)) await loadRoute(route.name, true)
    scheduleBackgroundRefresh()
  }, realtime.reduced ? 30000 : 15000)
}

function handleVisibility() {
  realtime.sync(route.name)
  if (shouldRefreshInBackground(document.hidden, realtime.paused)) loadRoute(route.name, true)
}

watch(() => route.name, (name) => {
  realtime.sync(name)
  loadRoute(name)
}, { immediate: true })

watch(() => realtime.reduced, scheduleBackgroundRefresh)

watch(() => realtime.paused, (paused) => {
  if (!paused && !document.hidden) loadRoute(route.name, true)
})

onMounted(() => {
  document.addEventListener('visibilitychange', handleVisibility)
  scheduleBackgroundRefresh()
})

onBeforeUnmount(() => {
  window.clearTimeout(refreshTimer)
  document.removeEventListener('visibilitychange', handleVisibility)
  realtime.stop()
})
</script>

<template>
  <div class="shell">
    <aside class="sidebar">
      <div class="brand">
        <div class="mark"><i /><i /><i /><i /></div>
        <div><strong data-testid="brand-title">SurgeEB</strong><span>MIHOMO PROTOCOL BRIDGE</span></div>
      </div>
      <nav id="nav" aria-label="主导航">
        <button
          v-for="item in navigation"
          :key="item[0]"
          type="button"
          :class="{ active: route.name === item[0] }"
          :aria-current="route.name === item[0] ? 'page' : undefined"
          @click="router.push({ name: item[0] })"
        >
          <b :data-short="item[2]">{{ item[1] }}</b><span>{{ item[2] }}</span>
        </button>
      </nav>
      <div class="side-status">
        <div class="engine-line"><i :class="running ? 'ok' : 'bad'" /><span>{{ running ? 'Mihomo 网关运行中' : 'Mihomo 网关异常' }}</span></div>
        <code>socks {{ gateway.socks_address || '—' }}</code>
        <small>{{ gateway.projection_count || 0 }} 个可用节点</small>
      </div>
    </aside>
    <main id="main-content" tabindex="-1">
      <RouterView v-if="overview" v-slot="{ Component }">
        <component :is="Component" :key="route.name" />
      </RouterView>
      <div v-else-if="bootError" class="card empty-state"><div class="empty-state-icon">!</div><b>无法连接配置台</b><span>{{ bootError }}</span><div class="actions"><button class="button primary" type="button" @click="loadRoute(route.name)">重新连接</button></div></div>
      <div v-else class="loading"><span class="loading-dot" /><b>正在连接配置台</b><small>读取网关状态…</small></div>
    </main>
  </div>
  <div id="toasts" aria-live="polite" aria-atomic="true">
    <div v-for="item in toasts" :key="item.id" class="toast" :class="{ bad: item.bad }" :role="item.bad ? 'alert' : 'status'">{{ item.message }}</div>
  </div>
  <div id="announcer" class="sr-only" aria-live="polite">{{ announcement }}</div>
</template>
