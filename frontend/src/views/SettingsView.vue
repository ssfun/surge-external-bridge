<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { onBeforeRouteLeave } from 'vue-router'
import PageHeader from '@/components/PageHeader.vue'
import { api } from '@/api.js'
import { useDataStore } from '@/stores/data.js'
import { useUIStore } from '@/stores/ui.js'
import { randomToken } from '@/utils.js'

const data = useDataStore()
const ui = useUIStore()
const { settings, service } = storeToRefs(data)
const dirty = ref(false)
const saving = ref(false)
const appliedMessage = ref('')
const form = reactive({ mode: 'local', http_bind: '', socks_bind: '', socks_port: 0, socks_advertise: '', policy_base_url: '', prefix_provider: false, management_token: '', policy_token: '', node_test_url: '', node_test_udp_address: '', node_test_timeout_seconds: 10 })

function populate(value) {
  if (!value) return
  Object.assign(form, value, { management_token: '', policy_token: '' })
}
watch(settings, (value) => { if (!dirty.value) populate(value) }, { immediate: true })
const protectedState = computed(() => settings.value?.data_directory_protected && settings.value?.configuration_protected && settings.value?.master_key_protected && settings.value?.controller_key_protected)

function markDirty() { dirty.value = true; appliedMessage.value = '' }
function modeChanged() {
  if (form.mode === 'gateway') {
    let generated = false
    if (!settings.value?.management_token_configured && !form.management_token) { form.management_token = randomToken(); generated = true }
    if (!settings.value?.policy_token_configured && !form.policy_token) { form.policy_token = randomToken(); generated = true }
    if (generated) ui.toast('已生成独立随机 Token；保存前请妥善记录')
  }
  markDirty()
}

function confirmLeave() { return !dirty.value || window.confirm('设置尚未保存，离开此页将丢失改动。确认离开？') }
onBeforeRouteLeave(() => confirmLeave())
function beforeUnload(event) { if (!dirty.value) return; event.preventDefault(); event.returnValue = '' }
onMounted(() => window.addEventListener('beforeunload', beforeUnload))
onBeforeUnmount(() => window.removeEventListener('beforeunload', beforeUnload))

async function save() {
  const body = {
    mode: form.mode, http_bind: form.http_bind, socks_bind: form.socks_bind, socks_port: Number(form.socks_port),
    socks_advertise: form.socks_advertise, policy_base_url: form.policy_base_url, prefix_provider: form.prefix_provider,
    projection_types: ['*'], node_test_url: form.node_test_url, node_test_udp_address: form.node_test_udp_address,
    node_test_timeout_seconds: Number(form.node_test_timeout_seconds),
  }
  if (form.management_token) body.management_token = form.management_token
  if (form.policy_token) body.policy_token = form.policy_token
  if (body.policy_token && !window.confirm('Policy Token 保存后不会在普通 API 或界面中再次显示，请确认已经妥善记录。')) return
  saving.value = true
  try {
    const result = await data.updateSettings(body)
    dirty.value = false
    if (body.management_token) localStorage.setItem('surgeeb-management-token', body.management_token)
    if (!result.reconnect) populate(settings.value)
    appliedMessage.value = result.reconnect ? '设置已应用，请使用新的配置台地址重新连接。' : ''
    ui.toast(result.reconnect ? '设置已应用，请使用新 HTTP 地址重新连接' : '设置已原子应用')
  } catch (error) { ui.toast(error.message, true) }
  finally { saving.value = false }
}

async function rotateKey() {
  if (!window.confirm('这会立即使所有旧 Surge SOCKS 凭据失效。确认全量轮换？')) return
  try {
    await api('/api/settings/rotate-projection-key', { method: 'POST', headers: { 'X-SurgeEB-Confirm': 'rotate-projection-key' } })
    await data.reloadResources(['overview', 'settings'])
    ui.toast('Projection Key 已轮换')
  } catch (error) { ui.toast(error.message, true) }
}

async function serviceAction(install) {
  const question = install ? '注册用户级开机服务？当前进程不会被重复启动。' : '卸载用户级服务定义？'
  if (!window.confirm(question)) return
  try {
    data.service = await api(install ? '/api/service/install' : '/api/service', { method: install ? 'POST' : 'DELETE', headers: { 'X-SurgeEB-Confirm': install ? 'install-user-service' : 'uninstall-user-service' } })
    ui.toast('系统服务状态已更新')
  } catch (error) { ui.toast(error.message, true) }
}
</script>

