<script setup lang="ts">
import RobotMascot from './RobotMascot.vue'
defineProps<{ theme: string; large?: boolean }>()
</script>
<template>
  <div class="room-art" :class="[theme, { large }]" aria-hidden="true">
    <div class="art-orbit orbit-one"></div>
    <div class="art-orbit orbit-two"></div>
    <div class="art-table">
      <div class="art-stitch"></div>
      <span class="art-table-label">LINX CLUB</span>
    </div>
    <div class="art-card card-one"><span>?</span></div>
    <div class="art-card card-two"><span>✦</span></div>
    <div class="art-card card-three"><span>?</span></div>
    <RobotMascot class="art-robot" />
    <div class="art-chip chip-one"></div>
    <div class="art-chip chip-two"></div>
    <div class="art-spark spark-one">＋</div>
    <div class="art-spark spark-two">✧</div>
  </div>
</template>
