<script setup>
import { nextTick, reactive, ref, watch } from 'vue'
import { api, encodeID } from '@/api.js'
import { useDataStore } from '@/stores/data.js'
import { useUIStore } from '@/stores/ui.js'

const props = defineProps({ open: Boolean, provider: { type: Object, default: null } })
const emit = defineEmits(['close', 'saved'])
const data = useDataStore()
const ui = useUIStore()
const dialog = ref(null)
const nameInput = ref(null)
const busy = ref(false)
let returnFocus = null

const defaults = () => ({
  name: '', type: 'http', enabled: true, url: '', headers: '', file_path: '', payload: '',
  refresh_seconds: 21600, download_proxy: '', size_limit: 16777216,
  include_name: '', exclude_name: '', health_check: true,
  health_check_url: 'https://www.gstatic.com/generate_204', health_check_seconds: 300,
  health_check_timeout: 5000, health_check_lazy: true, expected_status: '200-399',
})
const form = reactive(defaults())

function resetForm() {
  Object.assign(form, defaults(), props.provider || {}, {
    url: '', headers: '', payload: '',
    health_check_url: props.provider ? '' : (props.provider?.health_check_url || 'https://www.gstatic.com/generate_204'),
  })
}

watch(() => props.open, async (open) => {
  if (!open) return
  returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  resetForm()
  await nextTick()
  nameInput.value?.focus({ preventScroll: true })
}, { immediate: true })

function close() {
  if (busy.value) return
  emit('close')
  nextTick(() => returnFocus?.isConnected && returnFocus.focus())
}

function trapFocus(event) {
  const items = [...dialog.value.querySelectorAll('button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),summary,[tabindex]:not([tabindex="-1"])')]
  if (!items.length) return event.preventDefault()
  const first = items[0]
  const last = items.at(-1)
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
}

async function reveal() {
  if (!window.confirm('敏感 URL 与 Header 可能包含订阅凭据，确认显示？')) return
  try {
    const secrets = await api(`/api/providers/${encodeID(props.provider.stable_id)}/secrets`, { headers: { 'X-SurgeEB-Confirm': 'reveal-provider-secrets' } })
    form.url = secrets.url || ''
    form.headers = JSON.stringify(secrets.headers || {}, null, 2)
  } catch (error) { ui.toast(error.message, true) }
}

