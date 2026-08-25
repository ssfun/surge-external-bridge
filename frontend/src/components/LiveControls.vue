<script setup>
import { storeToRefs } from 'pinia'
import { useRealtimeStore } from '@/stores/realtime.js'

const realtime = useRealtimeStore()
const { paused, reduced } = storeToRefs(realtime)
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
    <span class="pill" :class="paused ? 'warn' : 'ok'">{{ paused ? '已暂停' : '实时' }}</span>
  </div>
</template>
