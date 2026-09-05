<script setup>
import { storeToRefs } from 'pinia'
import { useRealtimeStore } from '@/stores/realtime.js'

const realtime = useRealtimeStore()
const { paused, reduced, streamStatus, lastReceivedAt } = storeToRefs(realtime)
const labels = { paused: '已暂停', idle: '未订阅', connecting: '连接中', connected: '实时', disconnected: '连接中断，正在重连' }
</script>

<template>
  <div class="live-controls">
    <button class="button ghost" type="button" :aria-pressed="paused" @click="realtime.setPaused(!paused)">
      {{ paused ? '继续更新' : '暂停更新' }}
    </button>
    <label class="check-row compact" title="降低实时列表的更新频率">
      <input :checked="reduced" type="checkbox" @change="realtime.setReduced($event.target.checked)">
      低频刷新
    </label>
    <span class="pill" :class="streamStatus === 'connected' ? 'ok' : 'warn'" role="status">{{ labels[streamStatus] }}</span>
    <small v-if="streamStatus === 'disconnected' && lastReceivedAt">最后更新 {{ new Date(lastReceivedAt).toLocaleTimeString() }}</small>
  </div>
</template>
