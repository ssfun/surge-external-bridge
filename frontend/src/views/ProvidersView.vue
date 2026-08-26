<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import PageHeader from '@/components/PageHeader.vue'
import ProviderDialog from '@/components/ProviderDialog.vue'
import { api, encodeID } from '@/api.js'
import { useDataStore } from '@/stores/data.js'
import { useUIStore } from '@/stores/ui.js'
import { formatDateTime, formatDuration, providerTypeLabel } from '@/utils.js'

const data = useDataStore()
const ui = useUIStore()
const { providers, nodes } = storeToRefs(data)
const expanded = reactive(new Set())
const loadingRuntime = reactive(new Set())
const busy = reactive(new Set())
const dialogOpen = ref(false)
const editing = ref(null)
const menuOpen = ref('')
const enabledCount = computed(() => providers.value.filter((provider) => provider.enabled).length)
const errorCount = computed(() => providers.value.filter((provider) => provider.last_error || provider.runtimeError).length)

watch(() => data.loadedAt.providers, () => {
  for (const id of expanded) data.loadProviderRuntime(id, { quiet: true })
})

function open(provider = null) { menuOpen.value = ''; editing.value = provider; dialogOpen.value = true }
function closeDialog() { dialogOpen.value = false; editing.value = null }
function proxyList(provider) { return Array.isArray(provider.runtime?.proxies) ? provider.runtime.proxies : [] }
function providerCount(provider) { return proxyList(provider).length || provider.runtime?.count || nodes.value.filter((node) => node.provider_id === provider.stable_id).length }
function lastDelay(proxy) { return [...(Array.isArray(proxy.history) ? proxy.history : [])].reverse().find((item) => item.delay)?.delay }
function sourceLabel(provider) { return provider.type === 'http' ? (provider.url || '订阅地址已隐藏') : provider.type === 'file' ? 'Mihomo 私有目录文件' : '内联节点配置' }
function filterLabel(provider) {
  const values = []
  if (provider.include_name) values.push(`包含 ${provider.include_name}`)
  if (provider.exclude_name) values.push(`排除 ${provider.exclude_name}`)
  return values.join(' · ')
}
function updatedAt(provider) { return provider.runtime?.updatedAt || provider.runtime?.updated_at }
function hasSubscriptionInfo(provider) { return Boolean(provider.runtime?.subscriptionInfo || provider.runtime?.subscription_info) }
function toggleMenu(id) { menuOpen.value = menuOpen.value === id ? '' : id }
function closeMenu() { menuOpen.value = '' }
onMounted(() => document.addEventListener('click', closeMenu))
onBeforeUnmount(() => document.removeEventListener('click', closeMenu))

async function toggle(provider) {
  if (expanded.has(provider.stable_id)) expanded.delete(provider.stable_id)
  else {
    expanded.add(provider.stable_id)
    if (provider.runtime) return
    loadingRuntime.add(provider.stable_id)
    try { await data.loadProviderRuntime(provider.stable_id) } catch (error) { ui.toast(`Provider 运行状态加载失败：${error.message}`, true) }
    finally { loadingRuntime.delete(provider.stable_id) }
  }
}

async function action(provider, name) {
  const id = provider.stable_id
  if (busy.has(id)) return
  busy.add(id)
  try {
    let message = ''
    if (name === 'delete') {
      if (!window.confirm(`删除 Provider“${provider.name}”？对应身份会立即失效，相关既有连接可能结束。`)) return
      await data.deleteProvider(id)
      expanded.delete(id)
      message = `已删除 Provider“${provider.name}”`
    } else if (name === 'refresh') {
      await data.refreshProvider(id)
      message = `Provider“${provider.name}”已刷新`
    } else if (name === 'health') {
      await data.healthCheckProvider(id)
      message = `Provider“${provider.name}”已启动健康检查`
    }
    if (message) ui.toast(message)
  } catch (error) { ui.toast(error.message, true) }
  finally { busy.delete(id) }
}

async function copySecrets(provider) {
  menuOpen.value = ''
  if (!window.confirm(`读取并复制 Provider“${provider.name}”的订阅 URL 与 Header？`)) return
  try {
    const secrets = await api(`/api/providers/${encodeID(provider.stable_id)}/secrets`, { headers: { 'X-SurgeEB-Confirm': 'reveal-provider-secrets' } })
    await navigator.clipboard.writeText(JSON.stringify(secrets, null, 2))
    ui.toast('敏感字段已复制')
  } catch (error) { ui.toast(error.message, true) }
}

function runAction(provider, name) {
  menuOpen.value = ''
  return action(provider, name)
}
</script>

