<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import {
  ArrowLeft,
  ArrowRight,
  ArrowUpRight,
  AudioLines,
  Check,
  CheckCheck,
  CircleHelp,
  Clock3,
  DoorOpen,
  Headphones,
  LoaderCircle,
  LogOut,
  LockKeyhole,
  Trash2,
  MessageCircle,
  Mic,
  Plus,
  Search,
  Send,
  ShieldCheck,
  Sparkles,
  Users,
  Volume2,
  VolumeX,
  X,
  Eye,
  EyeOff,
  Crown,
  Copy,
  RotateCcw,
  Trophy,
  Circle,
  Bot,
  Coffee,
} from '@lucide/vue'
import RobotMascot from './components/RobotMascot.vue'
import AccountAvatar from './components/AccountAvatar.vue'
import RoomArt from './components/RoomArt.vue'
import LobbyControls from './components/LobbyControls.vue'
import { api, ApiError } from './api'
import { usePushToTalk } from './composables/usePushToTalk'
import { useNarration } from './composables/useNarration'
import { playbackReadyResult, pendingPlaybackLogs } from './composables/playbackGate'
import { canStartVoice, voiceTurnAllowed } from './composables/voiceTurn'
import { seatPosition as relativeSeatPosition, tableCardPosition as relativeCardPosition } from './composables/tableLayout'
import type { AccountInfo, AudioCue, Config, GameLog, Player, RoomSummary, RoomView, User } from './types'

const config = ref<Config>({
  voiceConfigured: false,
  aiConfigured: false,
  authEnabled: false,
  allowGuest: false,
})
const user = ref<User | null>(null)
const rooms = ref<RoomSummary[]>([])
const room = ref<RoomView | null>(null)
const loading = ref(true)
const bootError = ref('')
const busy = ref(false)
const search = ref('')
const filter = ref('all')
const modal = ref<'login' | 'create' | 'rules' | 'leave' | 'close' | 'join' | 'remove' | null>(null)
const nickname = ref('')
const roomName = ref('')
const roomCapacity = ref(4)
const roomTheme = ref('jade')
const protectedRoom = ref(false)
const roomPassword = ref('')
const joinPassword = ref('')
const autoFillAI = ref(true)
const removingPlayer = ref<Player | null>(null)
const modalError = ref('')
const toast = ref('')
const draft = ref('')
const pcm = ref<Uint8Array | null>(null)
const selectedVote = ref('')
const peek = ref(false)
const now = ref(Date.now())
const clockOffset = ref(0)
const playedLiveLogs = new Set<string>()
const textWindows = new Set<string>()
let acknowledging = ''
let retryPlaybackAt = 0
const reconnecting = ref(false)
const transcript = ref<HTMLElement | null>(null)
let pendingRoom = ''
let source: EventSource | null = null
let knownLogs = new Set<string>()
let historicalLogs = new Set<string>()
const heardLogs = ref(new Set<string>())
let toastTimer: ReturnType<typeof setTimeout>
let pollTimer: ReturnType<typeof setInterval>
let clockTimer: ReturnType<typeof setInterval>
let lastVoiceError = 0

function notify(message: string) {
  toast.value = message
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => {
    toast.value = ''
  }, 4200)
}
const narration = useNarration((message) => {
  if (Date.now() - lastVoiceError > 30000) {
    notify(message)
    lastVoiceError = Date.now()
  }
})
const narrationLabel = computed(() => {
  if (!narration.enabled.value) return '语音播报已关闭'
  if (narration.state.value === 'blocked') return '点击开启声音，继续收听本桌播报'
  if (narration.state.value === 'error') return '播报暂未成功，可以重试或重听'
  if (narration.state.value === 'loading') return '正在准备语音…'
  if (narration.state.value === 'playing') {
    const cue = narration.activeCue.value
    return cue?.playerId ? `${cue.seat} 号正在发言` : '灵矽 AI 主持正在播报'
  }
  if (room.value?.agentActive) return '灵矽正在组织 AI 回合…'
  return '语音已开启 · 开局、发言和结果自动播报'
})
const narrationSource = computed(() =>
  narration.playbackSource.value === 'recording' ? '玩家原声'
    : narration.playbackSource.value === 'browser' || !config.value.voiceConfigured || narration.fallback.value
      ? '浏览器语音' : '灵矽音色',
)
function prepareNarration() { if (narration.enabled.value) narration.activate() }
function replayLog(log: GameLog) {
  if (room.value) narration.replay(room.value.id, log, config.value.voiceConfigured)
}
function replayLatest() {
  const log = room.value?.logs.at(-1)
  if (log) replayLog(log)
}
const voice = usePushToTalk((text, data) => {
  if (room.value?.phase === 'voting') {
    const match = text.match(/(?:投|选)(?:给)?\s*([一二三四五六123456])/)
    if (match) {
      const seat = '一二三四五六'.indexOf(match[1]!) + 1 || Number(match[1])
      const player = room.value.players.find((player) => player.seat === seat)
      if (player && room.value.legalTargets.includes(player.id)) {
        selectedVote.value = player.id
        notify(`已识别：投给 ${seat} 号，请确认投票`)
        return
      }
    }
    notify('请点击你想投的玩家，再确认投票')
  } else {
    draft.value = text
    pcm.value = data
    notify('已识别你的发言，确认后发送')
  }
}, notify, (id) => knownLogs.add(id), () => narration.enabled.value)

const audibleCue = computed(() => voice.streaming.value ? voice.activeCue.value
  : narration.state.value === 'playing' ? narration.activeCue.value : null)
const speakingPlayerId = computed(() => voice.recording.value ? room.value?.myPlayerId : audibleCue.value?.playerId)
function rememberCue(cue: AudioCue | null) {
  if (cue?.logId) heardLogs.value = new Set([...heardLogs.value, cue.logId])
}
watch(audibleCue, rememberCue)
watch(narration.completedLiveLog, (log) => {
  if (log) { playedLiveLogs.add(log.id); for (const cue of log.audioCues || []) if (cue.logId) playedLiveLogs.add(cue.logId) }
  syncPlaybackGate()
})
watch(narration.enabled, () => syncPlaybackGate())
watch(narration.completedLog, (log) => {
  if (!log) return
  const ids = log.audioCues?.flatMap(cue => cue.logId ? [cue.logId] : []) || []
  heardLogs.value = new Set([...heardLogs.value, log.id, ...ids])
  if (narration.replaying.value) syncPlaybackGate()
})
const visibleLogs = computed(() => {
  const visible: GameLog[] = []
  for (const log of room.value?.logs || []) {
    const bot = log.kind === 'speech' && room.value?.players.find(p => p.id === log.playerId)?.bot
    if (narration.enabled.value && bot && !historicalLogs.has(log.id) && !heardLogs.value.has(log.id)) break
    visible.push(log)
    if (audibleCue.value?.logId === log.id && !historicalLogs.has(log.id)) break
  }
  return visible
})
const listeningToPlayers = computed(() => !!audibleCue.value?.playerId ||
  (narration.enabled.value && (room.value?.logs.some(l => l.kind === 'speech' &&
    room.value?.players.find(p => p.id === l.playerId)?.bot && !historicalLogs.has(l.id) && !heardLogs.value.has(l.id)))))
function seatBubble(player?: Player) {
  if (!player) return ''
  if (voice.recording.value && player.id === room.value?.myPlayerId) return '正在说话…'
  return audibleCue.value?.playerId === player.id ? audibleCue.value.text : ''
}
function publicLogText(log: GameLog) {
  return log.source === 'linx_agent' && log.audioCues?.some(c => c.playerId) ? '本轮主持语音 · 点击右侧重听' : log.text
}

