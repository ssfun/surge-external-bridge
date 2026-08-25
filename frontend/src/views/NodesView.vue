<script setup>
import { computed, reactive, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import PageHeader from '@/components/PageHeader.vue'
import LiveControls from '@/components/LiveControls.vue'
import { api, encodeID } from '@/api.js'
import { useDataStore } from '@/stores/data.js'
import { useRealtimeStore } from '@/stores/realtime.js'
import { useUIStore } from '@/stores/ui.js'
import { formatBytes, nodeConnectionStats, nodeHistory } from '@/utils.js'

const router = useRouter()
const data = useDataStore()
const realtime = useRealtimeStore()
const ui = useUIStore()
const { nodes, providers } = storeToRefs(data)
const { connections } = storeToRefs(realtime)
const name = ref('')
const provider = ref('')
const type = ref('')
const alive = ref('')
const results = reactive({})
const errors = reactive({})
const busy = reactive(new Set())

const providerOptions = computed(() => [...new Map(nodes.value.map((node) => [node.provider_id, node.provider_name])).entries()])
const types = computed(() => [...new Set(nodes.value.map((node) => node.type))])
const filtered = computed(() => nodes.value.filter((node) =>
  (!name.value || node.name.toLowerCase().includes(name.value.toLowerCase())) &&
  (!provider.value || node.provider_id === provider.value) &&
  (!type.value || node.type === type.value) &&
  (!alive.value || String(Boolean(node.alive)) === alive.value)))
const hasFilters = computed(() => Boolean(name.value || provider.value || type.value || alive.value))

function clearFilters() { name.value = ''; provider.value = ''; type.value = ''; alive.value = '' }
function capabilities(node) { return [node.udp && 'UDP', node.uot && 'UOT', node.xudp && 'XUDP', node.tfo && 'TFO', node.mptcp && 'MPTCP', node.smux && 'SMUX'].filter(Boolean).join(' ') || '—' }
function stats(node) { return nodeConnectionStats(node, connections.value.connections || []) }
function diagnosticError(node) {
  if (errors[node.id]) return errors[node.id]
  const result = results[node.id]
  return result ? ['tcp', 'udp'].filter((protocol) => result[protocol] && !result[protocol].success).map((protocol) => `${protocol.toUpperCase()}: ${result[protocol].detail || '失败'}`).join('；') : ''
}

async function nodeAction(node, action) {
  if (busy.has(node.id)) return
  busy.add(node.id)
  try {
    if (action === 'diagnose') {
      results[node.id] = await api(`/api/nodes/${encodeID(node.id)}/diagnose`, { method: 'POST' })
      delete errors[node.id]
    } else if (action === 'health') {
      const result = await api(`/api/nodes/${encodeID(node.id)}/healthcheck`, { method: 'POST' })
      await data.reloadResources(['nodes'])
      ui.toast(`Mihomo 延迟 ${result.delay || '—'} ms`)
    } else if (action === 'copy') {
      if (!window.confirm('单节点行包含 SOCKS 密码，确认复制？')) return
      const result = await api(`/api/nodes/${encodeID(node.id)}/surge-line`, { headers: { 'X-SurgeEB-Confirm': 'reveal-node-credential' } })
      await navigator.clipboard.writeText(result.line)
      ui.toast('Surge 节点行已复制')
    }
  } catch (error) {
    if (action === 'diagnose') errors[node.id] = error.message
    ui.toast(error.message, true)
  } finally { busy.delete(node.id) }
}
</script>

<template>
  <PageHeader eyebrow="NODES" title="当前投影节点" description="查看 Mihomo 健康状态、实时连接与项目内核端到端诊断；诊断不经过 Surge。"><LiveControls /></PageHeader>

  <template v-if="nodes.length">
    <div class="filter-bar">
      <input v-model="name" class="search" aria-label="按节点名称过滤" placeholder="按名称过滤">
      <select v-model="provider" class="search" aria-label="按 Provider 过滤"><option value="">全部 Provider</option><option v-for="item in providerOptions" :key="item[0]" :value="item[0]">{{ item[1] }}</option></select>
      <select v-model="type" class="search" aria-label="按协议过滤"><option value="">全部协议</option><option v-for="item in types" :key="item" :value="item">{{ item.toUpperCase() }}</option></select>
      <select v-model="alive" class="search" aria-label="按存活状态过滤"><option value="">全部存活状态</option><option value="true">存活</option><option value="false">不可用 / 未知</option></select>
    </div>
    <div class="filter-summary"><span>显示 {{ filtered.length }} / {{ nodes.length }} 个节点</span><button v-if="hasFilters" type="button" @click="clearFilters">清除筛选</button></div>

    <div v-if="filtered.length" class="table-wrap responsive-table"><table class="node-table"><thead><tr><th>节点</th><th>Provider</th><th>状态 / 延迟</th><th>能力</th><th>连接 / 流量 / 链</th><th>诊断 / 最近错误</th><th>操作</th></tr></thead><tbody>
      <tr v-for="node in filtered" :key="node.id">
        <td data-label="节点"><b>{{ node.name }}</b><div class="meta">{{ node.id }}</div></td>
        <td data-label="Provider">{{ node.provider_name }}</td>
        <td data-label="状态 / 延迟"><span class="pill" :class="node.alive ? 'ok' : 'warn'">{{ String(node.type).toUpperCase() }} · {{ node.alive ? '存活' : '未知 / 失败' }}</span><div class="meta">延迟 {{ nodeHistory(node) }}</div></td>
        <td data-label="能力">{{ capabilities(node) }}</td>
        <td data-label="连接 / 流量"><b>{{ stats(node).count }} 个连接</b><div class="meta">↑ {{ formatBytes(stats(node).upload) }} · ↓ {{ formatBytes(stats(node).download) }}</div><div class="meta">{{ stats(node).chains.join(' → ') || '暂无代理链' }}</div></td>
        <td data-label="诊断结果">
          <div v-if="results[node.id]" class="test-result"><div v-for="protocol in ['tcp', 'udp']" :key="protocol" class="protocol-result" :class="results[node.id][protocol]?.success ? 'ok' : 'bad'"><b>{{ protocol.toUpperCase() }} {{ results[node.id][protocol]?.success ? '通过' : '失败' }} · {{ results[node.id][protocol]?.latency_ms }} ms</b><span>{{ results[node.id][protocol]?.detail }}</span></div></div>
          <span v-else class="meta">尚未运行端到端诊断</span><div v-if="diagnosticError(node)" class="node-error">{{ diagnosticError(node) }}</div>
        </td>
        <td data-label="操作"><div class="actions"><button class="button ghost compact" type="button" :disabled="busy.has(node.id)" @click="nodeAction(node, 'health')">Mihomo 测速</button><button class="button ghost compact" type="button" :disabled="busy.has(node.id)" :aria-busy="busy.has(node.id)" @click="nodeAction(node, 'diagnose')">TCP / UDP 诊断</button><button class="button ghost compact" type="button" :disabled="busy.has(node.id)" @click="nodeAction(node, 'copy')">复制 Surge 行</button></div></td>
      </tr>
    </tbody></table></div>
    <div v-else class="card empty-state"><div class="empty-state-icon">0</div><b>没有符合筛选的节点</b><span>调整条件，或清除全部筛选后重试。</span><div class="actions"><button class="button" type="button" @click="clearFilters">清除筛选</button></div></div>
  </template>

  <div v-else class="card empty-state"><div class="empty-state-icon">{{ providers.length ? '…' : '+' }}</div><b>{{ providers.length ? '暂时没有可投影节点' : '请先添加 Provider' }}</b><span>{{ providers.length ? '检查 Provider 是否已成功更新，以及投影名称筛选是否排除了全部节点。' : 'Provider 成功加载后，节点会自动出现在这里。' }}</span><div class="actions"><button class="button primary" type="button" @click="router.push({ name: 'providers' })">{{ providers.length ? '检查 Provider' : '添加 Provider' }}</button></div></div>
</template>
