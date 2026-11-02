<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { onBeforeRouteLeave } from 'vue-router'
import PageHeader from '@/components/PageHeader.vue'
import UpdateCard from '@/components/UpdateCard.vue'
import { api } from '@/api.js'
import { useDataStore } from '@/stores/data.js'
import { useUIStore } from '@/stores/ui.js'
import { copyText, randomToken } from '@/utils.js'

const data = useDataStore()
const ui = useUIStore()
const { settings, service } = storeToRefs(data)
const dirty = ref(false)
const saving = ref(false)
const appliedMessage = ref('')
const form = reactive({ mode: 'local', http_bind: '', socks_bind: '', socks_port: 0, socks_host: '', policy_host: '', projection_key: '', prefix_provider: false, management_token: '', policy_token: '', node_test_url: '', node_test_udp_address: '', node_test_timeout_seconds: 10 })
const nodeDNS = reactive({ nameservers: '', bootstrap: '', rules: [] })
const dnsLines = (value) => String(value || '').split(/[\n,]+/).map((line) => line.trim()).filter(Boolean)
function addDNSRule() { nodeDNS.rules.push({ domain: '', nameservers: '' }); markDirty() }
function removeDNSRule(index) { nodeDNS.rules.splice(index, 1); markDirty() }
const credentialVisible = reactive({ management: false })
const generatedCredential = reactive({ management: false, policy: false })

function populate(value) {
  if (!value) return
  Object.assign(form, value, { management_token: '' })
  nodeDNS.nameservers = (value.node_dns?.nameservers || []).join('\n')
  nodeDNS.bootstrap = (value.node_dns?.bootstrap_nameservers || []).join('\n')
  nodeDNS.rules = (value.node_dns?.rules || []).map((rule) => ({ domain: rule.domain, nameservers: (rule.nameservers || []).join('\n') }))
  credentialVisible.management = false
  generatedCredential.management = false
  generatedCredential.policy = false
}
watch(settings, (value) => { if (!dirty.value) populate(value) }, { immediate: true })
watch(() => data.loadedAt.settings, () => { if (!dirty.value) populate(settings.value) })
const protectedState = computed(() => settings.value?.data_directory_protected && settings.value?.configuration_protected && settings.value?.controller_key_protected)
const generatedCredentialTitle = computed(() => generatedCredential.management && generatedCredential.policy
  ? '两个独立的 24 位 Token 已生成并显示'
  : generatedCredential.management ? '新的 24 位 Management Token 已生成并显示' : '新的 24 位 Policy Token 已生成并显示')
const generatedCredentialHelp = computed(() => generatedCredential.management && generatedCredential.policy
  ? '请分别保存 Management Token，并将 Policy Token 复制到 Surge 配置。'
  : generatedCredential.management ? '请复制保存；Policy Token 没有改变。' : '请复制到 Surge Policy Path；Management Token 没有改变。')
const serviceDetectionFailed = computed(() => Boolean(service.value?.error))

