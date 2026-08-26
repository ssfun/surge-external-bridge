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
const form = reactive({ mode: 'local', http_bind: '', socks_bind: '', socks_port: 0, virtual_host: '', projection_key: '', prefix_provider: false, management_token: '', policy_token: '', node_test_url: '', node_test_udp_address: '', node_test_timeout_seconds: 10 })

function populate(value) {
  if (!value) return
  Object.assign(form, value, { management_token: '', policy_token: '' })
}
watch(settings, (value) => { if (!dirty.value) populate(value) }, { immediate: true })
const protectedState = computed(() => settings.value?.data_directory_protected && settings.value?.configuration_protected && settings.value?.controller_key_protected)

function markDirty() { dirty.value = true; appliedMessage.value = '' }
function generateProjectionKey() { form.projection_key = randomToken(); markDirty() }
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
    virtual_host: form.virtual_host, projection_key: form.projection_key, prefix_provider: form.prefix_provider,
    projection_types: ['*'], node_test_url: form.node_test_url, node_test_udp_address: form.node_test_udp_address,
    node_test_timeout_seconds: Number(form.node_test_timeout_seconds),
  }
  if (form.management_token) body.management_token = form.management_token
  if (form.policy_token) body.policy_token = form.policy_token
  if (body.policy_token && !window.confirm('Policy Token 保存后不会在普通 API 或界面中再次显示，请确认已经妥善记录。')) return
  if (body.projection_key !== settings.value?.projection_key && !window.confirm('修改 Projection Key 会立即改变全部投影节点凭据。确认保存？')) return
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
  <PageHeader eyebrow="SETTINGS" title="设置" description="配置网关入口、访问安全与稳定的节点身份；低频诊断和系统服务集中在页面下方。" />
  <div v-if="!settings" class="card empty-state"><div class="empty-state-icon">…</div><b>正在加载设置</b><span>只请求部署与服务状态。</span></div>
  <template v-else>
    <form @submit.prevent="save" @input="markDirty">
      <section class="settings-section">
        <div class="settings-section-head"><span>01</span><div><h2>网关配置</h2><p>这些设置决定 Surge 如何访问网关，以及节点凭据如何保持稳定。</p></div></div>
        <div class="card settings-card settings-deployment" data-testid="settings-deployment">
          <div class="settings-card-head"><div><h3>部署与入口</h3><p>先选择使用范围，再配置配置台、SOCKS 和对外发布地址。</p></div><span class="pill" :class="form.mode === 'gateway' ? 'warn' : 'ok'">{{ form.mode === 'gateway' ? '局域网' : '仅本机' }}</span></div>
          <div class="settings-deployment-grid">
            <label class="field"><span>部署模式</span><span class="select-control"><select v-model="form.mode" @change="modeChanged"><option value="local">仅本机</option><option value="gateway">局域网网关</option></select></span><small>{{ form.mode === 'gateway' ? '允许局域网或可信私网访问；Management / Policy Token 均为必填。' : '配置台与 SOCKS 只允许监听回环地址。' }}</small></label>
            <label class="field"><span>统一发布主机</span><input v-model="form.virtual_host" spellcheck="false" placeholder="surge.eb"><small>用于 Surge 节点和 Policy Path；只填写主机名或 IP。</small></label>
          </div>
          <div class="settings-subsection"><div class="settings-subsection-head"><b>监听地址</b><span>Policy Path 端口自动跟随配置台</span></div>
            <div class="settings-listener-grid">
              <label class="field"><span>配置台 HTTP</span><input v-model="form.http_bind" data-testid="settings-http-bind" spellcheck="false"><small>{{ form.mode === 'gateway' ? '必须同时配置 Management Token。' : '仅允许回环地址。' }}</small></label>
              <label class="field"><span>SOCKS 监听 IP</span><input v-model="form.socks_bind" spellcheck="false"></label>
              <label class="field"><span>SOCKS 端口</span><input v-model="form.socks_port" type="number" min="1" max="65535"></label>
            </div>
          </div>
        </div>
        <div class="settings-secondary-grid">
          <div class="card settings-card" data-testid="settings-security">
            <div class="settings-card-head"><div><h3>访问安全</h3><p>配置台与节点订阅使用相互独立的 Token。</p></div></div>
            <label class="field"><span class="settings-field-label">Management Token <i class="pill" :class="settings.management_token_configured ? 'ok' : 'warn'">{{ settings.management_token_configured ? '已配置' : '未配置' }}</i></span><input v-model="form.management_token" type="password" autocomplete="new-password" :placeholder="settings.management_token_configured ? '留空保持不变' : '输入新 Token'"><small>保护配置台和管理 API，不会从普通 API 回显。</small></label>
            <label class="field"><span class="settings-field-label">Policy Token <i class="pill" :class="settings.policy_token_configured ? 'ok' : 'warn'">{{ settings.policy_token_configured ? '已配置' : '未配置' }}</i></span><input v-model="form.policy_token" type="password" autocomplete="new-password" :placeholder="settings.policy_token_configured ? '留空保持不变' : '输入新 Token'"><small>单独保护包含节点 SOCKS 凭据的 /proxies。</small></label>
            <div v-if="form.mode === 'gateway'" class="settings-note warn">网关模式要求两类 Token 均已配置。首次切换时会为缺失项生成互不相同的随机值，请在保存前记录。</div>
          </div>
          <div class="card settings-card" data-testid="settings-identity">
            <div class="settings-card-head"><div><h3>投影身份</h3><p>控制所有设备是否生成完全一致的节点凭据。</p></div></div>
            <label class="field"><span>Projection Key</span><span class="settings-input-action"><input v-model="form.projection_key" data-testid="projection-key" spellcheck="false" autocomplete="off" minlength="16" maxlength="256"><button class="button ghost" type="button" @click="generateProjectionKey">生成新 Key</button></span><small>相同 Key、Provider 名称和节点名会生成相同凭据；修改任一项都会改变相关节点凭据。</small></label>
            <label class="check-row settings-check"><input v-model="form.prefix_provider" type="checkbox"><span><b>节点名添加 Provider 前缀</b><small>只改变 Surge 中的展示名，便于区分同名节点。</small></span></label>
          </div>
        </div>
        <details class="settings-disclosure" data-testid="settings-diagnostics">
          <summary><span><b>节点诊断目标</b><small>低频设置 · 由 Mihomo Core 直接测试，不经过 Surge</small></span><i></i></summary>
          <div class="settings-disclosure-body">
            <label class="field"><span>TCP Web 测试 URL</span><input v-model="form.node_test_url" type="url"></label>
            <div class="form-grid"><label class="field"><span>UDP DNS 目标</span><input v-model="form.node_test_udp_address"></label><label class="field"><span>超时（秒）</span><input v-model="form.node_test_timeout_seconds" type="number" min="1" max="120"></label></div>
          </div>
        </details>
      </section>
      <div v-if="dirty || appliedMessage" class="settings-save" data-testid="settings-save"><div><b>{{ appliedMessage ? '设置已应用' : '有未保存的设置' }}</b><span>{{ appliedMessage || '保存后将校验边界并原子应用。' }}</span></div><button class="button primary" type="submit" :disabled="!dirty || saving" :aria-busy="saving">保存并应用</button></div>
    </form>

    <section class="settings-section settings-operations">
      <div class="settings-section-head"><span>02</span><div><h2>运行与维护</h2><p>只读状态和本机服务操作，不影响上方尚未保存的配置。</p></div></div>
      <div class="settings-operations-grid">
        <div class="card settings-card" data-testid="settings-runtime">
          <div class="settings-card-head"><div><h3>运行与安全状态</h3><p>展示当前已应用配置的运行结果。</p></div><span class="pill" :class="settings.gateway_state === 'running' ? 'ok' : 'warn'">{{ settings.gateway_state === 'running' ? '运行中' : (settings.gateway_state || '未知') }}</span></div>
          <div class="settings-status-grid">
            <div><span>产品 / Core</span><b>{{ settings.version || '—' }} / Mihomo {{ settings.core_version || '—' }}</b></div>
            <div><span>可用节点</span><b>{{ settings.projection_count || 0 }} 个</b></div>
            <div><span>私有数据</span><b :class="protectedState ? 'ok' : 'bad'">{{ protectedState ? '权限受保护' : '权限需要修复' }}</b></div>
            <div><span>配置 / Controller Key</span><b>{{ settings.configuration_protected ? '安全' : '异常' }} / {{ settings.controller_key_protected ? '安全' : '异常' }}</b></div>
          </div>
          <div class="settings-boundary"><b>系统接管永久关闭</b><span>TUN、系统代理、DNS listener、iptables 和公开 Controller 均不可通过配置台开启。</span></div>
        </div>
        <div class="card settings-card" data-testid="settings-service">
          <div class="settings-card-head"><div><h3>系统服务</h3><p>管理当前用户的开机服务定义。</p></div><span class="pill" :class="service?.active ? 'ok' : 'warn'">{{ service?.active ? '活动' : '未活动' }}</span></div>
          <dl class="kv settings-service-facts"><dt>平台 / 范围</dt><dd>{{ service?.platform || '—' }} / {{ service?.scope || '—' }}</dd><dt>服务定义</dt><dd>{{ service?.installed ? '已注册' : '未注册' }}（本地路径不公开）</dd></dl>
          <div class="actions settings-service-actions"><button class="button" type="button" :disabled="service?.installed" @click="serviceAction(true)">注册开机服务</button><button class="button danger" type="button" :disabled="!service?.installed" @click="serviceAction(false)">卸载服务</button></div>
        </div>
      </div>
    </section>
  </template>
</template>
