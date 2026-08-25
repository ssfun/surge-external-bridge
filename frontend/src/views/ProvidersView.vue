<script setup>
import { reactive, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import PageHeader from '@/components/PageHeader.vue'
import ProviderDialog from '@/components/ProviderDialog.vue'
import { api, encodeID } from '@/api.js'
import { useDataStore } from '@/stores/data.js'
import { useUIStore } from '@/stores/ui.js'
import { formatBytes, formatDateTime, providerTypeLabel } from '@/utils.js'

const data = useDataStore()
const ui = useUIStore()
const { providers, nodes } = storeToRefs(data)
const expanded = reactive(new Set())
const busy = reactive(new Set())
const dialogOpen = ref(false)
const editing = ref(null)

watch(() => data.loadedAt.providers, () => {
  for (const id of expanded) data.loadProviderRuntime(id, { quiet: true })
})

function open(provider = null) { editing.value = provider; dialogOpen.value = true }
function closeDialog() { dialogOpen.value = false; editing.value = null }
function proxyList(provider) { return Array.isArray(provider.runtime?.proxies) ? provider.runtime.proxies : [] }
function providerCount(provider) { return proxyList(provider).length || provider.runtime?.count || nodes.value.filter((node) => node.provider_id === provider.stable_id).length }
function lastDelay(proxy) { return [...(Array.isArray(proxy.history) ? proxy.history : [])].reverse().find((item) => item.delay)?.delay }

async function toggle(provider) {
  if (expanded.has(provider.stable_id)) expanded.delete(provider.stable_id)
  else {
    expanded.add(provider.stable_id)
    try { await data.loadProviderRuntime(provider.stable_id) } catch (error) { ui.toast(`Provider 运行状态加载失败：${error.message}`, true) }
  }
}

async function action(provider, name) {
  const id = provider.stable_id
  if (busy.has(id)) return
  busy.add(id)
  try {
    if (name === 'delete') {
      if (!window.confirm(`删除 Provider“${provider.name}”？对应身份会立即失效，相关既有连接可能结束。`)) return
      await data.deleteProvider(id)
      expanded.delete(id)
    } else if (name === 'refresh') await data.refreshProvider(id)
    else if (name === 'health') await data.healthCheckProvider(id)
    ui.toast('操作完成')
  } catch (error) { ui.toast(error.message, true) }
  finally { busy.delete(id) }
}

async function copySecrets(provider) {
  if (!window.confirm('确认读取并复制敏感 URL 与 Header？')) return
  try {
    const secrets = await api(`/api/providers/${encodeID(provider.stable_id)}/secrets`, { headers: { 'X-SurgeEB-Confirm': 'reveal-provider-secrets' } })
    await navigator.clipboard.writeText(JSON.stringify(secrets, null, 2))
    ui.toast('敏感字段已复制')
  } catch (error) { ui.toast(error.message, true) }
}
</script>

<template>
  <PageHeader eyebrow="PROVIDERS" title="订阅与节点来源" description="订阅内容由 Mihomo 直接获取、解析和缓存；这里管理来源与 Surge 投影范围。">
    <button class="button primary" type="button" @click="open()">添加 Provider</button>
  </PageHeader>

  <div class="sub-list">
    <article v-for="provider in providers" :key="provider.stable_id" class="sub-card" :class="{ off: !provider.enabled }">
      <div><span class="pill" :class="{ ok: provider.enabled }">{{ provider.enabled ? '已启用' : '已停用' }}</span></div>
      <div>
        <h3>{{ provider.name }}</h3>
        <div class="meta">{{ provider.type === 'http' ? (provider.url || '订阅地址已隐藏') : provider.type === 'file' ? 'Mihomo 私有目录文件' : '内联节点配置' }}</div>
        <div class="chips">
          <span class="chip">{{ providerTypeLabel(provider.type) }}</span><span class="chip">{{ providerCount(provider) }} 个节点</span>
          <template v-if="provider.type === 'http'"><span class="chip">{{ Math.round((provider.refresh_seconds || 0) / 60) }} 分钟刷新</span><span class="chip">上限 {{ formatBytes(provider.size_limit) }}</span></template>
          <span v-if="provider.health_check" class="chip ok">自动健康检查</span><span v-if="provider.header_names?.length" class="chip">{{ provider.header_names.length }} 个敏感 Header</span>
        </div>
        <div class="provider-meta-line">
          <span>最近更新 {{ formatDateTime(provider.runtime?.updatedAt || provider.runtime?.updated_at) }}</span>
          <span v-if="provider.type === 'http'">下次刷新 {{ formatDateTime(provider.next_refresh_at) }}</span>
          <span>{{ provider.runtime?.subscriptionInfo || provider.runtime?.subscription_info ? '订阅流量信息已同步' : '暂无订阅流量信息' }}</span>
          <span>投影：包含 {{ provider.include_name || '全部' }} · 排除 {{ provider.exclude_name || '无' }}</span>
        </div>
        <div v-if="provider.last_error || provider.runtimeError" class="provider-error"><b>最近错误</b><span>{{ provider.last_error || provider.runtimeError }}</span></div>
        <div v-if="expanded.has(provider.stable_id)" class="provider-detail">
          <div class="code-head"><span>Mihomo 当前节点（只读）</span><span>{{ proxyList(provider).length }} 项</span></div>
          <div class="table-wrap"><table><thead><tr><th>节点</th><th>协议</th><th>状态</th><th>延迟</th></tr></thead><tbody>
            <tr v-for="proxy in proxyList(provider)" :key="proxy.name"><td><b>{{ proxy.name || '未命名' }}</b></td><td>{{ proxy.type || '—' }}</td><td><span class="pill" :class="proxy.alive ? 'ok' : 'warn'">{{ proxy.alive ? '存活' : '未知/失败' }}</span></td><td>{{ lastDelay(proxy) ? `${lastDelay(proxy)} ms` : '—' }}</td></tr>
            <tr v-if="!proxyList(provider).length"><td colspan="4">当前没有节点。</td></tr>
          </tbody></table></div>
        </div>
      </div>
      <div class="provider-actions">
        <button class="button ghost" type="button" :aria-expanded="expanded.has(provider.stable_id)" @click="toggle(provider)">{{ expanded.has(provider.stable_id) ? '收起节点' : '查看节点' }}</button>
        <button v-if="provider.type !== 'inline'" class="button ghost" type="button" :disabled="!provider.enabled || busy.has(provider.stable_id)" @click="action(provider, 'refresh')">立即刷新</button>
        <details class="action-menu"><summary class="button ghost">更多</summary><div class="action-menu-panel">
          <button class="button ghost" type="button" @click="open(provider)">编辑设置</button>
          <button v-if="provider.health_check" class="button ghost" type="button" :disabled="!provider.enabled || busy.has(provider.stable_id)" @click="action(provider, 'health')">健康检查</button>
          <button class="button ghost" type="button" @click="copySecrets(provider)">复制敏感字段</button>
          <button class="button danger" type="button" :disabled="busy.has(provider.stable_id)" @click="action(provider, 'delete')">删除 Provider</button>
        </div></details>
      </div>
    </article>

    <div v-if="!providers.length" class="card empty-state"><div class="empty-state-icon">+</div><b>还没有 Provider</b><span>添加订阅 URL 后，Mihomo 会原生解析 Clash YAML、URI 列表或 Base64 URI。</span><div class="actions"><button class="button primary" type="button" @click="open()">添加第一个 Provider</button></div></div>
  </div>
  <ProviderDialog :open="dialogOpen" :provider="editing" @close="closeDialog" @saved="closeDialog" />
</template>
