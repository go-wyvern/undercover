<script setup lang="ts">
import { computed } from 'vue'
import { Bot, Check, Crown, LockKeyhole, Plus, Users, X } from '@lucide/vue'
import AccountAvatar from './AccountAvatar.vue'
import type { Player, RoomView } from '../types'

const props = defineProps<{ room: RoomView; busy: boolean; voiceConfigured: boolean }>()
const emit = defineEmits<{
  ready: [value: boolean]
  autoFill: [value: boolean]
  aiSeat: [value: { seat: number; bot: boolean }]
  remove: [player: Player]
}>()
const players = computed(() => props.room.lobbyPlayers || props.room.players)
const me = computed(() => players.value.find(p => p.id === props.room.myPlayerId))
const guests = computed(() => players.value.filter(p => !p.bot && p.id !== props.room.hostPlayerId))
const readyCount = computed(() => guests.value.filter(p => p.ready).length)
const seats = computed(() => Array.from({ length: props.room.capacity }, (_, i) => ({
  seat: i + 1, player: players.value.find(p => p.seat === i + 1),
})))
</script>

<template>
  <section class="lobby-controls" aria-label="开局准备">
    <header class="lobby-controls-heading">
      <h2><Users :size="17" />{{ room.phase === 'finished' ? '下一局准备' : '开局准备' }}</h2>
      <span v-if="room.passwordRequired" class="protected-label"><LockKeyhole :size="12" />口令房</span>
    </header>
    <div class="readiness-summary" role="status">
      <b>{{ room.canStart ? '这一桌，准备好了' : room.startBlockReason }}</b>
      <p>{{ guests.length ? `${readyCount} / ${guests.length} 位同桌已准备，房主开局即视为准备` : '一个人也能玩，空位可由 AI 补齐' }}</p>
    </div>
    <button v-if="!room.isHost" class="button ready-button" :class="me?.ready ? 'secondary' : 'primary'"
      :disabled="busy" @click="emit('ready', !me?.ready)">
      <Check :size="17" />{{ me?.ready ? '已准备 · 取消准备' : room.phase === 'finished' ? '准备下一局' : '我准备好了' }}
    </button>
    <p v-else class="host-start-note">{{ room.canStart ? voiceConfigured ? '对灵矽说“开始游戏”，就能发牌。' : '准备好了，点击桌面上的“开始体验”。' : '等同桌准备好，房主就能开局。' }}</p>
    <label v-if="room.isHost" class="ai-fill-setting">
      <span><Bot :size="17" />空位自动由 AI 补齐</span>
      <input type="checkbox" :checked="room.autoFillAI" :disabled="busy"
        @change="emit('autoFill', ($event.target as HTMLInputElement).checked)" />
    </label>
    <p v-else class="lobby-fill-note">{{ room.autoFillAI ? '空位将由 AI 自动补齐' : '房主已关闭自动补位，坐满后开局' }}</p>
    <ul class="lobby-seat-list">
      <li v-for="item in seats" :key="item.seat">
        <span class="lobby-seat-number">{{ String(item.seat).padStart(2, '0') }}</span>
        <span class="lobby-seat-avatar" :class="`avatar-color-${item.seat}`">
          <Bot v-if="item.player?.bot" :size="18" />
          <AccountAvatar v-else-if="item.player" :src="item.player.avatar" :name="item.player.name" />
          <Plus v-else :size="17" />
        </span>
        <div class="lobby-seat-copy">
          <b>{{ item.player?.name || '等待入座' }}<small v-if="item.player?.id === room.myPlayerId">你</small></b>
          <span v-if="!item.player">{{ room.autoFillAI ? '开局时 AI 补位' : '留给朋友的位置' }}</span>
          <span v-else-if="item.player.id === room.hostPlayerId"><Crown :size="11" />房主</span>
          <span v-else-if="item.player.bot">AI 玩家 · 已准备</span>
          <span v-else :class="{ 'player-ready': item.player.ready }">{{ item.player.ready ? '已准备' : '待准备' }}</span>
        </div>
        <template v-if="room.isHost">
          <button v-if="!item.player" class="lobby-seat-action" :disabled="busy"
            :aria-label="`为${item.seat}号添加 AI`" @click="emit('aiSeat', { seat: item.seat, bot: true })"><Plus :size="13" />AI</button>
          <button v-else-if="item.player.bot" class="lobby-seat-action" :disabled="busy"
            :aria-label="`移除${item.seat}号 AI`" @click="emit('aiSeat', { seat: item.seat, bot: false })"><X :size="14" />AI</button>
          <button v-else-if="item.player.id !== room.myPlayerId" class="lobby-seat-action"
            :disabled="busy" :aria-label="`移除${item.seat}号${item.player.name}`" @click="emit('remove', item.player)">移除</button>
        </template>
        <Check v-else-if="item.player?.ready" class="player-ready" :size="16" />
      </li>
    </ul>
    <p class="readiness-reset-note">成员、AI 席位或补位设置变化后，需要重新准备。</p>
  </section>
</template>