<template>
  <PageHeader eyebrow="SETTINGS" title="部署与安全设置" description="配置产品监听、发布地址、访问令牌与诊断目标；Mihomo 的系统接管能力始终不可开启。" />
  <div v-if="!settings" class="card empty-state"><div class="empty-state-icon">…</div><b>正在加载设置</b><span>只请求部署与服务状态。</span></div>
  <template v-else>
    <form @submit.prevent="save" @input="markDirty">
      <div class="grid two">
        <div class="card"><h3>监听与发布</h3>
          <label class="field"><span>部署模式</span><select v-model="form.mode" @change="modeChanged"><option value="local">仅本机</option><option value="gateway">局域网网关</option></select><small>{{ form.mode === 'gateway' ? '适用于 macOS 与 Linux；允许局域网或可信私网地址，Management / Policy Token 均为必填。' : '配置台与 SOCKS 只允许监听回环地址。' }}</small></label>
          <label class="field"><span>配置台监听地址</span><input v-model="form.http_bind" spellcheck="false"><small>仅本机模式必须使用回环地址；局域网网关模式必须配置 Management Token。</small></label>
          <div class="form-grid"><label class="field"><span>SOCKS 监听 IP</span><input v-model="form.socks_bind" spellcheck="false"></label><label class="field"><span>SOCKS 端口</span><input v-model="form.socks_port" type="number" min="1" max="65535"></label></div>
          <label class="field"><span>Surge 连接地址</span><input v-model="form.socks_advertise" spellcheck="false"><small>写入每条 Surge SOCKS5 节点，不能使用 0.0.0.0 或 ::。</small></label>
          <label class="field"><span>Policy 基础 URL</span><input v-model="form.policy_base_url" type="url" spellcheck="false"></label>
          <label class="check-row"><input v-model="form.prefix_provider" type="checkbox"> 节点展示名添加 Provider 前缀</label>
        </div>
        <div class="card"><h3>访问令牌与诊断</h3>
          <label class="field"><span>Management Token</span><input v-model="form.management_token" type="password" autocomplete="new-password" :placeholder="settings.management_token_configured ? '已设置；留空保持不变' : '尚未设置'"><small>保护配置台和管理 API；不会从普通 API 回显。</small></label>
          <label class="field"><span>Policy Token</span><input v-model="form.policy_token" type="password" autocomplete="new-password" :placeholder="settings.policy_token_configured ? '已设置；留空保持不变' : '尚未设置'"><small>单独保护包含节点 SOCKS 凭据的 /proxies。</small></label>
          <div class="modal-hint">切换到局域网网关模式时，会为尚未配置的两类 Token 自动生成互不相同的随机值。保存前请妥善记录。</div>
          <div class="form-section"><div class="form-section-title"><b>节点诊断目标</b><span>由项目内核直接测试，不经过 Surge</span></div>
            <label class="field"><span>TCP Web 测试 URL</span><input v-model="form.node_test_url" type="url"></label>
            <div class="form-grid"><label class="field"><span>UDP DNS 目标</span><input v-model="form.node_test_udp_address"></label><label class="field"><span>超时（秒）</span><input v-model="form.node_test_timeout_seconds" type="number" min="1" max="120"></label></div>
          </div>
        </div>
      </div>
      <div class="settings-save" :class="{ clean: !dirty }"><div><b>{{ appliedMessage ? '设置已应用' : dirty ? '有未保存的设置' : '设置没有改动' }}</b><span>{{ appliedMessage || (dirty ? '保存后将校验边界并原子应用。' : '修改后可一次性原子应用。') }}</span></div><button class="button primary" type="submit" :disabled="!dirty || saving" :aria-busy="saving">保存并应用</button></div>
    </form>

    <div class="grid two diagnostics-grid">
      <div class="card"><h3>投影协议范围</h3><dl class="kv"><dt>协议</dt><dd><span class="pill ok">全部 Mihomo Provider 协议</span></dd><dt>配置来源</dt><dd>Mihomo Provider 当前成功节点</dd><dt>输出方式</dt><dd>统一投影为 Surge SOCKS5 节点</dd></dl></div>
      <div class="card" :class="{ 'recovery-card': settings.recovery_required }"><h3>版本与安全诊断</h3><dl class="kv">
        <dt>产品 / Core</dt><dd>{{ settings.version || '—' }} / Mihomo {{ settings.core_version || '—' }}</dd><dt>网关状态</dt><dd><span class="pill" :class="settings.gateway_state === 'running' ? 'ok' : 'warn'">{{ settings.gateway_state || '—' }}</span></dd><dt>Projection</dt><dd>{{ settings.projection_count || 0 }} 节点 · <code>{{ settings.projection_hash || '空' }}</code></dd><dt>私有数据目录</dt><dd><span class="pill" :class="protectedState ? 'ok' : 'bad'">{{ protectedState ? '权限受保护' : '权限需要修复' }}</span></dd><dt>配置 / 主密钥 / Controller Key</dt><dd>{{ settings.configuration_protected ? '安全' : '异常' }} / {{ settings.master_key_protected ? '安全' : '异常' }} / {{ settings.controller_key_protected ? '安全' : '异常' }}</dd>
      </dl><div v-if="settings.recovery_required" class="provider-error"><b>需要恢复</b><span>Projection Master Key 无效。数据面保持关闭，请确认执行全量轮换。</span></div><p class="meta">安全状态仅报告权限结论，不向浏览器暴露本地路径。</p></div>
    </div>
    <div class="grid two diagnostics-grid">
      <div class="card"><h3>Projection Master Key</h3><p class="meta">只有在需要让全部旧 Surge SOCKS 凭据立即失效时才轮换。该操作不可撤销。</p><button class="button danger" type="button" @click="rotateKey">全量轮换凭据</button></div>
      <div class="card"><h3>系统服务</h3><dl class="kv"><dt>平台 / 范围</dt><dd>{{ service?.platform || '—' }} / {{ service?.scope || '—' }}</dd><dt>已安装 / 活动</dt><dd>{{ service?.installed ? '是' : '否' }} / {{ service?.active ? '是' : '否' }}</dd><dt>服务定义</dt><dd>{{ service?.installed ? '已注册' : '未注册' }}（本地路径不公开）</dd></dl><div class="actions"><button class="button" type="button" :disabled="service?.installed" @click="serviceAction(true)">注册开机服务</button><button class="button danger" type="button" :disabled="!service?.installed" @click="serviceAction(false)">卸载服务</button></div></div>
    </div>
    <div class="banner diagnostics-grid"><b>系统接管永久关闭</b><span>TUN、HTTP/Mixed/Redir/TProxy、DNS listener、iptables、系统代理和公开 Controller 均不可通过此配置台开启。</span></div>
  </template>
</template>
