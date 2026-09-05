<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRoute, useRouter } from 'vue-router'
import { useDataStore } from '@/stores/data.js'
import { useRealtimeStore } from '@/stores/realtime.js'
import { useUIStore } from '@/stores/ui.js'
import { shouldRefreshInBackground } from '@/refreshPolicy.js'
import LoginPage from '@/components/LoginPage.vue'
import { authState, initializeAuth, logout } from '@/api.js'

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

function navigationActive(name) {
  return route.name === name || (name === 'providers' && route.name === 'policyPaths')
}

const gateway = computed(() => overview.value?.gateway || {})
const running = computed(() => gateway.value.state === 'running')
const unavailable = computed(() => Boolean(data.resourceErrors.overview) || realtime.streamStatus === 'disconnected')
const gatewayLabel = computed(() => unavailable.value ? '连接中断，状态待确认' : running.value ? 'Mihomo 网关运行中' : 'Mihomo 网关异常')
let refreshTimer = 0
let lastBackgroundError = ''
const bootError = ref('')
const sessionError = ref('')
const sessionReady = ref(false)

async function loadRoute(name, background = false) {
  if (!sessionReady.value || !authState.authenticated) return
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
    if (authState.authenticated && shouldRefreshInBackground(document.hidden, realtime.paused)) await loadRoute(route.name, true)
    scheduleBackgroundRefresh()
  }, realtime.reduced ? 30000 : 15000)
}

function handleVisibility() {
  if (!authState.authenticated) return
  realtime.sync(route.name)
  if (shouldRefreshInBackground(document.hidden, realtime.paused)) loadRoute(route.name, true)
}

watch([() => route.name, () => authState.authenticated, sessionReady], ([name, authenticated, ready]) => {
  if (ready && authenticated) {
    realtime.sync(name)
    loadRoute(name)
  } else {
    realtime.stop()
  }
}, { immediate: true })

watch(() => realtime.reduced, scheduleBackgroundRefresh)

watch(() => realtime.paused, (paused) => {
  if (!paused && !document.hidden) loadRoute(route.name, true)
})

onMounted(() => {
  document.addEventListener('visibilitychange', handleVisibility)
  scheduleBackgroundRefresh()
  initializeSession()
})

onBeforeUnmount(() => {
  window.clearTimeout(refreshTimer)
  document.removeEventListener('visibilitychange', handleVisibility)
  realtime.stop()
})

function authenticated() {
  sessionError.value = ''
  sessionReady.value = true
}

async function initializeSession() {
  sessionError.value = ''
  sessionReady.value = false
  try {
    await initializeAuth()
  } catch (error) {
    sessionError.value = error.message
  } finally {
    sessionReady.value = true
  }
}

async function retryConnection() {
  if (!authState.authenticated) {
    await initializeSession()
    return
  }
  await loadRoute(route.name)
}

async function signOut() {
  if (!window.confirm('退出当前配置台会话？')) return
  try {
    await logout()
    realtime.resetSession()
    data.resetSession()
    bootError.value = ''
  } catch (error) {
    ui.toast(`退出登录失败：${error.message}`, true)
  }
}
</script>

<template>
  <div v-if="!sessionReady || authState.checking" class="loading auth-loading"><span class="loading-dot" /><b>正在检查登录状态</b><small>建立安全会话…</small></div>
  <LoginPage v-else-if="!sessionError && authState.required && !authState.authenticated" @authenticated="authenticated" />
  <div v-else class="shell">
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
          :class="{ active: navigationActive(item[0]) }"
          :aria-current="navigationActive(item[0]) ? 'page' : undefined"
          @click="router.push({ name: item[0] })"
        >
          <b :data-short="item[2]">{{ item[1] }}</b><span>{{ item[2] }}</span>
        </button>
      </nav>
      <div class="side-status">
        <div class="engine-line"><i :class="running && !unavailable ? 'ok' : 'bad'" /><span>{{ gatewayLabel }}</span></div>
        <code>socks {{ gateway.socks_address || '—' }}</code>
        <small>{{ gateway.projection_count || 0 }} 个发布节点</small>
        <button v-if="authState.required" class="logout-link" type="button" @click="signOut">退出登录</button>
      </div>
      <button v-if="authState.required" class="button ghost compact mobile-logout" type="button" @click="signOut">退出登录</button>
    </aside>
    <main id="main-content" tabindex="-1">
      <div v-if="unavailable && overview" class="banner bad" role="status"><b>连接中断</b><span>正在尝试重新连接，以下为最后收到的数据。</span></div>
      <div v-if="data.pendingRefresh.length" class="banner" role="status" data-testid="refresh-warning"><b>操作已成功，部分状态尚未刷新</b><span>无需重复提交，后台会重试读取。</span><button class="button" @click="data.retryPendingRefresh()">重新读取状态</button></div>
      <RouterView v-if="overview" v-slot="{ Component }">
        <component :is="Component" :key="route.name" />
      </RouterView>
      <div v-else-if="bootError || sessionError" class="card empty-state"><div class="empty-state-icon">!</div><b>无法连接配置台</b><span>{{ bootError || sessionError }}</span><div class="actions"><button class="button primary" type="button" @click="retryConnection">重新连接</button></div></div>
      <div v-else class="loading"><span class="loading-dot" /><b>正在连接配置台</b><small>读取网关状态…</small></div>
    </main>
  </div>
  <div id="toasts" aria-live="polite" aria-atomic="true">
    <div v-for="item in toasts" :key="item.id" class="toast" :class="{ bad: item.bad }" :role="item.bad ? 'alert' : 'status'">{{ item.message }}</div>
  </div>
  <div id="announcer" class="sr-only" aria-live="polite">{{ announcement }}</div>
</template>