async function confirmPlayback(result: 'played' | 'muted' | 'unavailable') {
  const value = room.value
  const gate = value?.action
  if (!value || !gate?.required || gate.ready || gate.state !== 'waiting_playback' || acknowledging === gate.id) return
  acknowledging = gate.id
  const sentAt = Date.now()
  if (result !== 'played') {
    textWindows.add(gate.id)
    narration.cancel()
    heardLogs.value = new Set([...heardLogs.value, ...value.logs.map(log => log.id)])
  }
  try {
    const updated = await api<RoomView>(`/api/rooms/${encodeURIComponent(value.id)}/playback-ready`, {
      turnId: gate.id, throughLogId: gate.boundaryId, result,
    })
    if (room.value?.id === value.id) {
      updateRoom(updated)
      if (updated.serverNow) clockOffset.value = updated.serverNow - (sentAt + Date.now()) / 2
    }
  } catch (error) {
    retryPlaybackAt = Date.now() + 3000
    if (!(error instanceof ApiError && error.status === 409)) notify((error as Error).message)
  } finally { if (acknowledging === gate.id) acknowledging = '' }
}
function syncPlaybackGate() {
  const value = room.value
  const gate = value?.action
  if (!value || !gate) return
  if (gate.textOnly && !textWindows.has(gate.id)) {
    textWindows.add(gate.id)
    narration.cancel()
    heardLogs.value = new Set([...heardLogs.value, ...value.logs.map(log => log.id)])
  }
  if (gate.state !== 'waiting_playback' || !gate.required || gate.ready || Date.now() < retryPlaybackAt) return
  const result = textWindows.has(gate.id) ? 'unavailable' : playbackReadyResult(gate, narration.enabled.value, playedLiveLogs)
  if (result) { void confirmPlayback(result); return }
  if (!narration.replaying.value || narration.state.value === 'idle')
    narration.enqueue(value.id, pendingPlaybackLogs(value, playedLiveLogs), config.value.voiceConfigured)
}
function stopPlayback() {
  if (room.value?.action?.required && room.value.action.state === 'waiting_playback') void confirmPlayback('unavailable')
  heardLogs.value = new Set([...heardLogs.value, ...(room.value?.logs.filter(l => l.kind === 'speech').map(l => l.id) || [])])
  voice.cancel()
  narration.cancel()
}
function toggleSound() {narration.toggle(); if (!narration.enabled.value) voice.muteOutput()}

const filteredRooms = computed(() =>
  rooms.value.filter(
    (r) =>
      (!search.value || r.name.includes(search.value)) &&
      (filter.value !== 'waiting' || r.phase === 'waiting' || r.phase === 'finished'),
  ),
)
const availableCount = computed(
  () => rooms.value.filter((r) => r.phase === 'waiting' || r.phase === 'finished').length,
)
const me = computed(() => room.value?.players.find((p) => p.id === room.value?.myPlayerId))
const speaker = computed(() =>
  room.value?.players.find((p) => p.id === room.value?.currentPlayerId),
)
const myTurn = computed(
  () =>
    !!room.value &&
    ['speaking', 'tiebreak'].includes(room.value.phase) && room.value.action?.state !== 'waiting_playback' &&
    room.value.currentPlayerId === room.value.myPlayerId,
)
const canVote = computed(
  () => room.value?.phase === 'voting' && room.value.action?.state !== 'waiting_playback' && !room.value.agentActive && me.value?.alive && !me.value.hasVoted && !listeningToPlayers.value,
)
const secondsLeft = computed(() =>
  room.value?.deadline ? Math.max(0, Math.ceil((room.value.deadline - now.value - clockOffset.value) / 1000)) : 0,
)
const seats = computed(() =>
  Array.from({ length: room.value?.capacity || 4 }, (_, i) => ({
    number: i + 1,
    player: room.value?.players.find((p) => p.seat === i + 1),
  })),
)
const selectedPlayer = computed(() => room.value?.players.find((p) => p.id === selectedVote.value))
const phaseTitle = computed(
  () => listeningToPlayers.value ? '描述环节 · 请收听' :
    ({
      waiting: '等待入座',
      dealing: '灵矽正在发牌',
      speaking: '描述环节',
      tiebreak: '平票加赛',
      voting: '投票环节',
      finished: '本局已结束',
    })[room.value?.phase || 'waiting'],
)
const phaseHint = computed(() => {
  if (!room.value) return ''
  if (audibleCue.value?.playerId) return `正在听 ${audibleCue.value.seat} 号发言，留意这句话里的线索`
  if (listeningToPlayers.value) return 'AI 玩家已准备好描述，正在等待语音播报'
  if (room.value.action?.state === 'waiting_playback') return room.value.action.ready ? '收听已完成，等待其他玩家或主持处理结束' : '正在收听前置播报，完成后才开始行动倒计时'
  if (room.value.phase === 'waiting' && room.value.startBlockReason) return room.value.startBlockReason
  if (room.value.phase === 'waiting' && !room.value.autoFillAI) return '这一桌已坐满，等房主开始游戏'
  if (room.value.phase === 'waiting')
    return config.value.voiceConfigured
      ? room.value.isHost ? '朋友随时可加入，准备好后对灵矽说“开始游戏”' : '大家已入座，等待房主对灵矽说“开始游戏”'
      : room.value.isHost ? '朋友随时可加入，空位由演示玩家补齐' : '大家已入座，等待房主开始体验'
  if (room.value.phase === 'dealing') return '每个人的词卡，只有自己能看见'
  if (room.value.phase === 'finished')
    return room.value.winner === 'civilian'
      ? '藏不住了，平民找到了卧底！'
      : '完美伪装，卧底笑到了最后！'
  if (!me.value?.alive) return '你已出局，留在这里看看最后的答案吧'
  if (room.value.phase === 'voting')
    return me.value.hasVoted ? '你的票已收好，等待其他玩家' : '谁的描述不太一样？点击玩家投票'
  return myTurn.value
    ? '轮到你了，用一句话描述，别直接说出词语'
    : `等待 ${speaker.value?.seat || ''} 号 ${speaker.value?.name || ''} 发言`
})
const turnProgress = computed(() => {
  const r = room.value
  if (!r || r.phase === 'waiting') return 0
  if (r.phase === 'finished') return 100
  if (r.phase === 'voting') return 80
  return 25 + ((speaker.value?.seat || 1) / r.capacity) * 40
})
const playbackPending = computed(() => !room.value?.action?.required && narration.enabled.value &&
  (narration.queuedCount.value > 0 || narration.state.value === 'loading' || narration.state.value === 'playing'))
const canRecord = computed(() => {
  if (!config.value.voiceConfigured || busy.value) return false
  // Keep a held button enabled so pointer-up still flushes this recording.
  if (voice.recording.value || voice.connecting.value || voice.recognizing.value) return true
  return canStartVoice(room.value, playbackPending.value)
})
const voiceWaitText = computed(() => {
  if (playbackPending.value) {
    const seat = audibleCue.value?.seat || narration.current.value?.seat
    return seat ? `正在播放${seat}号发言` : '请先收听主持播报'
  }
  const value = room.value
  if (!value) return '请先入座'
  if (value.action?.state === 'waiting_playback') return value.action.ready ? '等待开放行动' : '请先完成前置收听'
  if (value.agentActive) return '主持正在处理，请稍候'
  if (value.phase === 'dealing') return '正在发牌'
  if (!voiceTurnAllowed(value)) {
    if (value.phase === 'waiting') return '等待房主开始'
    if (value.phase === 'finished') return '等待房主再开一局'
    if (!me.value?.alive) return '正在观战'
    if (value.phase === 'voting') return '已投票，等待其他玩家'
    return speaker.value ? `等待${speaker.value.seat}号发言` : '等待自己的回合'
  }
  return ''
})
const pttText = computed(() =>
  voice.recording.value
    ? `松开结束 · ${voice.seconds.value}s`
    : voice.recognizing.value
      ? voice.connecting.value ? '正在发送录音' : voice.assistant.value ? '灵矽正在回应' : '正在识别'
      : voice.connecting.value
        ? '正在开启麦克风'
        : config.value.voiceConfigured
          ? voiceWaitText.value || '按住和灵矽说话'
          : me.value?.alive === false
            ? '正在观战'
            : '听听大家怎么说',
)

