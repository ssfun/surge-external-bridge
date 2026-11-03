<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { api } from '@/api.js'

const props = defineProps({ currentVersion: { type: String, default: '' } })
const status = ref(null)
const error = ref('')
const action = ref('')
const reconnecting = ref(false)
const accepted = ref(false)
const requestedVersion = ref('')
const preferences = reactive({ auto_check: true, auto_install: false, install_hour: 4, proxy_url: '' })
const dirty = ref(false)
const saved = ref(false)
let timer
let stopped = false
const phaseNames = {
 idle: '尚未检查', checking: '检查中', downloading: '下载与校验中', prepared: '准备更新', stopping: '正在停止旧进程',
 backed_up: '已备份', installing: '安装中', starting: '正在重启并验证', succeeded: '更新成功',
 installed: '已安装，等待手动启动', rolling_back: '正在恢复旧版本', rolled_back: '更新失败，已恢复旧版本',
 failed: '更新失败', recovery_failed: '恢复失败，需要手动处理',
}
const active = computed(() => ['checking', 'downloading', 'prepared', 'stopping', 'backed_up', 'installing', 'starting', 'rolling_back'].includes(status.value?.phase))
const canInstall = computed(() => status.value?.latest && status.value?.can_install && !action.value && !accepted.value)
const label = computed(() => status.value?.check_error && !active.value ? '检查或下载失败' : reconnecting.value ? '正在重新连接' : accepted.value && !active.value ? '正在确认更新结果' : status.value?.phase === 'idle' && status.value?.checked_at && !String(status.value.checked_at).startsWith('0001') ? (status.value.latest ? '发现新版本' : '已是最新稳定版本') : (phaseNames[status.value?.phase] || '加载中'))

async function refresh() {
 try {
  const result = await api('/api/update')
  if (stopped) return
  status.value = result
  if (!dirty.value) Object.assign(preferences, result.preferences)
  reconnecting.value = false
  if ((result.target_version === requestedVersion.value && ['succeeded', 'installed', 'rolled_back', 'failed', 'recovery_failed'].includes(result.phase)) || (accepted.value && result.check_error && !active.value)) accepted.value = false
  error.value = ''
 } catch (e) {
  if (stopped) return
  error.value = e.message
  if (accepted.value || active.value) reconnecting.value = true
 }
}
async function poll() {
 await refresh()
 if (!stopped) timer = setTimeout(poll, accepted.value || active.value ? 2000 : 10000)
}
onMounted(poll)
onBeforeUnmount(() => { stopped = true; clearTimeout(timer) })
async function check() {
 action.value = 'check'; error.value = ''
 try { status.value = await api('/api/update/check', { method: 'POST' }) }
 catch (e) { error.value = e.message }
 finally { action.value = '' }
}
async function install() {
 if (!canInstall.value || !window.confirm(`更新到 ${status.value.latest.tag_name} 并重启？现有代理连接会中断，失败时将尝试恢复旧版本。`)) return
 action.value = 'install'; error.value = ''; requestedVersion.value = status.value.latest.tag_name
 try {
  await api('/api/update/install', { method: 'POST', headers: { 'X-SurgeEB-Confirm': 'install-update' }, body: JSON.stringify({ version: status.value.latest.tag_name }) })
  accepted.value = true
  await refresh()
 } catch (e) { error.value = `${e.message}。请先刷新状态确认结果，不要重复提交。` }
 finally { action.value = '' }
}
async function save() {
 if (preferences.auto_install && !window.confirm('开启后，SurgeEB 将在所选本机时段自动安装稳定版本并重启，现有代理连接会中断。确认开启？')) return
 action.value = 'save'; error.value = ''
 try {
  status.value = await api('/api/update/settings', { method: 'PUT', headers: preferences.auto_install ? { 'X-SurgeEB-Confirm': 'enable-auto-update' } : {}, body: JSON.stringify({ ...preferences, install_hour: Number(preferences.install_hour) }) })
  dirty.value = false
  saved.value = true
 } catch (e) { error.value = e.message }
 finally { action.value = '' }
}
function changed() { saved.value = false; if (!preferences.auto_check) preferences.auto_install = false; dirty.value = true }
</script>