function markDirty() { dirty.value = true; appliedMessage.value = '' }
function generateProjectionKey() { form.projection_key = randomToken(); markDirty() }
function generateManagementToken(quiet = false) {
  form.management_token = randomToken()
  credentialVisible.management = true
  generatedCredential.management = true
  markDirty()
  if (!quiet) ui.toast('Management Token 已生成')
}
function generatePolicyToken(quiet = false) {
  form.policy_token = randomToken()
  generatedCredential.policy = true
  markDirty()
  if (!quiet) ui.toast('Policy Token 已生成')
}
function listenerPort(address) {
  const match = String(address || '').match(/:(\d+)$/)
  return match?.[1] || '18080'
}
function modeChanged() {
  const port = listenerPort(form.http_bind)
  if (form.mode === 'gateway') {
    form.http_bind = `0.0.0.0:${port}`
    form.socks_bind = '0.0.0.0'
    const suggestedHost = settings.value?.suggested_gateway_host || ''
    if (!form.socks_host || /^(127\.0\.0\.1|localhost|0\.0\.0\.0|::1)$/.test(form.socks_host)) form.socks_host = suggestedHost
    if (!form.policy_host || /^(127\.0\.0\.1|localhost|0\.0\.0\.0|::1)$/.test(form.policy_host)) form.policy_host = suggestedHost
    let managementGenerated = false
    let policyDefaulted = false
    if (!settings.value?.management_token_configured && !form.management_token) {
      generateManagementToken(true); managementGenerated = true
    }
    if (!form.policy_token) {
      generatePolicyToken(true); policyDefaulted = true
    }
    if (managementGenerated && policyDefaulted) ui.toast('Management Token 与 Policy Token 已分别生成')
    else if (managementGenerated) ui.toast('Management Token 已生成，请复制保存')
    else if (policyDefaulted) ui.toast('Policy Token 已生成，请复制到 Surge 配置')
  } else {
    form.http_bind = `127.0.0.1:${port}`
    form.socks_bind = '127.0.0.1'
    form.socks_host = '127.0.0.1'
    form.policy_host = '127.0.0.1'
  }
  markDirty()
}

async function copyCredential(label, value) {
  if (!value) return
  try {
    await copyText(value)
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
    socks_host: form.socks_host, policy_host: form.policy_host, projection_key: form.projection_key, prefix_provider: form.prefix_provider,
    projection_types: ['*'], node_test_url: form.node_test_url, node_test_udp_address: form.node_test_udp_address,
    node_test_timeout_seconds: Number(form.node_test_timeout_seconds),
    node_dns: { nameservers: dnsLines(nodeDNS.nameservers), bootstrap_nameservers: dnsLines(nodeDNS.bootstrap), rules: nodeDNS.rules.map((rule) => ({ domain: rule.domain.trim(), nameservers: dnsLines(rule.nameservers) })) },
  }
  if (form.management_token) body.management_token = form.management_token
  if (form.policy_token && form.policy_token !== settings.value?.policy_token) body.policy_token = form.policy_token
  if (body.projection_key !== settings.value?.projection_key && !window.confirm('修改后，现有 Surge 节点的用户名和密码会变化。确认保存？')) return
  saving.value = true
  try {
    const result = await data.updateSettings(body)
    dirty.value = false
    if (!result.reconnect && result.refreshed !== false) populate(settings.value)
    appliedMessage.value = result.reconnect ? '设置已保存，请使用新的配置台地址重新连接。' : ''
    ui.toast(result.sessionExpired ? '设置已保存，请使用新的 Management Token 重新登录' : result.reconnect ? '设置已保存，请使用新的配置台地址重新连接' : '设置已保存')
  } catch (error) { ui.toast(error.message, true) }
  finally { saving.value = false }
}

async function serviceAction(install) {
  const question = install
    ? (service.value?.repair_needed ? '修复旧版自动启动定义并迁移到当前用户目录？不会重复启动当前程序。' : '设置为开机自动启动？不会重复启动当前程序。')
    : '取消开机自动启动？'
  if (!window.confirm(question)) return
  try {
    data.service = await api(install ? '/api/service/install' : '/api/service', { method: install ? 'POST' : 'DELETE', headers: { 'X-SurgeEB-Confirm': install ? 'install-user-service' : 'uninstall-user-service' } })
    ui.toast('系统服务状态已更新')
  } catch (error) { ui.toast(error.message, true) }
}
</script>