<template>
  <PageHeader eyebrow="PROVIDERS" title="订阅与节点来源" description="添加和管理节点来源；Mihomo 成功加载后会自动发布给 Surge。">
    <button class="button primary" type="button" @click="open()">添加 Provider</button>
  </PageHeader>

  <div v-if="providers.length" class="provider-summary" data-testid="provider-summary">
    <span><b>{{ enabledCount }}</b> / {{ providers.length }} 已启用</span>
    <span><b>{{ nodes.length }}</b> 个发布节点</span>
    <span v-if="errorCount" class="bad"><b>{{ errorCount }}</b> 个需要处理</span>
  </div>

  <div class="sub-list">
    <article v-for="provider in providers" :key="provider.stable_id" class="sub-card" :class="{ off: !provider.enabled }" data-testid="provider-card" :data-provider-id="provider.stable_id">
      <div class="provider-card-head">
        <div class="provider-identity">
          <div class="provider-title-line">
            <h3>{{ provider.name }}</h3>
            <span class="pill" :class="provider.last_error || provider.runtimeError ? 'bad' : provider.enabled ? 'ok' : ''">{{ provider.last_error || provider.runtimeError ? '需要处理' : provider.enabled ? '已启用' : '已停用' }}</span>
          </div>
          <div class="provider-source"><span>{{ providerTypeLabel(provider.type) }}</span><code>{{ sourceLabel(provider) }}</code></div>
        </div>
        <div class="provider-actions">
          <button class="button ghost" type="button" :disabled="busy.has(provider.stable_id)" @click="open(provider)">编辑</button>
          <button v-if="provider.type !== 'inline' && provider.enabled" class="button ghost" type="button" :disabled="busy.has(provider.stable_id)" :aria-busy="busy.has(provider.stable_id)" @click="runAction(provider, 'refresh')">刷新</button>
          <div class="provider-menu" @click.stop @keydown.esc.prevent.stop="menuOpen = ''">
            <button class="button ghost provider-more" type="button" aria-haspopup="menu" :aria-expanded="menuOpen === provider.stable_id" @click.stop="toggleMenu(provider.stable_id)">更多</button>
            <div v-if="menuOpen === provider.stable_id" class="provider-menu-panel" role="menu">
              <button v-if="provider.health_check && provider.enabled" class="button ghost" type="button" role="menuitem" :disabled="busy.has(provider.stable_id)" @click="runAction(provider, 'health')">运行健康检查</button>
              <button v-if="provider.type === 'http'" class="button ghost" type="button" role="menuitem" @click="copySecrets(provider)">复制 URL / Header</button>
              <button class="button danger" type="button" role="menuitem" :disabled="busy.has(provider.stable_id)" @click="runAction(provider, 'delete')">删除 Provider</button>
            </div>
          </div>
        </div>
      </div>

      <div class="provider-facts">
        <div><b>{{ providerCount(provider) }}</b><span>节点</span></div>
        <div v-if="provider.type === 'http'"><b>{{ formatDuration(provider.refresh_seconds) }}</b><span>刷新周期</span></div>
        <div v-if="provider.enabled && provider.next_refresh_at"><b>{{ formatDateTime(provider.next_refresh_at) }}</b><span>下次刷新</span></div>
        <div v-else-if="provider.health_check"><b>{{ provider.enabled ? '已开启' : '启用后生效' }}</b><span>健康检查</span></div>
      </div>

      <div v-if="filterLabel(provider) || provider.header_names?.length || hasSubscriptionInfo(provider)" class="provider-secondary">
        <span v-if="filterLabel(provider)"><b>筛选</b>{{ filterLabel(provider) }}</span>
        <span v-if="provider.header_names?.length"><b>请求</b>{{ provider.header_names.length }} 个敏感 Header</span>
        <span v-if="hasSubscriptionInfo(provider)"><b>订阅</b>流量信息已同步</span>
      </div>
      <div v-if="updatedAt(provider)" class="provider-updated">最近更新 {{ formatDateTime(updatedAt(provider)) }}</div>
      <div v-if="provider.last_error || provider.runtimeError" class="provider-error"><b>最近错误</b><span>{{ provider.last_error || provider.runtimeError }}</span></div>

      <button v-if="providerCount(provider)" class="provider-disclosure" type="button" :aria-expanded="expanded.has(provider.stable_id)" :aria-busy="loadingRuntime.has(provider.stable_id)" @click="toggle(provider)">
        <span><b>节点详情</b><small>查看 Mihomo 当前节点状态</small></span>
        <span>{{ providerCount(provider) }} 个 <i aria-hidden="true" :class="{ open: expanded.has(provider.stable_id) }" /></span>
      </button>
      <div v-if="expanded.has(provider.stable_id)" class="provider-detail">
        <div v-if="loadingRuntime.has(provider.stable_id)" class="provider-detail-state">正在读取 Mihomo 节点…</div>
        <div v-else-if="provider.runtimeError" class="provider-detail-state bad">{{ provider.runtimeError }}</div>
        <div v-else-if="proxyList(provider).length" class="provider-node-list">
          <div v-for="(proxy, index) in proxyList(provider)" :key="`${proxy.name}-${index}`" class="provider-node-row">
            <div><b>{{ proxy.name || '未命名' }}</b><span>{{ proxy.type || '—' }}</span></div>
            <div><span class="pill" :class="proxy.alive ? 'ok' : 'warn'">{{ proxy.alive ? '存活' : '未知 / 失败' }}</span><span>{{ lastDelay(proxy) ? `${lastDelay(proxy)} ms` : '暂无延迟' }}</span></div>
          </div>
        </div>
        <div v-else class="provider-detail-state">当前没有可显示的 Mihomo 节点。</div>
      </div>
    </article>

    <div v-if="!providers.length" class="card empty-state"><div class="empty-state-icon">+</div><b>还没有 Provider</b><span>添加订阅 URL 后，Mihomo 会原生解析 Clash YAML、URI 列表或 Base64 URI。</span><div class="actions"><button class="button primary" type="button" @click="open()">添加第一个 Provider</button></div></div>
  </div>
  <ProviderDialog :open="dialogOpen" :provider="editing" @close="closeDialog" @saved="closeDialog" />
</template>
