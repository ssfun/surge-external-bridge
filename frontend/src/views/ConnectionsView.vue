<script setup>
import { computed, reactive, ref } from 'vue'
import { storeToRefs } from 'pinia'
import PageHeader from '@/components/PageHeader.vue'
import LiveControls from '@/components/LiveControls.vue'
import { api, encodeID } from '@/api.js'
import { useRealtimeStore } from '@/stores/realtime.js'
import { useUIStore } from '@/stores/ui.js'
import { formatBytes, formatRate } from '@/utils.js'

const realtime = useRealtimeStore()
const ui = useUIStore()
const { connections, traffic, memory } = storeToRefs(realtime)
const query = ref('')
const busy = reactive(new Set())
const all = computed(() => connections.value.connections || [])
const filtered = computed(() => {
  const needle = query.value.toLowerCase()
  return all.value.filter((connection) => !needle || JSON.stringify(connection).toLowerCase().includes(needle))
})
function target(connection) { const item = connection.metadata || {}; return `${item.host || item.destinationIP || item.remoteDestination || '—'}:${item.destinationPort || ''}` }
function source(connection) { const item = connection.metadata || {}; return `${item.sourceIP || item.source || '—'}:${item.sourcePort || ''}` }

async function closeConnection(id) {
  busy.add(id)
  try { await api(`/api/mihomo/connections/${encodeID(id)}`, { method: 'DELETE' }); ui.toast('连接已关闭') }
  catch (error) { ui.toast(error.message, true) }
  finally { busy.delete(id) }
}
async function closeAll() {
  if (!window.confirm('关闭全部实时连接会中断当前流量，确认继续？')) return
  try { await api('/api/mihomo/connections', { method: 'DELETE', headers: { 'X-SurgeEB-Confirm': 'close-all-connections' } }); ui.toast('全部连接已关闭') }
  catch (error) { ui.toast(error.message, true) }
}
</script>

<template>
  <PageHeader eyebrow="CONNECTIONS" title="实时连接" description="查看当前流量经过的节点、规则与代理链。"><LiveControls /><button class="button danger" type="button" :disabled="!all.length" @click="closeAll">关闭全部</button></PageHeader>
  <div v-if="all.length" class="filter-bar"><input v-model="query" class="search" aria-label="过滤实时连接" placeholder="按节点、Provider、域名或 IP 过滤"></div>
  <div class="grid three section-gap">
    <div class="stat"><label>连接数</label><strong>{{ all.length }}</strong></div>
    <div class="stat"><label>实时上 / 下</label><strong>{{ formatRate(traffic.up) }} / {{ formatRate(traffic.down) }}</strong></div>
    <div class="stat"><label>内存</label><strong>{{ formatBytes(memory.inuse) }}</strong></div>
  </div>
  <div v-if="filtered.length" class="table-wrap responsive-table"><table><thead><tr><th>源 → 目标 / 网络</th><th>节点 / Provider 链</th><th>规则</th><th>流量</th><th>开始时间</th><th>操作</th></tr></thead><tbody>
    <tr v-for="connection in filtered" :key="connection.id">
      <td data-label="源 → 目标"><div class="meta">{{ source(connection) }} →</div><b>{{ target(connection) }}</b><div class="meta">{{ connection.metadata?.network || connection.metadata?.type }}</div></td>
      <td data-label="节点 / 链">{{ connection.chains?.join(' → ') || '—' }}<div class="meta">{{ connection.providerChains?.join(' → ') }}</div></td>
      <td data-label="规则">{{ connection.rule || '—' }}<div class="meta">{{ connection.rulePayload }}</div></td>
      <td data-label="流量">{{ formatBytes(connection.upload) }} ↑<br>{{ formatBytes(connection.download) }} ↓</td>
      <td data-label="开始时间">{{ connection.start ? new Date(connection.start).toLocaleString() : '—' }}</td>
      <td data-label="操作"><button class="button danger compact" type="button" :disabled="busy.has(connection.id)" @click="closeConnection(connection.id)">关闭连接</button></td>
    </tr>
  </tbody></table></div>
  <div v-else class="card empty-state"><div class="empty-state-icon">{{ all.length ? '0' : '—' }}</div><b>{{ all.length ? '没有符合筛选的连接' : '当前没有实时连接' }}</b><span>{{ all.length ? '尝试缩短关键词或清除搜索。' : '当 Surge 通过投影节点建立连接后，会实时显示在这里。' }}</span></div>
</template>
