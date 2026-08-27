<script setup>
import { nextTick, onMounted, ref } from 'vue'
import { login } from '@/api.js'

const emit = defineEmits(['authenticated'])
const tokenInput = ref(null)
const token = ref('')
const errorMessage = ref('')
const busy = ref(false)

onMounted(() => nextTick(() => tokenInput.value?.focus()))

async function submit() {
  errorMessage.value = ''
  busy.value = true
  try {
    await login(token.value.trim())
    token.value = ''
    emit('authenticated')
  } catch (error) {
    errorMessage.value = error.message
    await nextTick()
    tokenInput.value?.focus()
    tokenInput.value?.select()
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="login-page" id="main-content">
    <section class="login-card" aria-labelledby="login-title">
      <div class="login-brand"><div class="mark"><i /><i /><i /><i /></div><div><strong>SurgeEB</strong><span>MIHOMO PROTOCOL BRIDGE</span></div></div>
      <div class="eyebrow">MANAGEMENT CONSOLE</div>
      <h1 id="login-title">登录配置台</h1>
      <p>输入网关设置中的 Management Token。验证通过后，浏览器会使用受保护的会话 Cookie 保持登录。</p>
      <form @submit.prevent="submit">
        <label class="field"><span>Management Token</span><input ref="tokenInput" v-model="token" data-testid="login-token" type="password" autocomplete="current-password" required minlength="16" spellcheck="false"></label>
        <div v-if="errorMessage" class="banner bad" role="alert">{{ errorMessage }}</div>
        <button class="button primary login-submit" type="submit" :disabled="busy || token.trim().length < 16" :aria-busy="busy">{{ busy ? '正在验证…' : '登录' }}</button>
      </form>
      <small>Token 只用于建立当前浏览器会话，不会保存在网页存储中。</small>
    </section>
  </main>
</template>
