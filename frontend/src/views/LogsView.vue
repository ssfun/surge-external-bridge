<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import PageHeader from '@/components/PageHeader.vue'
import LiveControls from '@/components/LiveControls.vue'
import { useDataStore } from '@/stores/data.js'
import { useRealtimeStore } from '@/stores/realtime.js'
import { useUIStore } from '@/stores/ui.js'
import { copyText, logText } from '@/utils.js'

const data = useDataStore()
const realtime = useRealtimeStore()
const ui = useUIStore()
const { overview, providers, nodes, events } = storeToRefs(data)
const { logs } = storeToRefs(realtime)
const level = ref('')
const providerID = ref('')
const nodeID = ref('')
const query = ref('')
const logViewport = ref(null)
const unseenCount = ref(0)
let clearingLogs = false

function normalizedLevel(value) {
  const current = String(value || '').toLowerCase()
  return current === 'warning' ? 'warn' : current
}

function levelLabel(value) {
  return { debug: '调试', info: '信息', warn: '警告', error: '错误' }[normalizedLevel(value)] || '记录'
}

function matchesLog(item) {
  const provider = providers.value.find((candidate) => candidate.stable_id === providerID.value)
  const node = nodes.value.find((candidate) => candidate.id === nodeID.value)
  const text = logText(item)
  const itemLevel = normalizedLevel(item.level)
  return (!level.value || itemLevel === normalizedLevel(level.value)) &&
    (!provider || text.includes(provider.name.toLowerCase()) || text.includes(provider.stable_id.toLowerCase())) &&
    (!node || text.includes(node.name.toLowerCase()) || text.includes((node.proxy_name || '').toLowerCase()) || text.includes(node.id.toLowerCase())) &&
    (!query.value || text.includes(query.value.toLowerCase()))
}

const filtered = computed(() => logs.value.filter(matchesLog))
const displayedLogs = computed(() => [...filtered.value].reverse())
const hasFilters = computed(() => Boolean(level.value || providerID.value || nodeID.value || query.value))
const warningCount = computed(() => logs.value.filter((item) => normalizedLevel(item.level) === 'warn').length)
const errorCount = computed(() => logs.value.filter((item) => normalizedLevel(item.level) === 'error').length)

watch(level, (value) => realtime.setLogLevel(value))
watch([level, providerID, nodeID, query], () => {
  unseenCount.value = 0
  nextTick(() => { if (logViewport.value) logViewport.value.scrollTop = 0 })
})
watch(() => logs.value.at(-1)?._ui_id, async (current, previous) => {
  if (clearingLogs || !current || !previous || current === previous || !logViewport.value) return
  const viewport = logViewport.value
  const matchingAdded = filtered.value.filter((item) => item._ui_id > previous).length
  if (!matchingAdded) return
  const wasAtLatest = viewport.scrollTop <= 12
  const previousHeight = viewport.scrollHeight
  await nextTick()
  if (wasAtLatest) {
    viewport.scrollTop = 0
    unseenCount.value = 0
  } else {
    viewport.scrollTop += Math.max(0, viewport.scrollHeight - previousHeight)
    unseenCount.value += matchingAdded
  }
}, { flush: 'pre' })

function clearFilters() {
  level.value = ''
  providerID.value = ''
  nodeID.value = ''
  query.value = ''
}

function clearVisibleLogs() {
  clearingLogs = true
  realtime.clearLogs()
  unseenCount.value = 0
  nextTick(() => { clearingLogs = false })
}

function handleLogScroll() {
  if (logViewport.value?.scrollTop <= 12) unseenCount.value = 0
}

function showLatest() {
  if (logViewport.value) logViewport.value.scrollTop = 0
  unseenCount.value = 0
}

function eventTime(value) {
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? String(value || '刚刚') : parsed.toLocaleString()
}

async function copyDiagnostic() {
  const summary = {
    overview: overview.value,
    providers: providers.value.map(({ stable_id, name, type, enabled, runtime }) => ({ stable_id, name, type, enabled, count: runtime?.proxies?.length || 0 })),
    events: events.value.slice(-20),
    logs: logs.value.slice(-50).map(({ _ui_id, ...item }) => item),
  }
  try {
    await copyText(JSON.stringify(summary, null, 2))
    ui.toast('脱敏诊断信息已复制')
  } catch {
    ui.toast('复制失败，请检查浏览器剪贴板权限', true)
  }
}
</script>

