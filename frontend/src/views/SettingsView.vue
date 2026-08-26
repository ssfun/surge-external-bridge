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
const credentialVisible = reactive({ management: false, policy: false })
const generatedCredential = reactive({ management: false, policy: false })

function populate(value) {
  if (!value) return
  Object.assign(form, value, { management_token: '', policy_token: '' })
  Object.assign(credentialVisible, { management: false, policy: false })
  Object.assign(generatedCredential, { management: false, policy: false })
}
watch(settings, (value) => { if (!dirty.value) populate(value) }, { immediate: true })
const protectedState = computed(() => settings.value?.data_directory_protected && settings.value?.configuration_protected && settings.value?.controller_key_protected)

function markDirty() { dirty.value = true; appliedMessage.value = '' }
function generateProjectionKey() { form.projection_key = randomToken(); markDirty() }
function generateCredential(kind, quiet = false) {
  form[`${kind}_token`] = randomToken()
  credentialVisible[kind] = true
  generatedCredential[kind] = true
  markDirty()
  if (!quiet) ui.toast(`${kind === 'management' ? 'Management' : 'Policy'} Token 已生成`)
}
function modeChanged(event) {
  form.mode = event.target.value
  if (form.mode === 'gateway') {
    let generated = false
    if (!settings.value?.management_token_configured && !form.management_token) {
      generateCredential('management', true); generated = true
    }
    if (!settings.value?.policy_token_configured && !form.policy_token) {
      generateCredential('policy', true); generated = true
    }
    if (generated) ui.toast('新的 Token 已生成，请复制保存')
  }
  markDirty()
}

async function copyCredential(label, value) {
  if (!value) return
  try {
    await navigator.clipboard.writeText(value)
    ui.toast(`${label} 已复制`)
  } catch { ui.toast('复制失败，请手动选择并复制', true) }
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
  if (body.policy_token && !window.confirm('Policy Token 保存后不会再次显示，请先复制或记录好。确认保存？')) return
  if (body.projection_key !== settings.value?.projection_key && !window.confirm('修改后，现有 Surge 节点的用户名和密码会变化。确认保存？')) return
  saving.value = true
  try {
    const result = await data.updateSettings(body)
    dirty.value = false
    if (body.management_token) localStorage.setItem('surgeeb-management-token', body.management_token)
    if (!result.reconnect) populate(settings.value)
    appliedMessage.value = result.reconnect ? '设置已保存，请使用新的配置台地址重新连接。' : ''
    ui.toast(result.reconnect ? '设置已保存，请使用新的配置台地址重新连接' : '设置已保存')
  } catch (error) { ui.toast(error.message, true) }
  finally { saving.value = false }
}

async function serviceAction(install) {
  const question = install ? '设置为开机自动启动？不会重复启动当前程序。' : '取消开机自动启动？'
  if (!window.confirm(question)) return
  try {
    data.service = await api(install ? '/api/service/install' : '/api/service', { method: install ? 'POST' : 'DELETE', headers: { 'X-SurgeEB-Confirm': install ? 'install-user-service' : 'uninstall-user-service' } })
    ui.toast('系统服务状态已更新')
  } catch (error) { ui.toast(error.message, true) }
}
</script>

