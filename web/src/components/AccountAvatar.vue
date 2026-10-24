<script setup lang="ts">
import { computed, ref, watch } from 'vue'

const props = defineProps<{ src?: string; name: string }>()
const failed = ref(false)
const initials = computed(() => Array.from(props.name).slice(-2).join('') || '玩家')
watch(() => [props.src, props.name], () => { failed.value = false })
</script>

<template>
  <img
    v-if="src && !failed"
    class="account-avatar-image"
    :src="src"
    :alt="`${name}的头像`"
    referrerpolicy="no-referrer"
    @error="failed = true"
  />
  <span v-else>{{ initials }}</span>
</template>

<style scoped>
.account-avatar-image {
  display: block;
  width: 100%;
  height: 100%;
  border-radius: 50%;
  object-fit: cover;
}
</style>
