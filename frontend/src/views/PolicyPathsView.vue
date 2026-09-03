<script setup>
import { computed, reactive, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import PageHeader from '@/components/PageHeader.vue'
import { useDataStore } from '@/stores/data.js'
import { useUIStore } from '@/stores/ui.js'
import { copyText } from '@/utils.js'

const data = useDataStore()
const ui = useUIStore()
const router = useRouter()
const { policyPaths, providers } = storeToRefs(data)
const dialogOpen = ref(false)
const editingID = ref('')
const saving = ref(false)
const busy = reactive(new Set())
const form = reactive({ name: '', token: '', include_all: true, provider_ids: [] })
const selectedCount = computed(() => form.include_all ? providers.value.length : form.provider_ids.length)

function open(path = null) {
  editingID.value = path?.id || ''
  form.name = path?.name || ''
  form.token = ''
  form.include_all = path?.include_all ?? true
  form.provider_ids = [...(path?.provider_ids || [])]
  dialogOpen.value = true
}

function close() {
  if (saving.value) return
  dialogOpen.value = false
  editingID.value = ''
}

function setScope(includeAll) {
  form.include_all = includeAll
  if (includeAll) form.provider_ids = []
}

function toggleProvider(id) {
  const selected = new Set(form.provider_ids)
  if (selected.has(id)) selected.delete(id)
  else selected.add(id)
  form.provider_ids = providers.value.filter((provider) => selected.has(provider.stable_id)).map((provider) => provider.stable_id)
}

async function save() {
  if (!form.include_all && form.provider_ids.length === 0) {
    ui.toast('请至少选择一个 Provider', true)
    return
  }
  saving.value = true
  try {
    await data.savePolicyPath({ name: form.name, token: form.token, include_all: form.include_all, provider_ids: form.provider_ids }, editingID.value)
    ui.toast(editingID.value ? 'Policy Path 已更新' : 'Policy Path 已添加')
    dialogOpen.value = false
    editingID.value = ''
  } catch (error) { ui.toast(error.message, true) }
  finally { saving.value = false }
}

function snippet(path) {
  return `[Proxy Group]\n${path.name} = select, policy-path=${path.url}, update-interval=3600`
}

async function copyValue(path, config = false) {
  if (!window.confirm(`复制 Policy Path“${path.name}”${config ? '的 Surge 配置' : '链接'}？内容包含访问 Token。`)) return
  try {
    await copyText(config ? snippet(path) : path.url)
    ui.toast(config ? 'Surge 配置已复制' : 'Policy Path 链接已复制')
  } catch (error) { ui.toast(error.message, true) }
}

async function regenerate(path) {
  if (!window.confirm(`重新生成 Policy Path“${path.name}”的 Token？旧链接会立即失效。`)) return
  busy.add(path.id)
  try {
    await data.regeneratePolicyPathToken(path.id)
    ui.toast('Policy Path Token 已重新生成')
  } catch (error) { ui.toast(error.message, true) }
  finally { busy.delete(path.id) }
}

async function remove(path) {
  if (!window.confirm(`删除 Policy Path“${path.name}”？对应链接会立即失效。`)) return
  busy.add(path.id)
  try {
    await data.deletePolicyPath(path.id)
    ui.toast(`已删除 Policy Path“${path.name}”`)
  } catch (error) { ui.toast(error.message, true) }
  finally { busy.delete(path.id) }
}

function providerNames(path) {
  if (path.include_all) return '全部 Provider，自动包含以后新增的来源'
  const names = new Map(providers.value.map((provider) => [provider.stable_id, provider.name]))
  return path.provider_ids.map((id) => names.get(id) || id).join('、') || '当前没有关联 Provider'
}
</script>

<template>
  <PageHeader eyebrow="POLICY PATHS" title="Policy Path 管理" description="为不同 Surge 策略组发布独立链接，并自由组合需要包含的 Provider。">
    <button class="button primary" type="button" @click="open()">添加 Policy Path</button>
  </PageHeader>

  <nav class="section-nav" aria-label="订阅模块">
    <button type="button" @click="router.push({ name: 'providers' })"><span>Provider</span><small>{{ providers.length }}</small></button>
    <button class="active" type="button" aria-current="page"><span>Policy Path</span><small>{{ policyPaths.length }}</small></button>
  </nav>

  <div class="provider-summary policy-path-summary" data-testid="policy-path-summary">
    <span><b>{{ policyPaths.length }}</b> 个发布链接</span>
    <span><b>{{ policyPaths.reduce((total, path) => total + path.projection_count, 0) }}</b> 个链接内节点</span>
    <span>各链接 Token 可单独轮换</span>
  </div>

  <div class="policy-path-list">
    <article v-for="path in policyPaths" :key="path.id" class="card policy-path-card" data-testid="policy-path-card" :data-policy-path-id="path.id">
      <div class="policy-path-head">
        <div>
          <div class="provider-title-line"><h3>{{ path.name }}</h3><span v-if="path.default" class="pill ok">默认</span><span class="pill" :class="path.token ? 'ok' : 'warn'">{{ path.token ? '独立 Token' : '未设置 Token' }}</span></div>
          <p>{{ providerNames(path) }}</p>
        </div>
        <div class="actions policy-path-actions">
          <button class="button ghost" type="button" @click="open(path)">编辑</button>
          <button class="button ghost" type="button" :disabled="busy.has(path.id)" @click="regenerate(path)">重新生成 Token</button>
          <button v-if="!path.default" class="button danger" type="button" :disabled="busy.has(path.id)" @click="remove(path)">删除</button>
        </div>
      </div>
      <div class="policy-path-facts">
        <div><b>{{ path.provider_count }}</b><span>Provider</span></div>
        <div><b>{{ path.projection_count }}</b><span>当前节点</span></div>
        <div><b>{{ path.include_all ? '自动扩展' : '固定选择' }}</b><span>范围</span></div>
      </div>
      <div v-if="!path.include_all && !path.provider_ids.length" class="banner bad"><b>当前为空</b><span>关联 Provider 已被删除，请重新编辑此链接。</span></div>
      <div class="policy-path-url"><span>Policy Path URL</span><code>{{ path.url }}</code></div>
      <div class="actions policy-path-copy-actions">
        <button class="button" type="button" @click="copyValue(path)">复制链接</button>
        <button class="button primary" type="button" @click="copyValue(path, true)">复制 Surge 配置</button>
      </div>
    </article>
  </div>

  <div v-if="dialogOpen" class="overlay" @click.self="close">
    <form class="modal policy-path-modal" role="dialog" aria-modal="true" aria-labelledby="policy-path-dialog-title" @submit.prevent="save">
      <div class="modal-header"><div><div class="eyebrow">POLICY PATH</div><h2 id="policy-path-dialog-title">{{ editingID ? '编辑 Policy Path' : '添加 Policy Path' }}</h2></div><button class="modal-close" type="button" aria-label="关闭" @click="close">×</button></div>
      <div class="modal-scroll">
        <p>名称用于复制 Surge 策略组配置；链接 ID 不会因改名而变化。</p>
        <section class="form-section">
          <label class="field"><span>名称</span><input v-model.trim="form.name" data-testid="policy-path-name" maxlength="80" required placeholder="例如：香港节点"><small>不能包含等号或换行。</small></label>
          <label class="field"><span>访问 Token</span><input v-model="form.token" data-testid="policy-path-token" type="text" autocomplete="off" minlength="16" :placeholder="editingID ? '留空保持当前值' : '留空自动生成'"><small>{{ editingID ? '可直接设置新值；留空保持当前 Token。' : '至少 16 位；留空时自动生成强随机 Token。' }}</small></label>
        </section>
        <section class="form-section">
          <div class="form-section-title"><b>Provider 范围</b><span>当前选择 {{ selectedCount }} 个</span></div>
          <div class="policy-path-scope">
            <label class="check-row"><input type="radio" name="policy-path-scope" :checked="form.include_all" @change="setScope(true)"><span><b>全部 Provider</b><small>自动包含以后新增的 Provider</small></span></label>
            <label class="check-row"><input type="radio" name="policy-path-scope" :checked="!form.include_all" @change="setScope(false)"><span><b>指定 Provider</b><small>只发布下方勾选的来源</small></span></label>
          </div>
          <div v-if="!form.include_all" class="policy-path-provider-list" data-testid="policy-path-provider-list">
            <label v-for="provider in providers" :key="provider.stable_id" class="check-row">
              <input type="checkbox" :checked="form.provider_ids.includes(provider.stable_id)" @change="toggleProvider(provider.stable_id)">
              <span><b>{{ provider.name }}</b><small>{{ provider.enabled ? '已启用' : '已停用，启用后自动恢复输出' }}</small></span>
            </label>
            <div v-if="!providers.length" class="modal-hint">暂无 Provider，请先添加节点来源。</div>
          </div>
        </section>
        <p v-if="editingID" class="modal-footnote">名称和范围不会更改路径 ID 或节点凭据；修改 Token 会立即使旧链接失效。默认 Policy Path 继续兼容现有 <code>/proxies</code> 地址。</p>
      </div>
      <div class="modal-actions actions"><button class="button" type="button" :disabled="saving" @click="close">取消</button><button class="button primary" type="submit" :disabled="saving || !form.name || (!form.include_all && !form.provider_ids.length)" :aria-busy="saving">{{ saving ? '保存中…' : '保存' }}</button></div>
    </form>
  </div>
</template>