<template>
 <div class="card settings-card settings-update" data-testid="settings-update">
  <div class="settings-card-head"><div><h3>软件更新</h3><p>从项目 GitHub Releases 更新 SurgeEB、内嵌 Mihomo 与配置台。</p></div><span class="pill" role="status">{{ label }}</span></div>
  <div class="update-layout">
   <div class="update-details">
  <dl class="kv"><dt>当前版本</dt><dd>{{ status?.current_version || props.currentVersion || '—' }}</dd><dt v-if="status?.latest">可用版本</dt><dd v-if="status?.latest">{{ status.latest.tag_name }}</dd><dt v-if="status?.target">更新位置</dt><dd v-if="status?.target" class="update-path">{{ status.target }}</dd><dt>运行方式</dt><dd>{{ status?.service ? '系统自启服务' : '手动运行' }}</dd></dl>
  <p v-if="status?.service" class="settings-note">更新后由系统服务运行；已注册自启的手动进程会在释放端口后交给服务接管。</p>
  <p v-if="status?.install_reason" class="settings-note">{{ status.install_reason }}</p>
  <p v-if="status?.error || status?.check_error || error" class="settings-note warn" role="alert">{{ error || status.error || status.check_error }}</p>
  <p v-if="reconnecting" class="settings-note">服务可能正在重启，正在重新连接并核对结果；连接中断不代表更新成功。</p>
  <div v-if="status?.latest" class="update-release"><a :href="status.latest.html_url" target="_blank" rel="noopener noreferrer">查看 Release</a><details v-if="status.latest.body"><summary>发布说明</summary><pre>{{ status.latest.body }}</pre></details></div>
  <div class="actions"><button class="button ghost" type="button" :disabled="Boolean(action) || active" @click="check">{{ action === 'check' ? '检查中…' : '检查更新' }}</button><button class="button" type="button" :disabled="!canInstall" @click="install">更新并重启</button></div>
   </div>
  <fieldset :disabled="!status || Boolean(action) || active" class="update-preferences">
   <label><input v-model="preferences.auto_check" type="checkbox" @change="changed"> 自动检查稳定版本（每 24 小时）</label>
   <label><input v-model="preferences.auto_install" type="checkbox" :disabled="!preferences.auto_check || !status?.service" @change="changed"> 自动安装并重启</label>
   <label v-if="preferences.auto_install">安装时段（运行主机本地时间）<select v-model="preferences.install_hour" @change="changed"><option v-for="hour in 24" :key="hour - 1" :value="hour - 1">{{ String(hour - 1).padStart(2, '0') }}:00–{{ String(hour - 1).padStart(2, '0') }}:59</option></select></label>
   <label class="field update-proxy"><span>更新代理</span><input v-model="preferences.proxy_url" type="text" autocomplete="off" spellcheck="false" placeholder="http://127.0.0.1:7890" @input="changed"></label>
   <p class="settings-note">支持 HTTP、HTTPS、SOCKS5 / SOCKS5H，可包含认证信息；留空直连。保存后用于版本检查和下载，不跟随系统代理。请使用更新期间仍可用的独立代理。</p>
   <button class="button ghost" type="button" :disabled="!dirty" @click="save">保存更新设置</button><span v-if="saved" role="status">更新设置已保存</span>
  </fieldset>
  </div>
 </div>
</template>
<style scoped>
.settings-update{grid-column:1/-1;min-width:0}
.update-layout{display:grid;grid-template-columns:minmax(0,1.25fr) minmax(0,1fr);gap:24px}
.update-details{min-width:0}.update-details>.kv{margin-top:0;grid-template-columns:max-content minmax(0,1fr)}
.update-path{overflow-wrap:anywhere}
.update-preferences{display:grid;align-content:start;gap:12px;min-width:0;border:0;padding:0 0 0 24px;margin:0;border-left:1px solid var(--line)}
.update-preferences label{display:flex;gap:8px;align-items:center;flex-wrap:wrap}.update-preferences button{justify-self:start}
.update-preferences .update-proxy{display:grid;gap:8px;margin:0}.update-proxy input{width:100%;min-width:0;box-sizing:border-box}.update-release{margin:12px 0}.update-release pre{white-space:pre-wrap;overflow-wrap:anywhere;max-height:240px;overflow:auto;font:inherit}.update-release summary{cursor:pointer;margin-top:8px}
@media(max-width:980px){.update-layout{grid-template-columns:1fr;gap:16px}.update-preferences{border-left:0;border-top:1px solid var(--line);padding:16px 0 0}}
@media(max-width:620px){.settings-update>.settings-card-head{flex-wrap:wrap}}
</style>