async function submit() {
  busy.value = true
  try {
    const headersText = form.headers.trim()
    const payloadText = form.payload.trim()
    const body = {
      name: form.name, type: form.type, url: form.url, file_path: form.file_path, enabled: form.enabled,
      headers: headersText ? JSON.parse(headersText) : undefined,
      payload: payloadText ? JSON.parse(payloadText) : undefined,
      refresh_seconds: Number(form.refresh_seconds), download_proxy: form.download_proxy,
      size_limit: Number(form.size_limit), include_name: form.include_name, exclude_name: form.exclude_name,
      health_check: form.health_check, health_check_url: form.health_check_url,
      health_check_seconds: Number(form.health_check_seconds), health_check_timeout: Number(form.health_check_timeout),
      health_check_lazy: form.health_check_lazy, expected_status: form.expected_status,
    }
    await data.saveProvider(body, props.provider?.stable_id || '')
    ui.toast('Provider 已通过进程内受控 ApplyConfig 生效')
    emit('saved')
  } catch (error) {
    ui.toast(error.message, true)
  } finally { busy.value = false }
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="overlay" @mousedown.self="close">
      <form
        ref="dialog"
        class="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="provider-dialog-title"
        aria-describedby="provider-dialog-description"
        tabindex="-1"
        @submit.prevent="submit"
        @keydown.esc.prevent.stop="close"
        @keydown.tab="trapFocus"
      >
        <div class="modal-header">
          <div><div class="eyebrow">{{ provider ? 'EDIT PROVIDER' : 'NEW PROVIDER' }}</div><h2 id="provider-dialog-title">{{ provider ? '编辑' : '添加' }} Provider</h2></div>
          <button type="button" class="modal-close" aria-label="关闭" :disabled="busy" @click="close">×</button>
        </div>
        <div class="modal-scroll">
          <p id="provider-dialog-description">订阅和节点由 Mihomo 直接管理；名称筛选只影响 Surge 投影。</p>
          <div class="banner"><b>应用方式</b><span>保存 Provider 定义会在进程内应用，相关既有连接可能结束；普通订阅刷新不会主动关闭连接。</span></div>
          <section class="form-section">
            <div class="form-section-title"><b>基础信息</b><span>{{ form.type === 'http' ? 'Mihomo 定时拉取并保留最近成功缓存。' : form.type === 'file' ? '文件必须位于 Mihomo 私有数据目录内。' : '适合临时粘贴或测试，不执行远端刷新。' }}</span></div>
            <div class="form-grid">
              <label class="field"><span>名称</span><input ref="nameInput" v-model="form.name" required autocomplete="off"></label>
              <label class="field"><span>来源类型</span><select v-model="form.type"><option value="http">HTTP 订阅</option><option value="file">私有文件</option><option value="inline">Inline payload</option></select></label>
            </div>
            <label class="check-row"><input v-model="form.enabled" type="checkbox"> 启用这个 Provider</label>
          </section>

          <section v-show="form.type === 'http'" class="form-section conditional">
            <div class="form-section-title"><b>HTTP 订阅</b><span>支持 Clash YAML、URI 列表与 Base64 URI</span></div>
            <label class="field"><span>订阅 URL{{ provider ? '（留空保持现有值）' : '' }}</span><input v-model="form.url" type="url" placeholder="https://example.com/subscription" autocomplete="off"></label>
            <label class="field"><span>请求 Header JSON{{ provider ? '（留空保持现有值）' : '' }}</span><textarea v-model="form.headers" spellcheck="false" placeholder='{"Authorization":["Bearer ..."]}' /><small>仅允许 Authorization、Cookie、User-Agent、Accept 与 Accept-Language。</small></label>
            <button v-if="provider" type="button" class="button ghost" @click="reveal">读取现有 URL / Header</button>
            <div class="form-grid">
              <label class="field"><span>刷新间隔（秒）</span><input v-model="form.refresh_seconds" type="number" min="60"></label>
              <label class="field"><span>响应上限（字节）</span><input v-model="form.size_limit" type="number" min="1024" max="134217728"></label>
            </div>
            <label class="field"><span>下载所用代理</span><input v-model="form.download_proxy" placeholder="留空使用 DIRECT"><small>填写 Mihomo 中已有的 Proxy 名称。</small></label>
          </section>

          <section v-show="form.type === 'file'" class="form-section conditional">
            <div class="form-section-title"><b>私有文件</b><span>路径受数据目录边界保护</span></div>
            <label class="field"><span>Provider 文件路径</span><input v-model="form.file_path" autocomplete="off"><small>必须位于 Mihomo 私有目录；不能使用任意绝对路径。</small></label>
          </section>
          <section v-show="form.type === 'inline'" class="form-section conditional">
            <div class="form-section-title"><b>Inline payload</b><span>适合粘贴与测试</span></div>
            <label class="field"><span>Proxies JSON 数组</span><textarea v-model="form.payload" spellcheck="false" placeholder='[{"name":"...","type":"vless",...}]' /></label>
          </section>

          <details class="advanced">
            <summary>投影筛选与健康检查 <span>可选</span></summary>
            <div class="form-grid">
              <label class="field"><span>名称包含（正则）</span><input v-model="form.include_name"></label>
              <label class="field"><span>名称排除（正则）</span><input v-model="form.exclude_name"></label>
            </div>
            <div class="modal-hint">筛选只改变 Surge 投影视图，不删除或改写 Mihomo Provider 中的节点。</div>
            <label class="check-row"><input v-model="form.health_check" type="checkbox"> 启用 Mihomo 自动健康检查</label>
            <div v-show="form.health_check" class="conditional">
              <div class="form-grid">
                <label class="field"><span>健康检查 URL{{ provider ? '（留空保持现有值）' : '' }}</span><input v-model="form.health_check_url" type="url"></label>
                <label class="field"><span>间隔（秒）</span><input v-model="form.health_check_seconds" type="number" min="60"></label>
                <label class="field"><span>超时（毫秒）</span><input v-model="form.health_check_timeout" type="number" min="100" max="120000"></label>
                <label class="field"><span>期望状态码</span><input v-model="form.expected_status"></label>
              </div>
              <label class="check-row"><input v-model="form.health_check_lazy" type="checkbox"> Lazy：只在需要时主动检查</label>
            </div>
          </details>
        </div>
        <div class="modal-actions"><button type="button" class="button" :disabled="busy" @click="close">取消</button><button class="button primary" type="submit" :disabled="busy" :aria-busy="busy">{{ provider ? '保存并应用' : '添加并应用' }}</button></div>
      </form>
    </div>
  </Teleport>
</template>
