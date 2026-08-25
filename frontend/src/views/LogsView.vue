<script setup>
import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import PageHeader from '@/components/PageHeader.vue'
import LiveControls from '@/components/LiveControls.vue'
import { useDataStore } from '@/stores/data.js'
import { useRealtimeStore } from '@/stores/realtime.js'
import { useUIStore } from '@/stores/ui.js'
import { logText } from '@/utils.js'

const data = useDataStore()
const realtime = useRealtimeStore()
const ui = useUIStore()
const { overview, providers, nodes, events } = storeToRefs(data)
const { logs } = storeToRefs(realtime)
const level = ref('')
const providerID = ref('')
const nodeID = ref('')
const query = ref('')

watch(level, (value) => realtime.setLogLevel(value))
const filtered = computed(() => {
  const provider = providers.value.find((item) => item.stable_id === providerID.value)
  const node = nodes.value.find((item) => item.id === nodeID.value)
  return logs.value.filter((item) => {
    const text = logText(item)
    return (!level.value || String(item.level || '').toLowerCase() === level.value) &&
      (!provider || text.includes(provider.name.toLowerCase()) || text.includes(provider.stable_id.toLowerCase())) &&
      (!node || text.includes(node.name.toLowerCase()) || text.includes((node.proxy_name || '').toLowerCase()) || text.includes(node.id.toLowerCase())) &&
      (!query.value || text.includes(query.value.toLowerCase()))
  })
})

async function copyDiagnostic() {
  const summary = {
    overview: overview.value,
    providers: providers.value.map(({ stable_id, name, type, enabled, runtime }) => ({ stable_id, name, type, enabled, count: runtime?.proxies?.length || 0 })),
    events: events.value.slice(-20),
    logs: logs.value.slice(-50).map(({ _ui_id, ...item }) => item),
  }
  await navigator.clipboard.writeText(JSON.stringify(summary, null, 2))
  ui.toast('脱敏诊断摘要已复制')
}
</script>

<template>
  <PageHeader eyebrow="LOGS" title="结构化日志与产品事件" description="Mihomo 日志经过 URL、Header、UUID、密码、Token 和本地路径二次脱敏。"><LiveControls /><button class="button" type="button" @click="copyDiagnostic">复制诊断摘要</button></PageHeader>
  <div class="filter-bar log-filter">
    <select v-model="level" class="search" aria-label="按日志级别过滤"><option value="">全部级别</option><option v-for="item in ['debug', 'info', 'warning', 'warn', 'error']" :key="item" :value="item">{{ item }}</option></select>
    <select v-model="providerID" class="search" aria-label="按 Provider 过滤"><option value="">全部 Provider</option><option v-for="provider in providers" :key="provider.stable_id" :value="provider.stable_id">{{ provider.name }}</option></select>
    <select v-model="nodeID" class="search" aria-label="按节点过滤"><option value="">全部节点</option><option v-for="node in nodes" :key="node.id" :value="node.id">{{ node.name }}</option></select>
    <input v-model="query" class="search" aria-label="按关键词过滤" placeholder="关键词过滤">
  </div>
  <div class="grid two">
    <div class="card"><div class="code-head"><h3>Mihomo 结构化日志</h3><span>{{ filtered.length }} / {{ logs.length }}</span></div><div class="events log-events">
      <div v-for="item in [...filtered].reverse()" :key="item._ui_id" class="event" :class="item.level"><span>{{ item.time || '实时' }}</span><b>{{ item.level }}</b><span>{{ item.message }} {{ item.fields?.map((field) => `${field.key}=${field.value}`).join(' ') }}</span></div>
      <div v-if="!filtered.length" class="meta">没有符合过滤条件的日志。</div>
    </div></div>
    <div class="card"><h3>Surge External Bridge 产品事件</h3><div class="events log-events">
      <div v-for="item in [...events].reverse()" :key="`${item.time}-${item.message}`" class="event" :class="item.level"><span>{{ new Date(item.time).toLocaleString() }}</span><b>{{ item.level }}</b><span>{{ item.message }}</span></div>
      <div v-if="!events.length" class="meta">暂无产品事件</div>
    </div></div>
  </div>
</template>