<template>
  <PageHeader eyebrow="SETTINGS" title="设置" description="设置 Surge 如何连接网关，以及其他设备是否可以访问。" />
  <div v-if="!settings" class="card empty-state"><div class="empty-state-icon">…</div><b>正在加载设置</b><span>只请求部署与服务状态。</span></div>
  <template v-else>
    <form @submit.prevent="save" @input="markDirty">
      <section class="settings-section">
        <div class="settings-section-head"><span>01</span><div><h2>网关配置</h2><p>先完成常用设置，连接测试和开机启动放在下方。</p></div></div>
        <div class="card settings-card settings-deployment" data-testid="settings-deployment">
          <div class="settings-card-head"><div><h3>使用范围与地址</h3><p>选择只供本机使用，或允许同一局域网内的设备连接。</p></div><span class="pill" :class="form.mode === 'gateway' ? 'warn' : 'ok'">{{ form.mode === 'gateway' ? '局域网' : '仅本机' }}</span></div>
          <div class="settings-deployment-grid">
            <label class="field"><span>使用范围</span><span class="select-control"><select :value="form.mode" data-testid="settings-mode" @change="modeChanged"><option value="local">仅本机</option><option value="gateway">局域网网关</option></select></span><small>{{ form.mode === 'gateway' ? '同一局域网内的设备可以访问，请保存下面两项 Token。' : '只有这台电脑可以访问配置台和 SOCKS。' }}</small></label>
            <label class="field"><span>Surge 访问地址</span><input v-model="form.virtual_host" spellcheck="false" placeholder="surge.eb"><small>填写 Surge 能访问到的域名或 IP，不要带 http:// 和端口。</small></label>
          </div>
          <div class="settings-subsection"><div class="settings-subsection-head"><b>本机监听地址</b><span>Surge 订阅地址使用与配置台相同的端口</span></div>
            <div class="settings-listener-grid">
              <label class="field"><span>配置台地址</span><input v-model="form.http_bind" data-testid="settings-http-bind" spellcheck="false"><small>{{ form.mode === 'gateway' ? '局域网模式下需要配置 Management Token。' : '仅供本机访问。' }}</small></label>
              <label class="field"><span>SOCKS 地址</span><input v-model="form.socks_bind" spellcheck="false"></label>
              <label class="field"><span>SOCKS 端口</span><input v-model="form.socks_port" type="number" min="1" max="65535"></label>
            </div>
          </div>
        </div>
        <div class="settings-secondary-grid">
          <div class="card settings-card" data-testid="settings-security">
            <div class="settings-card-head"><div><h3>访问密码</h3><p>局域网模式需要两项不同的 Token，避免其他人访问配置或节点列表。</p></div></div>
            <label class="field"><span class="settings-field-label">Management Token <i class="pill" :class="form.management_token ? 'warn' : settings.management_token_configured ? 'ok' : 'warn'">{{ form.management_token ? '待保存' : settings.management_token_configured ? '已配置' : '未配置' }}</i></span><span class="settings-secret-control"><input v-model="form.management_token" data-testid="management-token" :type="credentialVisible.management ? 'text' : 'password'" autocomplete="new-password" :placeholder="settings.management_token_configured ? '留空保持不变' : '输入新 Token'"><span class="settings-field-actions"><button v-if="form.management_token" class="button ghost compact" type="button" @click="credentialVisible.management = !credentialVisible.management">{{ credentialVisible.management ? '隐藏' : '显示' }}</button><button v-if="form.management_token" class="button ghost compact" type="button" @click="copyCredential('Management Token', form.management_token)">复制</button><button class="button ghost compact" type="button" @click="generateCredential('management')">{{ form.management_token ? '重新生成' : '生成' }}</button></span></span><small>在其他设备打开此配置台时使用。</small></label>
            <label class="field"><span class="settings-field-label">Policy Token <i class="pill" :class="form.policy_token ? 'warn' : settings.policy_token_configured ? 'ok' : 'warn'">{{ form.policy_token ? '待保存' : settings.policy_token_configured ? '已配置' : '未配置' }}</i></span><span class="settings-secret-control"><input v-model="form.policy_token" data-testid="policy-token" :type="credentialVisible.policy ? 'text' : 'password'" autocomplete="new-password" :placeholder="settings.policy_token_configured ? '留空保持不变' : '输入新 Token'"><span class="settings-field-actions"><button v-if="form.policy_token" class="button ghost compact" type="button" @click="credentialVisible.policy = !credentialVisible.policy">{{ credentialVisible.policy ? '隐藏' : '显示' }}</button><button v-if="form.policy_token" class="button ghost compact" type="button" @click="copyCredential('Policy Token', form.policy_token)">复制</button><button class="button ghost compact" type="button" @click="generateCredential('policy')">{{ form.policy_token ? '重新生成' : '生成' }}</button></span></span><small>Surge 下载节点列表时使用，与管理 Token 分开更安全。</small></label>
            <div v-if="generatedCredential.management || generatedCredential.policy" class="settings-note warn settings-generated-note" data-testid="generated-token-note"><b>新的 24 位 Token 已生成并显示</b><span>请分别复制保存。保存设置后，完整内容不会再次显示。</span></div>
            <div v-else-if="form.mode === 'gateway'" class="settings-note">局域网模式需要两项不同的 Token。已配置的 Token 不会在页面中回显。</div>
          </div>
          <div class="card settings-card" data-testid="settings-identity">
            <div class="settings-card-head"><div><h3>节点凭据</h3><p>让多台 SurgeEB 为同一个节点生成相同的用户名和密码。</p></div></div>
            <label class="field"><span>节点身份 Key（Projection Key）</span><span class="settings-input-action"><input v-model="form.projection_key" data-testid="projection-key" spellcheck="false" autocomplete="off" minlength="16" maxlength="256"><span class="settings-field-actions"><button class="button ghost compact" type="button" @click="copyCredential('Projection Key', form.projection_key)">复制</button><button class="button ghost compact" type="button" @click="generateProjectionKey">重新生成</button></span></span><small>多台设备填写相同的 Key，并保持订阅名称和节点名称一致，即可得到相同的节点凭据。重新生成会创建 24 位 Key，并更换现有节点的用户名和密码。</small></label>
            <label class="check-row settings-check"><input v-model="form.prefix_provider" type="checkbox"><span><b>节点名添加 Provider 前缀</b><small>只改变 Surge 中的展示名，便于区分同名节点。</small></span></label>
          </div>
        </div>
        <details class="settings-disclosure" data-testid="settings-diagnostics">
          <summary><span><b>连接测试地址</b><small>一般不需要修改，用于检查节点是否可以正常联网</small></span><i></i></summary>
          <div class="settings-disclosure-body">
            <label class="field"><span>TCP Web 测试 URL</span><input v-model="form.node_test_url" type="url"></label>
            <div class="form-grid"><label class="field"><span>UDP DNS 目标</span><input v-model="form.node_test_udp_address"></label><label class="field"><span>超时（秒）</span><input v-model="form.node_test_timeout_seconds" type="number" min="1" max="120"></label></div>
          </div>
        </details>
      </section>
      <div v-if="dirty || appliedMessage" class="settings-save" data-testid="settings-save"><div><b>{{ appliedMessage ? '设置已保存' : '有未保存的修改' }}</b><span>{{ appliedMessage || '保存后，新的设置会立即生效。' }}</span></div><button class="button primary" type="submit" :disabled="!dirty || saving" :aria-busy="saving">保存设置</button></div>
    </form>

    <section class="settings-section settings-operations">
      <div class="settings-section-head"><span>02</span><div><h2>状态与启动</h2><p>查看当前状态，并选择是否开机自动启动。</p></div></div>
      <div class="settings-operations-grid">
        <div class="card settings-card" data-testid="settings-runtime">
          <div class="settings-card-head"><div><h3>当前状态</h3><p>这里显示的是已经保存并正在使用的设置。</p></div><span class="pill" :class="settings.gateway_state === 'running' ? 'ok' : 'warn'">{{ settings.gateway_state === 'running' ? '运行中' : (settings.gateway_state || '未知') }}</span></div>
          <div class="settings-status-grid">
            <div><span>SurgeEB / Mihomo</span><b>{{ settings.version || '—' }} / {{ settings.core_version || '—' }}</b></div>
            <div><span>可用节点</span><b>{{ settings.projection_count || 0 }} 个</b></div>
            <div><span>本地数据</span><b :class="protectedState ? 'ok' : 'bad'">{{ protectedState ? '已保护' : '需要检查权限' }}</b></div>
            <div><span>配置与管理密钥</span><b>{{ settings.configuration_protected && settings.controller_key_protected ? '已保护' : '需要检查权限' }}</b></div>
          </div>
          <div class="settings-boundary"><b>SurgeEB 不会接管系统网络</b><span>系统代理、TUN 和 DNS 等功能仍由 Surge 管理。</span></div>
        </div>
        <div class="card settings-card" data-testid="settings-service">
          <div class="settings-card-head"><div><h3>开机自动启动</h3><p>让 SurgeEB 在登录系统后自动运行。</p></div><span class="pill" :class="service?.active ? 'ok' : 'warn'">{{ service?.active ? '运行中' : '未运行' }}</span></div>
          <dl class="kv settings-service-facts"><dt>运行平台</dt><dd>{{ service?.platform || '—' }}</dd><dt>自动启动</dt><dd>{{ service?.installed ? '已开启' : '未开启' }}</dd></dl>
          <div class="actions settings-service-actions"><button class="button" type="button" :disabled="service?.installed" @click="serviceAction(true)">开启自动启动</button><button class="button danger" type="button" :disabled="!service?.installed" @click="serviceAction(false)">关闭自动启动</button></div>
        </div>
      </div>
    </section>
  </template>
</template>