const dealerCaption = computed(() => {
  const cue = audibleCue.value
  if (cue) return cue.playerId ? `${cue.seat} 号正在发言` : '主持播报中'
  const r = room.value
  if (!r) return ''
  if (r.phase === 'dealing') return '嘘，你的秘密正在路上…'
  if (r.phase === 'waiting' && r.startBlockReason) return r.startBlockReason
  if (r.phase === 'waiting')
    return config.value.voiceConfigured
      ? r.isHost ? '牌已洗好，跟我说“开始游戏”吧。' : '等房主说“开始游戏”，就能发牌。'
      : '牌已洗好，准备开始体验。'
  if (r.phase === 'finished')
    return config.value.voiceConfigured
      ? r.isHost ? '想继续玩，跟我说“再来一局”。' : '房主说“再来一局”，大家就能继续。'
      : '谢谢各位，好戏待续。'
  return speaker.value?.bot ? `${speaker.value.name}正在组织语言…` : '每一句话，都可能是线索。'
})

const voiceHint = computed(() => {
  if (voice.recording.value) return voice.connecting.value ? '正在录音并连接灵矽，松开后自动发送' : '正在录音，最长 30 秒'
  if (!config.value.voiceConfigured) return '语音尚未开启，可使用体验模式和文字操作'
  if (voiceWaitText.value) return `${voiceWaitText.value}，轮到你后即可按住说话`
  if (room.value?.phase === 'waiting')
    return '按住说：开始游戏'
  if (room.value?.phase === 'finished')
    return '按住说：再来一局'
  if (room.value?.phase === 'dealing') return '词卡正在分发，请稍候'
  return '松开后由灵矽理解并执行'
})

function seatPosition(number: number) {
  return relativeSeatPosition(number, me.value?.seat || 1, room.value?.capacity === 6 ? 6 : 4)
}
function tableCardPosition(number: number) {
  return relativeCardPosition(number, me.value?.seat || 1, room.value?.capacity === 6 ? 6 : 4)
}
function tableCardWord(player?: Player) {
  if (!player) return ''
  if (room.value?.phase === 'finished') return room.value.reveals.find(r => r.playerId === player.id)?.word || ''
  return player.id === room.value?.myPlayerId && peek.value ? room.value?.myWord || '' : ''
}
function tableCardRole(player?: Player) {
  return room.value?.reveals.find(r => r.playerId === player?.id)?.role === 'undercover' ? '卧底' : '平民'
}
function openModal(value: typeof modal.value) {
  modal.value = value
  modalError.value = ''
  void nextTick(() => document.querySelector<HTMLInputElement>('.modal input')?.focus())
}
function updateRoom(value: RoomView, initial = false) {
  if (room.value?.id === value.id && value.version < room.value.version) return
  const previous = room.value
  now.value = Date.now()
  if (initial) clockOffset.value = value.serverNow ? value.serverNow - now.value : 0
  if (initial) {
    playedLiveLogs.clear()
    textWindows.clear()
    knownLogs = new Set(value.logs.map((log) => log.id))
    const pending = new Set(value.action?.state === 'waiting_playback' && value.action.required && !value.action.ready ? value.action.logIds : [])
    historicalLogs = new Set([...knownLogs].filter(id => !pending.has(id)))
    heardLogs.value = new Set(historicalLogs)
    narration.cancel()
  }
  if (previous?.gameId !== value.gameId) {
    if (!initial) {historicalLogs.clear(); heardLogs.value = new Set(); playedLiveLogs.clear(); textWindows.clear()}
    narration.cancel()
    peek.value = false
    selectedVote.value = ''
    draft.value = ''
    pcm.value = null
    if (!voice.assistant.value) voice.cancel()
  }
  if (
    previous?.phase !== value.phase ||
    previous?.round !== value.round ||
    previous?.currentPlayerId !== value.currentPlayerId
  ) {
    selectedVote.value = ''
    draft.value = ''
    pcm.value = null
    if (!voice.assistant.value) voice.cancel()
  }
  room.value = value
  if (value.myDraft && (!draft.value || value.myDraft !== previous?.myDraft)) draft.value = value.myDraft
  if (!initial) {
    const added = value.logs.filter((log) => !knownLogs.has(log.id))
    for (const log of added) knownLogs.add(log.id)
    narration.enqueue(
      value.id,
      added,
      config.value.voiceConfigured,
    )
  }
  syncPlaybackGate()
  void nextTick(() => {
    if (transcript.value) transcript.value.scrollTop = transcript.value.scrollHeight
  })
}
function subscribe(id: string) {
  source?.close()
  const stream = new EventSource(`/api/rooms/${encodeURIComponent(id)}/events`)
  source = stream
  stream.addEventListener('room', (event) => {
    if (source !== stream) return
    reconnecting.value = false
    updateRoom(JSON.parse((event as MessageEvent).data))
  })
  stream.addEventListener('removed', () => {
    if (source === stream) void returnToLobby('你已离开房间或被房主移除')
  })
  stream.addEventListener('closed', () => {
    if (source === stream) void returnToLobby('房主已关闭房间')
  })
  stream.onopen = () => {
    if (source === stream) reconnecting.value = false
  }
  stream.onerror = () => {
    if (source !== stream) return
    reconnecting.value = true
    void api<RoomView>(`/api/rooms/${encodeURIComponent(id)}`).catch((error) => {
      if (source === stream && error instanceof ApiError && [403, 404].includes(error.status))
        void returnToLobby(error.status === 404 ? '房间已关闭' : '你已离开房间或被房主移除')
    })
  }
}
function enter(value: RoomView) {
  updateRoom(value, true)
  history.replaceState(null, '', `/room/${encodeURIComponent(value.id)}`)
  subscribe(value.id)
  pendingRoom = ''
  joinPassword.value = ''
  roomPassword.value = ''
  modal.value = null
}
async function refreshRooms() {
  rooms.value = (await api<{ rooms: RoomSummary[] }>('/api/rooms')).rooms
}
async function boot() {
  loading.value = true
  bootError.value = ''
  try {
    const [cfg, account] = await Promise.all([
      api<Config>('/api/config'),
      api<AccountInfo>('/api/auth/me'),
      refreshRooms(),
    ])
    config.value = cfg
    user.value = account.user
    const pathId = location.pathname.startsWith('/room/')
      ? decodeURIComponent(location.pathname.slice(6))
      : ''
    if (account.user && account.currentRoomId)
      enter(await api<RoomView>(`/api/rooms/${encodeURIComponent(account.currentRoomId)}`))
    else if (pathId) await joinRoom(pathId)
  } catch (error) {
    bootError.value = error instanceof Error ? error.message : '连接没有成功'
  } finally {
    loading.value = false
  }
}
async function joinRoom(id: string, password?: string) {
  prepareNarration()
  if (!user.value) {
    pendingRoom = id
    openModal('login')
    return
  }
  if (password === undefined && rooms.value.find(r => r.id === id)?.passwordRequired) {
    pendingRoom = id
    joinPassword.value = ''
    openModal('join')
    return
  }
  busy.value = true
  modalError.value = ''
  try {
    enter(await api<RoomView>(`/api/rooms/${encodeURIComponent(id)}/join`, { password: password || '' }))
  } catch (error) {
    if (error instanceof ApiError && error.code?.startsWith('room_password_')) {
      pendingRoom = id
      if (modal.value !== 'join') { joinPassword.value = ''; openModal('join') }
      if (error.code === 'room_password_invalid') modalError.value = error.message
    } else if (modal.value === 'join') modalError.value = (error as Error).message
    else notify((error as Error).message)
  } finally {
    busy.value = false
  }
}
function submitRoomPassword() {
  if (pendingRoom && !busy.value) void joinRoom(pendingRoom, joinPassword.value)
}
async function guestLogin() {
  busy.value = true
  modalError.value = ''
  try {
    const response = await api<{ user: User }>('/api/auth/guest', { displayName: nickname.value })
    user.value = response.user
    modal.value = null
    if (pendingRoom) await joinRoom(pendingRoom)
  } catch (error) {
    modalError.value = (error as Error).message
  } finally {
    busy.value = false
  }
}
function requestLogin() { pendingRoom = ''; openModal('login') }
function resetRoomSearch() { search.value = ''; filter.value = 'all' }
function accountLogin() {
  const target = pendingRoom ? `/room/${encodeURIComponent(pendingRoom)}` : location.pathname
  location.href = `/auth/login?return_to=${encodeURIComponent(target)}`
}
function requestCreate() {
  if (!user.value) {
    pendingRoom = ''
    openModal('login')
    return
  }
  roomName.value = `${user.value.displayName}的卧底局`
  roomPassword.value = ''
  protectedRoom.value = false
  autoFillAI.value = true
  openModal('create')
}
async function createRoom() {
  prepareNarration()
  busy.value = true
  modalError.value = ''
  try {
    enter(
      await api<RoomView>('/api/rooms', {
        name: roomName.value,
        capacity: roomCapacity.value,
        theme: roomTheme.value,
        password: protectedRoom.value ? roomPassword.value : '',
        autoFillAI: autoFillAI.value,
      }),
    )
    await refreshRooms()
  } catch (error) {
    modalError.value = (error as Error).message
  } finally {
    busy.value = false
  }
}
async function action(path: string, body: unknown = {}) {
  if (!room.value || busy.value) return false
  const id = room.value.id
  if (path === 'start' || path === 'speech') prepareNarration()
  busy.value = true
  try {
    const value = await api<RoomView>(`/api/rooms/${encodeURIComponent(id)}/${path}`, body)
    if (room.value?.id !== id) return false
    updateRoom(value)
    return true
  } catch (error) {
    notify((error as Error).message)
    return false
  } finally {
    busy.value = false
  }
}
function requestRemove(player: Player) {
  removingPlayer.value = player
  openModal('remove')
}
async function removeMember() {
  if (!removingPlayer.value) return
  if (await action(`members/${encodeURIComponent(removingPlayer.value.id)}/remove`)) {
    modal.value = null
    removingPlayer.value = null
  }
}
function audioBase64(data: Uint8Array) {
  let binary = ''
  for (let i = 0; i < data.length; i += 8192)
    binary += String.fromCharCode(...data.subarray(i, i + 8192))
  return btoa(binary)
}
async function sendSpeech() {
  const text = draft.value.trim()
  if (!text) return
  await action('speech', { text, ...(pcm.value ? { audio: audioBase64(pcm.value) } : {}) })
}
function chooseVote(player?: Player) {
  if (!canVote.value || !player || !room.value?.legalTargets.includes(player.id)) return
  selectedVote.value = player.id
}
function startRecording(event?: PointerEvent) {
  if (!canRecord.value || !room.value) return
  event?.preventDefault()
  if (event) (event.currentTarget as HTMLElement).setPointerCapture(event.pointerId)
  stopPlayback()
  prepareNarration()
  void voice.start(room.value.id, true)
}
async function returnToLobby(message = '') {
  source?.close()
  source = null
  voice.cancel()
  narration.cancel()
  room.value = null
  peek.value = false
  modal.value = null
  reconnecting.value = false
  selectedVote.value = ''
  draft.value = ''
  pcm.value = null
  playedLiveLogs.clear()
  textWindows.clear()
  acknowledging = ''
  retryPlaybackAt = 0
  knownLogs.clear()
  historicalLogs.clear()
  heardLogs.value = new Set()
  history.replaceState(null, '', '/')
  if (message) notify(message)
  try { await refreshRooms() } catch (error) { notify((error as Error).message) }
}
async function closeRoom() {
  if (!room.value?.isHost || busy.value) return
  const id = room.value.id
  busy.value = true
  modalError.value = ''
  try {
    await api(`/api/rooms/${encodeURIComponent(id)}`, undefined, 'DELETE')
    if (room.value?.id === id) await returnToLobby('房间已关闭')
  } catch (error) {
    modalError.value = (error as Error).message
  } finally { busy.value = false }
}
async function leaveRoom() {
  if (!room.value) return
  busy.value = true
  try {
    await api(`/api/rooms/${encodeURIComponent(room.value.id)}/leave`, {})
    await returnToLobby()
  } catch (error) {
    notify((error as Error).message)
  } finally { busy.value = false }
}
function requestLeave() {
  if (!room.value || busy.value) return
  if (['waiting', 'finished'].includes(room.value.phase)) void leaveRoom()
  else openModal('leave')
}
async function logout() {
  try {
    await api('/auth/logout', {})
    source?.close()
    voice.cancel()
    narration.cancel()
    room.value = null
    user.value = null
    history.replaceState(null, '', '/')
    await refreshRooms()
  } catch (error) {
    notify((error as Error).message)
  }
}
async function copyRoom() {
  try {
    await navigator.clipboard.writeText(location.href)
    notify('房间链接已复制，发给朋友一起入座吧')
  } catch {
    notify(`房间地址：${location.href}`)
  }
}
function keydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    modal.value = null
    voice.cancel()
  }
}
function visibilityChanged() {
  if (document.hidden) voice.cancel()
}
function pageChanged() {
  if (room.value) history.replaceState(null, '', `/room/${encodeURIComponent(room.value.id)}`)
}
onMounted(() => {
  void boot()
  pollTimer = setInterval(() => {
    if (!room.value) void refreshRooms().catch(() => {})
  }, 5000)
  clockTimer = setInterval(() => {
    now.value = Date.now()
    syncPlaybackGate()
  }, 1000)
  document.addEventListener('keydown', keydown)
  document.addEventListener('visibilitychange', visibilityChanged)
  window.addEventListener('popstate', pageChanged)
  window.addEventListener('blur', voice.cancel)
})
onUnmounted(() => {
  source?.close()
  voice.cancel()
  narration.cancel()
  clearInterval(pollTimer)
  clearInterval(clockTimer)
  clearTimeout(toastTimer)
  document.removeEventListener('keydown', keydown)
  document.removeEventListener('visibilitychange', visibilityChanged)
  window.removeEventListener('popstate', pageChanged)
  window.removeEventListener('blur', voice.cancel)
})
</script>

