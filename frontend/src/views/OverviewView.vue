<script setup>
import { computed, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import PageHeader from '@/components/PageHeader.vue'
import ProviderDialog from '@/components/ProviderDialog.vue'
import { useDataStore } from '@/stores/data.js'
import { useRealtimeStore } from '@/stores/realtime.js'
import { useUIStore } from '@/stores/ui.js'
import { formatBytes, formatRate } from '@/utils.js'

const router = useRouter()
const data = useDataStore()
const realtime = useRealtimeStore()
const ui = useUIStore()
const { overview, providers, nodes } = storeToRefs(data)
const { connections, traffic, memory } = storeToRefs(realtime)
const dialogOpen = ref(false)

const gateway = computed(() => overview.value?.gateway || {})
const alive = computed(() => nodes.value.filter((node) => node.alive).length)
const providerError = computed(() => providers.value.find((provider) => provider.last_error)?.last_error || gateway.value.last_error)
const snippet = computed(() => `[Proxy Group]\nExternal = select, policy-path=${overview.value.policy_url}, update-interval=3600\n\n[Rule]\n${overview.value.process_rule}`)

async function copyPolicy() {
  if (!window.confirm('内容包含 Policy URL，确认复制到剪贴板？')) return
  await navigator.clipboard.writeText(snippet.value)
  ui.toast('已复制 Surge 配置')
}
</script>

<template>
  <PageHeader
    eyebrow="OVERVIEW"
    title="网关总览"
    :description="overview.provider_count ? '节点更新由 Mihomo Provider 原地生效；Surge 始终负责系统代理、规则与 TUN。' : '先添加订阅，Surge External Bridge 会把 Mihomo 节点投影给 Surge。'"
  >
    <button v-if="overview.provider_count" class="button" type="button" @click="router.push({ name: 'providers' })">查看订阅</button>
    <button v-else class="button primary" type="button" @click="dialogOpen = true">添加第一个 Provider</button>
    <button v-if="overview.projection_count" class="button primary" type="button" @click="copyPolicy">复制 Surge 配置</button>
  </PageHeader>

  <section v-if="!overview.provider_count" class="onboarding">
    <div class="card welcome-card">
      <div class="eyebrow">GET STARTED</div>
      <h2>把 Surge 不原生支持的节点接入策略组</h2>
      <p>订阅和协议节点由 Mihomo 直接管理；上游更新会自动进入 Surge 投影，无需转换配置或重启内核。</p>
      <button class="button primary" type="button" @click="dialogOpen = true">添加订阅</button>
    </div>
    <div class="setup-steps">
      <div class="setup-step"><b>添加 Provider</b><span>粘贴订阅 URL，或使用私有文件 / Inline payload。</span></div>
      <div class="setup-step"><b>确认节点可用</b><span>在节点页查看 Mihomo 延迟，按需运行 TCP/UDP 诊断。</span></div>
      <div class="setup-step"><b>复制到 Surge</b><span>复制 Policy Path 配置，加入 Surge 策略组。</span></div>
    </div>
  </section>

  <div v-if="providerError" class="banner bad"><b>需要关注</b><span>{{ providerError }}</span></div>
  <div class="banner" data-testid="overview-static-copy">
    <b>避免代理递归</b>
    <span>请将 <code>{{ overview.process_rule }}</code> 放在 Surge 的其他代理规则之前。</span>
  </div>

  <div class="grid three section-gap">
    <div class="stat"><label>Provider / 节点 / 存活</label><strong>{{ overview.provider_count }} / {{ overview.projection_count }} / {{ alive }}</strong><small>{{ overview.provider_count ? '最近成功内容由 Mihomo 保留' : '等待添加第一个 Provider' }}</small></div>
    <div class="stat"><label>实时连接</label><strong>{{ connections.connections?.length || 0 }}</strong><small>累计 ↑ {{ formatBytes(connections.uploadTotal) }} · ↓ {{ formatBytes(connections.downloadTotal) }}</small></div>
    <div class="stat"><label>实时速率 / 内存</label><strong>{{ formatRate((traffic.up || 0) + (traffic.down || 0)) }}</strong><small>内存 {{ formatBytes(memory.inuse) }}</small></div>
  </div>

  <div class="grid two section-gap">
    <div class="card">
      <div class="code-head"><span>Surge Policy Path</span><button v-if="overview.projection_count" class="button ghost compact" type="button" @click="copyPolicy">复制配置</button><span v-else>有可投影节点后即可复制</span></div>
      <pre>{{ snippet }}</pre>
    </div>
    <div class="card">
      <h3>运行与安全边界</h3>
      <dl class="kv">
        <dt>产品 / Core</dt><dd>{{ overview.version }} / Mihomo {{ overview.core_version }}</dd>
        <dt>SOCKS</dt><dd><code>{{ gateway.socks_address }}</code></dd>
        <dt>TUN / 系统代理</dt><dd><span class="pill ok">永久禁用 / 不接管</span></dd>
        <dt>代理入口</dt><dd>一个强制认证 SOCKS5 TCP/UDP 端口</dd>
        <dt>Projection</dt><dd><code :title="gateway.projection_hash">{{ gateway.projection_hash ? `${gateway.projection_hash.slice(0, 12)}…` : '空' }}</code></dd>
        <dt>最近 Provider 错误</dt><dd>{{ providerError || '无' }}</dd>
      </dl>
    </div>
  </div>

  <ProviderDialog :open="dialogOpen" @close="dialogOpen = false" @saved="dialogOpen = false" />
</template>