<template>
  <PageHeader eyebrow="LOGS" title="日志" description="查看网关运行和异常记录；URL、Token、密码等敏感信息会自动隐藏。">
    <button class="button" type="button" title="复制状态、最近事件和最近 50 条日志" @click="copyDiagnostic">复制诊断信息</button>
  </PageHeader>

  <div class="log-summary" data-testid="log-summary">
    <span><b>{{ logs.length }}</b> 条运行日志</span>
    <span :class="{ bad: errorCount }"><b>{{ errorCount }}</b> 条错误</span>
    <span :class="{ warn: warningCount }"><b>{{ warningCount }}</b> 条警告</span>
    <span><b>{{ events.length }}</b> 条产品事件</span>
    <small>运行日志最多保留最近 500 条</small>
  </div>

  <section class="card log-product-card" aria-labelledby="product-events-title">
    <div class="log-section-head">
      <div><h2 id="product-events-title">最近事件</h2><p>优先查看启动、配置更新和 Provider 异常。</p></div>
      <span class="pill">{{ events.length }} 条</span>
    </div>
    <div v-if="events.length" class="product-event-list">
      <article v-for="item in [...events].reverse()" :key="`${item.time}-${item.message}`" class="product-event-row" :class="normalizedLevel(item.level)">
        <time>{{ eventTime(item.time) }}</time>
        <span class="log-level" :class="normalizedLevel(item.level)">{{ levelLabel(item.level) }}</span>
        <p>{{ item.message }}</p>
      </article>
    </div>
    <div v-else class="log-empty compact"><b>暂无产品事件</b><span>启动、设置更新和 Provider 状态变化会显示在这里。</span></div>
  </section>

  <section class="card log-detail-card" aria-labelledby="runtime-logs-title">
    <div class="log-section-head log-detail-head">
      <div><h2 id="runtime-logs-title">详细运行日志</h2><p>用于进一步排查节点连接和网关内核问题，最新记录排在最前。</p></div>
      <div class="actions"><LiveControls /><button class="button ghost" type="button" :disabled="!logs.length" @click="clearVisibleLogs">清空当前日志</button></div>
    </div>

    <div class="log-filter-panel" aria-label="日志筛选">
      <div class="log-filter-fields">
        <input v-model="query" class="search" aria-label="按关键词过滤" placeholder="搜索消息、字段、节点或 Provider">
        <select v-model="level" class="search" aria-label="按日志级别过滤"><option value="">全部级别</option><option value="error">错误</option><option value="warning">警告</option><option value="info">信息</option><option value="debug">调试</option></select>
        <select v-model="providerID" class="search" aria-label="按 Provider 过滤"><option value="">全部 Provider</option><option v-for="provider in providers" :key="provider.stable_id" :value="provider.stable_id">{{ provider.name }}</option></select>
        <select v-model="nodeID" class="search" aria-label="按节点过滤"><option value="">全部节点</option><option v-for="node in nodes" :key="node.id" :value="node.id">{{ node.name }}</option></select>
      </div>
      <div class="log-filter-summary"><span>显示 {{ filtered.length }} / {{ logs.length }} 条日志</span><button v-if="hasFilters" type="button" @click="clearFilters">清除筛选</button></div>
    </div>

    <div class="log-stream-shell">
      <button v-if="unseenCount" class="log-new-indicator" type="button" aria-live="polite" @click="showLatest">{{ unseenCount }} 条新日志 · 回到最新</button>
      <div ref="logViewport" class="log-stream" data-testid="log-stream" @scroll="handleLogScroll">
        <article v-for="item in displayedLogs" :key="item._ui_id" class="log-row" :class="normalizedLevel(item.level)" data-testid="log-row">
          <time>{{ item.time || '实时' }}</time>
          <span class="log-level" :class="normalizedLevel(item.level)">{{ levelLabel(item.level) }}</span>
          <div class="log-row-content"><p>{{ item.message || '—' }}</p><div v-if="item.fields?.length" class="log-fields"><code v-for="field in item.fields" :key="`${field.key}-${field.value}`"><b>{{ field.key }}</b>{{ field.value }}</code></div></div>
        </article>
        <div v-if="!filtered.length" class="log-empty">
          <b>{{ logs.length ? '没有符合筛选的日志' : '等待运行日志' }}</b>
          <span>{{ logs.length ? '调整关键词或筛选条件后重试。' : '网关产生新的运行记录后会实时显示在这里。' }}</span>
          <button v-if="logs.length && hasFilters" class="button ghost compact" type="button" @click="clearFilters">清除筛选</button>
        </div>
      </div>
    </div>
  </section>
</template>