<template>
  <div class="app-shell">
    <div class="workspace">
      <header class="topbar">
        <a class="wordmark" href="/" @click.prevent="room ? requestLeave() : undefined">
          <span class="brand-mark" aria-hidden="true"><RobotMascot small /></span>
          <span class="wordmark-name">卧底俱乐部<span class="wordmark-dot">.</span></span>
          <span class="by-linx">WITH LINX AI</span>
        </a>
        <div class="topbar-right">
          <span
            v-if="!config.aiConfigured && !loading"
            class="demo-label"
            title="当前使用演示玩家；绑定灵矽智能体后可开启完整语音体验"
            >体验模式</span
          >
          <button class="text-button rules-top" aria-label="玩法说明" @click="openModal('rules')">
            <CircleHelp :size="16" /><span>玩法说明</span>
          </button>
          <span class="topbar-divider"></span>
          <template v-if="user"
            ><div class="profile">
              <span class="profile-avatar">
                <AccountAvatar :src="user.avatar" :name="user.displayName" />
              </span>
              <span>{{ user.displayName }}</span>
            </div>
            <button
              class="icon-button logout"
              aria-label="退出登录"
              title="退出登录"
              @click="logout"
            >
              <LogOut :size="17" /></button
          ></template>
          <button
            v-else
            class="login-button"
            @click="requestLogin"
          >
            登录入座<ArrowUpRight :size="15" />
          </button>
        </div>
      </header>

      <main v-if="loading" class="loading-page">
        <LoaderCircle class="spin" :size="30" />
        <p>正在为你准备座位…</p>
      </main>
      <main v-else-if="bootError" class="loading-page">
        <CircleHelp :size="32" />
        <h2>暂时没能连接俱乐部</h2>
        <p>{{ bootError }}</p>
        <button class="button primary" @click="boot">重新连接</button>
      </main>

      <main v-else-if="!room" class="lobby page-enter">
        <section class="room-section">
          <div class="section-toolbar">
            <div class="section-title">
              <h1>选一桌，入座吧</h1>
              <span>{{ availableCount }} 间可加入</span>
            </div>
            <div class="room-tools">
              <button class="button primary create-room" @click="requestCreate">
                <Plus :size="18" />创建房间
              </button>
              <div class="filter-tabs">
                <button :class="{ selected: filter === 'all' }" @click="filter = 'all'">
                  全部房间</button
                ><button :class="{ selected: filter === 'waiting' }" @click="filter = 'waiting'">
                  等待开局
                </button>
              </div>
              <label class="search-box"
                ><Search :size="16" /><input
                  v-model="search"
                  aria-label="搜索房间"
                  placeholder="搜索房间"
              /></label>
            </div>
          </div>
          <div v-if="filteredRooms.length" class="room-grid">
            <button
              v-for="r in filteredRooms"
              :key="r.id"
              class="room-card"
              :disabled="
                busy || !['waiting', 'finished'].includes(r.phase) || r.availableSeats === 0
              "
              @click="joinRoom(r.id)"
            >
              <div class="room-card-art">
                <RoomArt :theme="r.theme" /><span
                  class="room-state"
                  :class="{ playing: !['waiting', 'finished'].includes(r.phase) }"
                  ><span></span
                  >{{ ['waiting', 'finished'].includes(r.phase) ? '等待入座' : '好戏进行中' }}</span
                ><span class="capacity-pill">{{ r.capacity }} 人局</span>
              </div>
              <div class="room-card-body">
                <div class="room-card-title">
                  <h3><LockKeyhole v-if="r.passwordRequired" :size="16" />{{ r.name }}</h3>
                  <span class="room-enter"><ArrowUpRight :size="20" /></span>
                </div>
                <p>
                  {{
                    r.theme === 'night'
                      ? '天黑之前，谁会先露出破绽？'
                      : r.theme === 'peach'
                        ? '聊点轻松的，藏点小秘密。'
                        : '茶已备好，就等你的一句描述。'
                  }}
                </p>
                <div class="room-card-footer">
                  <div class="avatar-stack">
                    <span v-for="n in Math.min(3, r.capacity)" :key="n" :class="`avatar-color-${n}`">
                      <AccountAvatar
                        v-if="r.players[n - 1] && !r.players[n - 1]!.bot"
                        :src="r.players[n - 1]!.avatar"
                        :name="r.players[n - 1]!.name"
                      />
                      <Bot v-else :size="15" />
                    </span>
                  </div>
                  <span class="occupancy"
                    ><b>{{ r.humanCount }}</b> / {{ r.capacity }} 已入座</span
                  ><span class="ai-fill"><Sparkles :size="12" />{{ r.autoFillAI ? 'AI 可补位' : '等待坐满' }}</span>
                </div>
              </div>
            </button>
          </div>
          <div v-else class="empty-state">
            <Search :size="28" />
            <h3>还没找到这一桌</h3>
            <p>换个关键词，或者创建自己的房间。</p>
            <button
              class="text-button"
              @click="resetRoomSearch"
            >
              查看全部房间<ArrowRight :size="16" />
            </button>
          </div>
        </section>
        <footer class="lobby-footer">
          <span><span class="tiny-spark">✦</span> 真相不止一种说法。</span
          ><span
            >POWERED BY <b>灵矽 AI</b><span class="footer-separator">/</span> QINIU BUILDER
            DAY</span
          >
        </footer>
      </main>

      <main v-else class="room-page page-enter">
        <div class="room-heading">
          <div class="room-heading-left">
            <button class="back-button" aria-label="离开房间" @click="requestLeave">
              <ArrowLeft :size="20" />
            </button>
            <div>
              <div class="breadcrumb">房间大厅 <span>/</span> {{ room.capacity }} 人经典局</div>
              <h1>
                {{ room.name
                }}<span class="phase-pill" :class="room.phase"><span></span>{{ phaseTitle }}</span>
              </h1>
            </div>
          </div>
          <div class="room-heading-right">
            <button v-if="room.isHost" class="button secondary close-room-button" :disabled="busy" @click="openModal('close')">
              <Trash2 :size="15" />关闭房间
            </button>
            <span class="connection-state" :class="{ offline: reconnecting }"
              ><span></span>{{ reconnecting ? '正在重连' : '已连接' }}</span
            ><button class="button secondary invite-button" @click="copyRoom">
              <Copy :size="15" />邀请朋友</button
            ><button
              class="icon-button audio-toggle"
              :aria-label="narration.enabled.value ? '关闭语音播报' : '开启语音播报'"
              :title="narration.enabled.value ? '关闭语音播报' : '开启语音播报'"
              @click="toggleSound"
            >
              <Volume2 v-if="narration.enabled.value" :size="20" /><VolumeX v-else :size="20" />
            </button>
          </div>
        </div>
        <section class="narration-bar" :class="[narration.state.value, { muted: !narration.enabled.value, playing: voice.streaming.value }]" aria-label="房间语音播报">
          <div class="narration-status-icon">
            <LoaderCircle v-if="narration.state.value === 'loading'" class="spin" :size="20" />
            <AudioLines v-else-if="narration.state.value === 'playing' || voice.streaming.value" :size="20" />
            <VolumeX v-else-if="!narration.enabled.value" :size="20" />
            <Volume2 v-else :size="20" />
          </div>
          <div class="narration-copy" role="status">
            <b>{{ voice.streaming.value ? audibleCue?.playerId ? `${audibleCue.seat} 号正在发言` : '灵矽主持正在说话' : voice.recognizing.value ? '灵矽正在理解并处理你的话…' : narrationLabel }}</b>
            <p>{{ audibleCue?.text || (voice.recognizing.value && voice.transcript.value ? `你说：${voice.transcript.value}` : '戴上耳机，听主持报幕、玩家描述和投票结果。') }}</p>
          </div>
          <span class="narration-source">{{ voice.streaming.value ? '灵矽音色 · 实时播放' : narrationSource }}</span>
          <button v-if="narration.state.value === 'blocked' || narration.state.value === 'error' || !narration.enabled.value"
            class="button secondary narration-resume" @click="narration.activate">
            <Volume2 :size="15" />{{ narration.state.value === 'error' ? '重试播报' : '点击开启声音' }}
          </button>
          <button v-else-if="narration.state.value === 'playing' || narration.state.value === 'loading' || voice.recognizing.value"
            class="text-button" @click="stopPlayback">停止播报</button>
          <button v-else class="text-button" :disabled="!room.logs.length" @click="replayLatest">
            <RotateCcw :size="14" />重听上一条
          </button>
        </section>
        <section v-if="room.action?.state === 'waiting_playback'" class="playback-wait-bar" role="status">
          <Headphones :size="19" />
          <div><b>{{ room.action.ready ? '收听已完成，等待开放行动' : '先听完，再开始计时' }}</b><p>前置播报完成后，服务端会开放发言或投票，并开始 60 秒倒计时。</p></div>
          <button v-if="room.action.required && !room.action.ready" class="button secondary" @click="confirmPlayback('unavailable')">改用文字参与</button>
        </section>
        <div class="game-layout">
          <section class="table-panel" :class="room.theme">
            <div class="table-meta">
              <span class="round-label"
                ><Circle :size="11" />{{
                  room.round ? `第 ${String(room.round).padStart(2, '0')} 轮` : '准备开局'
                }}</span
              ><span class="table-format">{{ room.capacity - 1 }} 平民 / 1 卧底</span
              ><span v-if="room.deadline && room.phase !== 'dealing' && !room.agentActive" class="turn-clock"
                ><Clock3 :size="14" />{{ secondsLeft }}s</span
              >
            </div>
            <div class="table-stage" :class="{ 'six-seats': room.capacity === 6 }">
              <div class="table-shadow"></div>
              <div class="round-table">
                <div class="table-stitched"></div>
                <div class="table-engraving">THE UNDERCOVER CLUB<span>KEEP YOUR SECRET.</span></div>
                <div class="felt-shine"></div>
              </div>
              <div class="dealer" :class="{ dealing: room.phase === 'dealing', talking: !!audibleCue && !audibleCue.playerId }">
                <div v-if="audibleCue && !audibleCue.playerId" class="speech-bubble host-bubble"><span><Volume2 :size="12" /> 灵矽主持</span><p>{{ audibleCue.text }}</p></div>
                <span class="dealer-label"><span></span>灵矽 AI · 本桌主持</span
                ><RobotMascot
                  :animated="room.phase === 'waiting' || room.phase === 'dealing' || narration.state.value === 'playing' || voice.recognizing.value"
                />
                <div v-if="audibleCue && !audibleCue.playerId" class="dealer-sound-wave" aria-hidden="true"><i></i><i></i><i></i><i></i><i></i></div>
                <div class="dealer-caption">{{ dealerCaption }}</div>
                <button
                  v-if="!config.voiceConfigured && (room.phase === 'waiting' || room.phase === 'finished')"
                  class="deal-button"
                  :disabled="!room.isHost || !room.canStart || busy"
                  @click="action('start')"
                >
                  <LoaderCircle v-if="busy" class="spin" :size="17" /><RotateCcw
                    v-else-if="room.phase === 'finished'"
                    :size="17"
                  /><Sparkles v-else :size="17" />{{
                    room.isHost
                      ? room.phase === 'finished'
                        ? '再玩一局'
                        : '开始体验'
                      : '等待房主开始'
                  }}<ArrowRight v-if="room.isHost" :size="16" />
                </button>
                <div v-else-if="room.phase === 'dealing'" class="dealing-dots">
                  <i></i><i></i><i></i>
                </div>
                <template v-if="config.voiceConfigured || !['waiting', 'finished', 'dealing'].includes(room.phase)"
                  ><button
                    class="ptt-button"
                    :class="{
                      recording: voice.recording.value,
                      processing: voice.connecting.value || voice.recognizing.value,
                      available: canRecord,
                    }"
                    :disabled="!canRecord || (voice.recognizing.value && !voice.recording.value)"
                    aria-label="按住说话"
                    @pointerdown="startRecording"
                    @pointerup="voice.stop"
                    @pointercancel="voice.cancel"
                    @lostpointercapture="voice.stop"
                    @contextmenu.prevent
                    @keydown.space.prevent="!$event.repeat && startRecording()"
                    @keyup.space.prevent="voice.stop"
                    @keydown.enter.prevent="!$event.repeat && startRecording()"
                    @keyup.enter.prevent="voice.stop"
                  >
                    <LoaderCircle
                      v-if="voice.connecting.value || voice.recognizing.value"
                      class="spin"
                      :size="20"
                    /><AudioLines v-else-if="voice.recording.value" :size="22" /><Mic
                      v-else
                      :size="20"
                    /><span>{{ pttText }}</span></button
                  ><span class="ptt-note">{{ voiceHint }}</span></template
                >
              </div>
              <template v-if="room.phase === 'dealing'">
                <div v-for="seat in seats" :key="`card-${seat.number}`" class="dealt-card" :style="{ '--target-left': seatPosition(seat.number).left, '--target-top': seatPosition(seat.number).top, animationDelay: `${(seat.number - 1) * 0.16}s` }"><span>?</span></div>
              </template>
              <template v-if="!['waiting', 'dealing'].includes(room.phase)">
                <button v-for="seat in seats.filter(s => s.player)" :key="`table-card-${seat.number}`"
                  class="table-card" :class="{ 'face-up': !!tableCardWord(seat.player), 'card-out': !seat.player?.alive, 'your-card': seat.player?.id === room.myPlayerId }"
                  :style="tableCardPosition(seat.number)" :disabled="seat.player?.id !== room.myPlayerId || room.phase === 'finished'"
                  :aria-label="seat.player?.id === room.myPlayerId ? '查看你的桌面词卡' : `${seat.number}号词卡已发放`"
                  @click="peek = !peek">
                  <small>{{ String(seat.number).padStart(2, '0') }}</small>
                  <template v-if="tableCardWord(seat.player)"><strong>{{ tableCardWord(seat.player) }}</strong><em>{{ room.phase === 'finished' ? tableCardRole(seat.player) : '你的词语' }}</em></template>
                  <template v-else><ShieldCheck :size="20" :stroke-width="1.3" /><em>{{ seat.player?.id === room.myPlayerId ? '点击看牌' : '秘密词卡' }}</em></template>
                </button>
              </template>
              <div
                v-for="seat in seats"
                :key="seat.number"
                class="seat"
                :class="[
                  {
                    own: seat.player?.id === room.myPlayerId,
                    speaking: !!speakingPlayerId && seat.player?.id === speakingPlayerId,
                    eliminated: seat.player && !seat.player.alive,
                    selectable:
                      canVote && seat.player && room.legalTargets.includes(seat.player.id),
                    selected: selectedVote === seat.player?.id,
                  },
                  `seat-${seat.number}`,
                ]"
                :style="seatPosition(seat.number)"
              >
                <div v-if="seatBubble(seat.player)" class="speech-bubble" :class="{ 'bubble-left': parseFloat(seatPosition(seat.number).left) < 30, 'bubble-right': parseFloat(seatPosition(seat.number).left) > 70, 'bubble-top': parseFloat(seatPosition(seat.number).top) < 15 }" role="status">
                  <span><AudioLines :size="12" /> {{ seat.number }} 号正在发言</span><p>{{ seatBubble(seat.player) }}</p>
                </div>
                <button
                  class="seat-avatar"
                  :class="[`avatar-color-${seat.number}`, { empty: !seat.player }]"
                  :disabled="
                    !canVote || !seat.player || !room.legalTargets.includes(seat.player.id)
                  "
                  :aria-label="
                    seat.player ? `选择${seat.number}号${seat.player.name}` : `${seat.number}号空位`
                  "
                  @click="chooseVote(seat.player)"
                >
                  <template v-if="seat.player"
                    ><Bot v-if="seat.player.bot" :size="30" :stroke-width="1.6" />
                    <AccountAvatar v-else
                      :src="seat.player.avatar || (seat.player.id === room.myPlayerId ? user?.avatar : '')"
                      :name="seat.player.name"
                    /></template
                  ><Plus v-else :size="25" :stroke-width="1.3" /><span
                    v-if="selectedVote === seat.player?.id"
                    class="selection-tick"
                    ><Check :size="13" /></span
                  ><span v-if="seat.player?.hasVoted && room.phase === 'voting'" class="voted-tick"
                    ><Check :size="11" /></span
                  ><span v-if="speakingPlayerId && seat.player?.id === speakingPlayerId" class="speaking-wave"
                    ><i></i><i></i><i></i
                  ></span>
                </button>
                <div class="seat-name">
                  <span class="seat-number">{{ String(seat.number).padStart(2, '0') }}</span
                  ><span>{{ seat.player?.name || '等待入座' }}</span
                  ><span v-if="seat.player?.id === room.myPlayerId" class="you-tag">你</span
                  ><Crown
                    v-if="seat.player?.id === room.hostPlayerId"
                    :size="12"
                    class="host-crown"
                  />
                </div>
                <span class="seat-caption">{{
                  seat.player
                    ? room.phase === 'waiting' && !seat.player.bot
                      ? seat.player.id === room.hostPlayerId ? '房主 · 等待开局' : seat.player.ready ? '已准备' : '待准备'
                      : !seat.player.alive
                      ? '已出局 · 观战中'
                      : seat.player.id === speakingPlayerId
                        ? '正在发言'
                        : seat.player.id === room.currentPlayerId
                          ? '等待发言'
                        : seat.player.hasVoted && room.phase === 'voting'
                          ? '已投票'
                          : seat.player.bot
                            ? 'AI 玩家'
                            : '已入座'
                    : room.autoFillAI ? '空位将由 AI 补齐' : '等待朋友入座'
                }}</span>
              </div>
            </div>
            <div class="table-bottom">
              <div class="table-hint">
                <span class="hint-icon"><Sparkles :size="16" /></span>
                <div>
                  <b>{{ phaseTitle }}</b>
                  <p>{{ phaseHint }}</p>
                </div>
              </div>
              <span v-if="room.phase !== 'waiting'" class="survivor-count"
                ><Users :size="15" />{{ room.players.filter((p) => p.alive).length }} /
                {{ room.capacity }} 在场</span
              >
            </div>
            <div class="phase-progress"><div :style="{ width: `${turnProgress}%` }"></div></div>
          </section>
          <aside class="game-sidebar">
            <LobbyControls v-if="['waiting', 'finished'].includes(room.phase)" :room="room" :busy="busy" :voice-configured="config.voiceConfigured"
              @ready="action('ready', { ready: $event })"
              @auto-fill="action('settings', { autoFillAI: $event })"
              @ai-seat="action('ai-seat', $event)" @remove="requestRemove" />
            <section
              v-if="room.phase !== 'waiting' && room.phase !== 'dealing'"
              class="word-card"
              :class="{ revealed: peek }"
            >
              <div class="word-card-top">
                <span><span class="tiny-spark">✦</span> 你的秘密词卡</span
                ><ShieldCheck :size="16" />
              </div>
              <button class="word-reveal" @click="peek = !peek">
                <span>{{ peek ? room.myWord : '••••' }}</span
                ><EyeOff v-if="peek" :size="18" /><Eye v-else :size="18" />
              </button>
              <p>{{ peek ? '用自己的话描述它，别直接说出来。' : '点击查看，只有你能看见。' }}</p>
            </section>
            <section v-else class="welcome-card">
              <div class="welcome-icon"><Headphones :size="23" /></div>
              <h3>把声音带上桌</h3>
              <p>拿好你的词，听听大家怎么说。<br />相似的话里，藏着不同的身份。</p>
              <span><ShieldCheck :size="13" /> 词卡仅自己可见</span>
            </section>
            <section class="conversation-panel">
              <header>
                <h2><MessageCircle :size="16" />本桌发言</h2>
                <span v-if="room.logs.length"
                  >{{ visibleLogs.filter((l) => l.kind === 'speech').length }} 条描述</span
                ><span v-else>好戏待启</span>
              </header>
              <div ref="transcript" class="transcript" aria-live="polite" aria-relevant="additions">
                <div v-if="!room.logs.length" class="conversation-empty">
                  <RobotMascot small /><b>欢迎来到这一桌</b>
                  <p>游戏开始后，大家的发言<br />都会留在这里。</p>
                </div>
                <div
                  v-for="log in visibleLogs"
                  :key="log.id"
                  class="log-entry"
                  :class="[log.kind, { 'log-playing': audibleCue?.logId === log.id }]"
                >
                  <template v-if="log.kind === 'speech'"
                    ><div class="log-author">
                      <span :class="`avatar-color-${log.seat}`">{{
                        String(log.seat).padStart(2, '0')
                      }}</span
                      ><b>{{ log.name }}</b
                      ><span v-if="log.playerId === room.myPlayerId" class="log-you">你</span
                      ><span class="log-round">第 {{ log.round }} 轮</span>
                      <button class="log-replay" :aria-label="`重听${log.seat}号${log.name}的发言`" title="重听发言" @click="replayLog(log)">
                        <AudioLines v-if="narration.activeLog.value === log.id && narration.state.value === 'playing'" :size="14" /><Volume2 v-else :size="14" />
                      </button>
                    </div>
                    <p>{{ log.text }}</p></template
                  ><template v-else
                    ><span class="host-log-icon"
                      ><Bot v-if="log.kind === 'host'" :size="14" /><CheckCheck v-else :size="14"
                    /></span>
                    <p><b v-if="log.kind === 'host'">灵矽 AI</b>{{ publicLogText(log) }}</p>
                    <button class="log-replay" :aria-label="log.kind === 'host' ? '重听主持播报' : '重听投票结果'" title="重听播报" @click="replayLog(log)">
                      <AudioLines v-if="narration.activeLog.value === log.id && narration.state.value === 'playing'" :size="14" /><Volume2 v-else :size="14" />
                    </button></template
                  >
                </div>
              </div>
              <div v-if="myTurn" class="composer">
                <label class="composer-label" for="speech-draft"
                  >{{ room.myDraft && !room.agentActive ? '已识别，待提交' : '轮到你发言了' }} <span>{{ room.myDraft && !room.agentActive ? '原文已保留，确认后继续。' : '一句话，也有大学问。' }}</span></label
                ><textarea
                  id="speech-draft"
                  v-model="draft"
                  :disabled="room.agentActive"
                  maxlength="160"
                  placeholder="也可以在这里输入你的描述…"
                  @keydown.ctrl.enter.prevent="sendSpeech"
                  @keydown.meta.enter.prevent="sendSpeech"
                ></textarea>
                <div class="composer-footer">
                  <button class="text-button skip-button" :disabled="busy || room.agentActive" @click="action('skip')">
                    跳过本轮</button
                  ><button
                    class="send-button"
                    :disabled="!draft.trim() || busy || voice.recording.value || room.agentActive"
                    @click="sendSpeech"
                  >
                    确认发言<Send :size="14" />
                  </button>
                </div>
              </div>
              <div v-else-if="canVote" class="vote-composer">
                <b>把这一票投给谁？</b>
                <div class="vote-options">
                  <button
                    v-for="p in room.players.filter((p) => room!.legalTargets.includes(p.id))"
                    :key="p.id"
                    :class="{ selected: selectedVote === p.id }"
                    @click="chooseVote(p)"
                  >
                    {{ p.seat }} 号<span>{{ p.name }}</span
                    ><Check v-if="selectedVote === p.id" :size="13" />
                  </button>
                </div>
                <button
                  class="button primary vote-confirm"
                  :disabled="!selectedVote || busy"
                  @click="action('vote', { targetId: selectedVote })"
                >
                  {{ selectedPlayer ? `确认投给 ${selectedPlayer.seat} 号` : '先选择一位玩家'
                  }}<Check :size="16" /></button
                ><button
                  class="text-button abstain"
                  :disabled="busy"
                  @click="action('vote', { targetId: '' })"
                >
                  这一轮选择弃权
                </button>
              </div>
              <div v-else class="sidebar-status">
                <LoaderCircle
                  v-if="
                    room.phase === 'dealing' ||
                    room.phase === 'speaking' ||
                    room.phase === 'tiebreak'
                  "
                  :size="15"
                  class="spin"
                /><CheckCheck v-else-if="room.phase === 'voting'" :size="16" /><Coffee
                  v-else
                  :size="16"
                /><span>{{ phaseHint }}</span>
              </div>
            </section>
          </aside>
        </div>
        <section v-if="room.phase === 'finished'" class="result-panel">
          <div class="result-title">
            <span class="result-icon"><Trophy :size="25" /></span>
            <div>
              <span class="eyebrow">THE SECRET IS OUT</span>
              <h2>{{ room.winner === 'civilian' ? '平民阵营获胜' : '卧底阵营获胜' }}</h2>
            </div>
            <p v-if="config.voiceConfigured" class="result-voice-hint">
              <Mic :size="16" />{{ room.isHost ? '按住圆桌中央按钮，说“再来一局”' : '等待房主说“再来一局”，继续同桌游戏' }}
            </p>
          </div>
          <div class="reveal-grid">
            <div
              v-for="reveal in room.reveals"
              :key="reveal.playerId"
              class="reveal-card"
              :class="{ undercover: reveal.role === 'undercover' }"
            >
              <span>{{ reveal.seat }} 号 · {{ reveal.name }}</span
              ><b>{{ reveal.word }}</b
              ><span class="role-tag">{{ reveal.role === 'undercover' ? '卧底' : '平民' }}</span>
            </div>
          </div>
        </section>
        <footer class="room-footer">
          <span><ShieldCheck :size="13" /> 词卡独立保管 · 投票统一揭晓</span
          ><button class="text-button" @click="requestLeave">
            <DoorOpen :size="14" />离开这一桌
          </button>
        </footer>
      </main>
    </div>

    <Transition name="toast"
      ><div v-if="toast" class="toast-message" role="status">
        <Check :size="17" />{{ toast }}
      </div></Transition
    >
    <div v-if="modal" class="modal-backdrop" @click.self="modal = null">
      <section
        class="modal"
        role="dialog"
        aria-modal="true"
        :aria-label="{
          login: '登录入座', create: '创建房间', rules: '游戏规则',
          leave: '离开房间', close: '关闭房间', join: '输入房间口令', remove: '移除成员',
        }[modal]"
      >
        <button class="modal-close icon-button" aria-label="关闭" @click="modal = null">
          <X :size="20" />
        </button>
        <template v-if="modal === 'login'"
          ><RobotMascot class="modal-robot" />
          <div class="eyebrow">WELCOME TO THE CLUB</div>
          <h2>留个名字，入座吧。</h2>
          <p class="modal-description">你的下一位朋友，可能就是卧底。</p>
          <form v-if="config.allowGuest" @submit.prevent="guestLogin">
            <label class="field-label" for="nickname">怎么称呼你</label
            ><input
              id="nickname"
              v-model="nickname"
              class="form-input"
              maxlength="20"
              autocomplete="nickname"
              placeholder="给自己起个名字"
              required
            />
            <p v-if="modalError" class="form-error">{{ modalError }}</p>
            <button class="button primary modal-submit" :disabled="busy || !nickname.trim()">
              <LoaderCircle v-if="busy" class="spin" :size="17" /><template v-else
                >游客入座<ArrowRight :size="17"
              /></template>
            </button>
          </form>
          <div v-if="config.allowGuest && config.authEnabled" class="or-divider">或</div>
          <button
            v-if="config.authEnabled"
            class="button account-login"
            :disabled="busy"
            @click="accountLogin"
          >
            使用 XBuilder 账号登录<ArrowUpRight :size="16" />
          </button>
          <p v-if="!config.allowGuest && !config.authEnabled" class="form-error">
            当前登录暂未开放，请联系部署者。
          </p>
          <span class="modal-footnote"
            ><ShieldCheck :size="12" /> 私密词卡只对本人展示</span
          ></template
        >
        <template v-else-if="modal === 'join'">
          <div class="modal-symbol"><LockKeyhole :size="26" /></div>
          <h2>这一桌，需要口令。</h2>
          <p class="modal-description">向房主获取口令，和朋友一起入座。</p>
          <form @submit.prevent="submitRoomPassword">
            <label class="field-label" for="join-password">房间口令</label>
            <input id="join-password" v-model="joinPassword" class="form-input" type="password"
              minlength="4" maxlength="32" autocomplete="off" placeholder="请输入房间口令" required />
            <p v-if="modalError" class="form-error" role="alert">{{ modalError }}</p>
            <button class="button primary modal-submit" :disabled="busy || !joinPassword">
              <LoaderCircle v-if="busy" class="spin" :size="17" />验证并入座<ArrowRight :size="17" />
            </button>
          </form>
        </template>
        <template v-else-if="modal === 'remove'">
          <div class="modal-symbol"><Users :size="26" /></div>
          <h2>移除{{ removingPlayer?.name }}？</h2>
          <p class="modal-description">该成员将返回大厅，其余同桌需要重新准备。对方仍可重新加入。</p>
          <div class="modal-actions">
            <button class="button secondary" :disabled="busy" @click="modal = null">取消</button>
            <button class="button danger" :disabled="busy" @click="removeMember">确认移除</button>
          </div>
        </template>
        <template v-else-if="modal === 'create'"
          ><div class="modal-symbol"><Plus :size="26" /></div>
          <div class="eyebrow">MAKE SOME ROOM</div>
          <h2>这一桌，由你做主。</h2>
          <p class="modal-description">叫上朋友，剩下的位置交给 AI。</p>
          <form @submit.prevent="createRoom">
            <label class="field-label" for="room-name">房间名称</label
            ><input
              id="room-name"
              v-model="roomName"
              class="form-input"
              maxlength="24"
              placeholder="给这一桌起个名字"
              required
            /><span class="field-label">选择人数</span>
            <div class="capacity-select">
              <button
                v-for="n in [4, 6]"
                :key="n"
                type="button"
                :class="{ selected: roomCapacity === n }"
                @click="roomCapacity = n"
              >
                <Users :size="20" /><b>{{ n }} 人经典局</b
                ><span>{{ n === 4 ? '轻松开场，快速过招' : '更多线索，更多悬念' }}</span
                ><Check v-if="roomCapacity === n" :size="15" />
              </button>
            </div>
            <span class="field-label">选个氛围</span>
            <div class="theme-select">
              <button
                v-for="(name, theme) in { jade: '森林绿', peach: '落日橘', night: '午夜蓝' }"
                :key="theme"
                type="button"
                :class="[theme, { selected: roomTheme === theme }]"
                @click="roomTheme = theme"
              >
                <span></span>{{ name }}<Check v-if="roomTheme === theme" :size="14" />
              </button>
            </div>
            <label class="room-option"><span><LockKeyhole :size="16" />使用房间口令</span><input v-model="protectedRoom" type="checkbox" /></label>
            <template v-if="protectedRoom">
              <label class="field-label" for="room-password">设置口令</label>
              <input id="room-password" v-model="roomPassword" class="form-input" type="password"
                minlength="4" maxlength="32" autocomplete="new-password" placeholder="4 至 32 字，分享链接后另行告知朋友" required />
            </template>
            <label class="room-option"><span><Bot :size="16" />空位自动由 AI 补齐</span><input v-model="autoFillAI" type="checkbox" /></label>
            <p v-if="modalError" class="form-error">{{ modalError }}</p>
            <button class="button primary modal-submit" :disabled="busy || !roomName.trim()">
              创建并入座<ArrowRight :size="17" />
            </button></form
        ></template>
        <template v-else-if="modal === 'rules'"
          ><div class="modal-symbol"><CircleHelp :size="26" /></div>
          <div class="eyebrow">HOW TO PLAY</div>
          <h2>相似的词，不同的心思。</h2>
          <p class="modal-description">每局只有一位卧底，其他人都是平民。</p>
          <ol class="rules-list">
            <li>
              <span>01</span>
              <div>
                <b>收好你的秘密词卡</b>
                <p>平民的词相同，卧底的词略有不同。你只知道自己的词，不知道自己的阵营。</p>
              </div>
            </li>
            <li>
              <span>02</span>
              <div>
                <b>轮流用一句话描述</b>
                <p>按座位顺序发言。可以说用途、特点或场景，但不能直接说出自己的词。</p>
              </div>
            </li>
            <li>
              <span>03</span>
              <div>
                <b>投出最可疑的一位</b>
                <p>所有存活玩家投票，最高票者出局。平票玩家补充发言后加投，仍平票则进入下一轮。</p>
              </div>
            </li>
            <li>
              <span>04</span>
              <div>
                <b>让真相浮出水面</b>
                <p>
                  找出卧底，平民获胜；卧底存活到最后两人，卧底获胜。超时发言会跳过，超时投票记为弃权。
                </p>
              </div>
            </li>
          </ol>
          <button class="button primary modal-submit" @click="modal = null">
            懂了，好戏开场<ArrowRight :size="17" /></button
        ></template>
        <template v-else-if="modal === 'close'">
          <div class="modal-symbol close-room-symbol"><Trash2 :size="26" /></div>
          <h2>关闭这一桌？</h2>
          <p class="modal-description">关闭后，房间和本局记录会删除，所有玩家将返回大厅。</p>
          <p v-if="modalError" class="form-error" role="alert">{{ modalError }}</p>
          <div class="modal-actions">
            <button class="button secondary" :disabled="busy" @click="modal = null">继续玩</button>
            <button class="button danger" :disabled="busy" @click="closeRoom">
              <LoaderCircle v-if="busy" class="spin" :size="16" /><Trash2 v-else :size="16" />确认关闭
            </button>
          </div>
        </template>
        <template v-else-if="modal === 'leave'"
          ><div class="modal-symbol"><DoorOpen :size="26" /></div>
          <h2>要先离席了吗？</h2>
          <p class="modal-description">
            这局还在进行中。离开后，你的座位会由 AI 接续，其他朋友可以继续玩。
          </p>
          <div class="modal-actions">
            <button class="button secondary" @click="modal = null">再坐一会儿</button
            ><button class="button primary" :disabled="busy" @click="leaveRoom">
              离开房间<ArrowRight :size="16" />
            </button></div
        ></template>
      </section>
    </div>
  </div>
</template>
