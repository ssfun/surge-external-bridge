<script setup>
defineProps({ eyebrow: String, title: String, description: String })
</script>

<template>
  <div class="topbar">
    <div>
      <div class="eyebrow">{{ eyebrow }}</div>
      <h1>{{ title }}</h1>
      <p>{{ description }}</p>
    </div>
    <div class="actions"><slot /></div>
  </div>
</template>