<template>
  <PageHeader eyebrow="SETTINGS" title="设置" description="设置 Surge 如何连接网关，以及其他设备是否可以访问。" />
  <div v-if="!settings" class="card empty-state"><div class="empty-state-icon">…</div><b>正在加载设置</b><span>请稍候，正在读取当前配置。</span></div>
  <template v-else>
    <form @submit.prevent="save" @input="markDirty">
      <section class="settings-section">
        <div class="settings-section-head"><span>01</span><div><h2>网关配置</h2><p>先完成常用设置，连接测试和开机启动放在下方。</p></div></div>
        <div class="card settings-card settings-deployment" data-testid="settings-deployment">
          <div class="settings-card-head"><div><h3>使用范围与地址</h3><p>选择只供本机使用，或允许同一局域网内的设备连接。</p></div><span class="pill" :class="form.mode === 'gateway' ? 'warn' : 'ok'">{{ form.mode === 'gateway' ? '局域网' : '仅本机' }}</span></div>
          <div class="settings-deployment-grid">
            <label class="field"><span>使用范围</span><span class="select-control"><select v-model="form.mode" data-testid="settings-mode" @change="modeChanged"><option value="local">仅本机</option><option value="gateway">局域网网关</option></select></span><small>{{ form.mode === 'gateway' ? '自动监听所有本机网卡；同一局域网内的设备可通过下方访问地址连接。' : '自动限制为 127.0.0.1，只有这台电脑可以访问。' }}</small></label>
            <label class="field"><span>SOCKS 发布主机</span><input v-model="form.socks_host" data-testid="settings-socks-host" spellcheck="false" placeholder="192.168.50.10"><small>Surge 连接节点时使用的 SOCKS 主机，不要带协议和端口。</small></label>
            <label class="field"><span>Policy Path 发布主机</span><input v-model="form.policy_host" data-testid="settings-policy-host" spellcheck="false" placeholder="surge.eb"><small>用于生成 policy-path URL，可与 SOCKS 主机不同，不要带协议和端口。</small></label>
          </div>
          <div class="settings-subsection"><div class="settings-subsection-head"><b>监听地址</b><span>切换使用范围时自动生成，仍可按需精确绑定网卡</span></div>
            <div class="settings-listener-grid">
              <label class="field"><span>配置台监听</span><input v-model="form.http_bind" data-testid="settings-http-bind" spellcheck="false"><small>{{ form.mode === 'gateway' ? '0.0.0.0 同时监听局域网与 Tailscale 等本机网卡；精确绑定只能填写本机实际地址，不能填写 peer 地址。' : '仅供本机访问。' }}</small></label>
              <label class="field"><span>SOCKS 监听</span><input v-model="form.socks_bind" data-testid="settings-socks-bind" spellcheck="false"><small>{{ form.mode === 'gateway' ? '0.0.0.0 可由局域网 IP 访问。' : '只监听本机回环。' }}</small></label>
              <label class="field"><span>SOCKS 端口</span><input v-model="form.socks_port" type="number" min="1" max="65535"></label>
            </div>
          </div>
        </div>
        <div class="settings-secondary-grid">
          <div class="card settings-card" data-testid="settings-security">
            <div class="settings-card-head"><div><h3>访问 Token</h3><p>Management Token 保护配置台；这里的 Policy Token 只对应默认 <code>/proxies</code> 链接。</p></div></div>
            <label class="field"><span class="settings-field-label">Management Token <i class="pill" :class="form.management_token ? 'warn' : settings.management_token_configured ? 'ok' : 'warn'">{{ form.management_token ? '待保存' : settings.management_token_configured ? '已配置' : '未配置' }}</i></span><span class="settings-secret-control"><input v-model="form.management_token" data-testid="management-token" :type="credentialVisible.management ? 'text' : 'password'" autocomplete="new-password" :placeholder="settings.management_token_configured ? '留空保持不变' : '输入新 Token'"><span class="settings-field-actions"><button v-if="form.management_token" class="button ghost compact" type="button" @click="credentialVisible.management = !credentialVisible.management">{{ credentialVisible.management ? '隐藏' : '显示' }}</button><button v-if="form.management_token" class="button ghost compact" type="button" @click="copyCredential('Management Token', form.management_token)">复制</button><button class="button ghost compact" type="button" @click="generateManagementToken">{{ form.management_token ? '重新生成' : '生成' }}</button></span></span><small>在其他设备打开此配置台时使用；保存后不会再次显示完整内容。</small></label>
            <label class="field"><span class="settings-field-label">默认 Policy Token <i class="pill" :class="form.policy_token ? 'ok' : 'warn'">{{ form.policy_token ? '已设置' : '未设置' }}</i></span><span class="settings-secret-control"><input v-model="form.policy_token" data-testid="policy-token" type="text" autocomplete="off" minlength="16" placeholder="至少 16 位"><span class="settings-field-actions"><button v-if="form.policy_token" class="button ghost compact" type="button" @click="copyCredential('Policy Token', form.policy_token)">复制</button><button class="button ghost compact" type="button" @click="generatePolicyToken">{{ form.policy_token ? '重新生成' : '生成' }}</button></span></span><small>用于兼容默认 /proxies 地址；其他链接请在 Policy Path 管理中分别轮换 Token。</small></label>
            <div v-if="generatedCredential.management || generatedCredential.policy" class="settings-note warn settings-generated-note" data-testid="generated-token-note"><b>{{ generatedCredentialTitle }}</b><span>{{ generatedCredentialHelp }}</span></div>
            <div v-else-if="form.mode === 'gateway'" class="settings-note">Management Token 保存后不会回显；Policy Token 会保留显示，便于配置 Surge Policy Path。</div>
          </div>
          <div class="card settings-card" data-testid="settings-identity">
            <div class="settings-card-head"><div><h3>节点凭据</h3><p>让多台 SurgeEB 为同一个节点生成相同的用户名和密码。</p></div></div>
            <label class="field"><span>节点身份 Key（Projection Key）</span><span class="settings-input-action"><input v-model="form.projection_key" data-testid="projection-key" spellcheck="false" autocomplete="off" minlength="16" maxlength="256"><span class="settings-field-actions"><button class="button ghost compact" type="button" @click="copyCredential('Projection Key', form.projection_key)">复制</button><button class="button ghost compact" type="button" @click="generateProjectionKey">重新生成</button></span></span><small>多台设备填写相同的 Key，并保持订阅名称和节点名称一致，即可得到相同的节点凭据。重新生成会创建 24 位 Key，并更换现有节点的用户名和密码。</small></label>
            <label class="check-row settings-check"><input v-model="form.prefix_provider" type="checkbox"><span><b>节点名添加 Provider 前缀</b><small>只改变 Surge 中的展示名，便于区分同名节点。</small></span></label>
          </div>
        </div>
        <div class="card settings-card" data-testid="settings-node-dns">
          <div class="settings-card-head"><div><h3>节点 DNS</h3><p>解析所有 Provider 的节点服务器域名。Provider hosts 优先；留空沿用系统 DNS，不修改系统设置，也不开放 DNS 端口。</p></div></div>
          <div class="settings-secondary-grid">
            <label class="field"><span>默认 DNS（可选）</span><textarea v-model="nodeDNS.nameservers" data-testid="node-dns-default" rows="3" spellcheck="false" placeholder="223.5.5.5&#10;https://dns.example.com/dns-query"></textarea><small>每行一个，或用逗号分隔。未命中规则时使用；留空使用系统 DNS。支持 UDP、TCP、DoH 和 DoT。</small></label>
            <label class="field"><span>引导 DNS（可选）</span><textarea v-model="nodeDNS.bootstrap" data-testid="node-dns-bootstrap" rows="3" spellcheck="false" placeholder="223.5.5.5&#10;tcp://1.1.1.1:53"></textarea><small>仅解析 DoH / DoT 上游自身的域名。留空使用系统 DNS；填写后仅使用指定的 IP 形式 UDP / TCP，失败不回退系统 DNS。</small></label>
          </div>
          <div class="settings-subsection">
            <div class="settings-subsection-head"><b>域名规则</b><button class="button ghost compact" type="button" data-testid="node-dns-add-rule" @click="addDNSRule">添加规则</button></div>
            <p class="node-dns-help">精确域名优先，其次是最具体的后缀，与行顺序无关。<code>*.example.com</code> 匹配任意层级子域名，不含根域；<code>+.example.com</code> 同时包含根域。同后缀并存时，子域名使用 * 规则。</p>
            <div v-for="(rule, index) in nodeDNS.rules" :key="index" class="node-dns-rule" data-testid="node-dns-rule">
              <label class="field"><span>域名</span><input v-model="rule.domain" :aria-label="`规则 ${index + 1} 域名`" required spellcheck="false" placeholder="*.example.com"></label>
              <label class="field"><span>指定 DNS</span><textarea v-model="rule.nameservers" :aria-label="`规则 ${index + 1} DNS`" required rows="2" spellcheck="false" placeholder="tls://1.1.1.1:853"></textarea></label>
              <button class="button ghost compact" type="button" :aria-label="`删除规则 ${index + 1}`" @click="removeDNSRule(index)">删除</button>
            </div>
            <p v-if="!nodeDNS.rules.length" class="node-dns-help">尚无域名规则，所有节点使用默认 DNS。</p>
          </div>
          <div class="settings-note">命中规则或已配置默认 DNS 后，解析失败不会回退到系统 DNS。多个上游作为同一组查询。节点地址与 TLS SNI 保持不变；不导入订阅内的 dns 配置。</div>
          <details class="node-dns-details"><summary>地址格式与适用范围</summary><p>可填写 IP[:端口]、udp://IP[:端口]、tcp://IP[:端口]、tls://主机[:端口] 或 https://主机[:端口]/dns-query。加密连接会校验证书，不接受凭据、查询参数、# 后缀或代理选项。</p><p>规则只用于已加载的节点域名；其他目标地址和订阅下载域名沿用原解析方式。Mihomo 的通用连接若恰好使用同一个节点域名，也会命中此设置。ECH 查询仍沿用系统解析。</p></details>
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
          <div class="settings-card-head"><div><h3>开机自动启动</h3><p>让 SurgeEB 随系统或用户会话自动运行；当前程序无需重复启动。</p></div><span class="pill" :class="service?.installed && !service?.repair_needed && !serviceDetectionFailed ? 'ok' : 'warn'">{{ serviceDetectionFailed ? '检测失败' : (service?.repair_needed ? '需要修复' : (service?.installed ? '已开启' : '未开启')) }}</span></div>
          <dl class="kv settings-service-facts"><dt>运行平台</dt><dd>{{ service?.platform || '—' }}</dd><dt>自动启动</dt><dd>{{ serviceDetectionFailed ? '检测失败' : (service?.repair_needed ? '旧定义需要迁移' : (service?.installed ? '已开启' : '未开启')) }}</dd></dl>
          <div v-if="serviceDetectionFailed" class="settings-note warn">无法检测系统服务状态：{{ service.error }}</div>
          <div v-if="service?.repair_needed" class="settings-note warn">当前 LaunchAgent 仍使用旧路径、定义异常或服务副本缺失；修复会迁移到当前用户目录，不需要 sudo。</div>
          <div class="actions settings-service-actions"><button class="button" type="button" :disabled="serviceDetectionFailed || (service?.installed && !service?.repair_needed)" @click="serviceAction(true)">{{ service?.repair_needed ? '修复自动启动' : '开启自动启动' }}</button><button class="button danger" type="button" :disabled="serviceDetectionFailed || !service?.installed" @click="serviceAction(false)">关闭自动启动</button></div>
        </div>
        <UpdateCard :current-version="settings.version" />
      </div>
    </section>
  </template>
</template>

<style scoped>
.node-dns-help, .node-dns-details { color: var(--muted); font-size: 13px; line-height: 1.7; }
.node-dns-rule { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.5fr) auto; gap: 12px; align-items: center; padding: 12px 0; }
.node-dns-rule .field { margin: 0; }
.node-dns-details summary { cursor: pointer; }
@media (max-width: 640px) { .node-dns-rule { grid-template-columns: minmax(0, 1fr); } .node-dns-rule > button { justify-self: end; } }
</style>
