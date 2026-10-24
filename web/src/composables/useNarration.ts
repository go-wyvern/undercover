import { ref } from 'vue'
import { cueAt, logCue } from './audioCues.ts'
import type { AudioCue, GameLog } from '../types'

type Item = { room: string; log: GameLog; linx: boolean; blob?: Blob }
type State = 'idle' | 'loading' | 'playing' | 'blocked' | 'error'

export function spokenText(log: GameLog) {
  return log.kind === 'speech' && !log.audio
    ? `${log.seat}号：${log.text}`
    : log.text
}

export function useNarration(onError: (text: string) => void) {
  let preference = true
  try { preference = localStorage.getItem('undercover:narration') !== 'off' } catch { /* Storage may be disabled. */ }
  const enabled = ref(preference)
  const activeLog = ref('')
  const activeCue = ref<AudioCue | null>(null)
  const completedLog = ref<GameLog | null>(null)
  const current = ref<GameLog | null>(null)
  const state = ref<State>('idle')
  const playbackSource = ref<'linx' | 'recording' | 'browser' | ''>('')
  const fallback = ref(false)
  const queuedCount = ref(0)
  let queue: Item[] = []
  let busy = false
  let priming = false
  let generation = 0
  let audio: HTMLAudioElement | null = null
  let controller: AbortController | null = null
  let objectURL = ''
  let release: (() => void) | null = null
  let seen = new Set<string>()

  function remember() {
    try { localStorage.setItem('undercover:narration', enabled.value ? 'on' : 'off') } catch { /* Optional preference. */ }
  }
  function clearMedia() {
    if (audio) {
      audio.ontimeupdate = null
      audio.onended = null
      audio.onerror = null
      audio.pause()
      audio.removeAttribute('src')
    }
    if (objectURL) URL.revokeObjectURL(objectURL)
    objectURL = ''
    if ('speechSynthesis' in window) window.speechSynthesis.cancel()
    release?.()
    release = null
  }
  function cancel() {
    generation++
    queue = []
    seen.clear()
    queuedCount.value = 0
    controller?.abort()
    controller = null
    clearMedia()
    activeLog.value = ''
    activeCue.value = null
    current.value = null
    state.value = 'idle'
    playbackSource.value = ''
    busy = false
    priming = false
  }

  // Reuse one media element, and prime it synchronously inside a real click.
  // If autoplay is still denied, keep the item for an explicit user retry.
  function activate() {
    enabled.value = true
    remember()
    if (busy || priming) return
    const run = generation
    priming = true
    state.value = 'idle'
    audio ??= new Audio()
    const bytes = new Uint8Array(204)
    const view = new DataView(bytes.buffer)
    bytes.set(new TextEncoder().encode('RIFF'), 0)
    view.setUint32(4, bytes.length - 8, true)
    bytes.set(new TextEncoder().encode('WAVEfmt '), 8)
    view.setUint32(16, 16, true)
    view.setUint16(20, 1, true)
    view.setUint16(22, 1, true)
    view.setUint32(24, 8000, true)
    view.setUint32(28, 16000, true)
    view.setUint16(32, 2, true)
    view.setUint16(34, 16, true)
    bytes.set(new TextEncoder().encode('data'), 36)
    view.setUint32(40, bytes.length - 44, true)
    const url = URL.createObjectURL(new Blob([bytes], { type: 'audio/wav' }))
    audio.src = url
    void audio.play().catch(() => {
      // The actual utterance will report whether user activation is required.
    }).finally(() => {
      URL.revokeObjectURL(url)
      if (run !== generation) return
      audio?.pause()
      priming = false
      void playNext()
    })
  }
  function toggle() {
    if (enabled.value) {
      enabled.value = false
      remember()
      cancel()
    } else activate()
  }
  function isBlocked(error: unknown) {
    return error instanceof Error && (error.name === 'NotAllowedError' || error.message === 'not-allowed')
  }
  async function loadAudio(item: Item, signal: AbortSignal) {
    for (let attempt = 0; attempt < 3; attempt++) {
      const response = await fetch(
        `/api/rooms/${encodeURIComponent(item.room)}/audio/${encodeURIComponent(item.log.id)}`,
        { signal, cache: 'no-store' },
      )
      if (response.status === 503 && attempt < 2) {
        // A software terminal can be occupied briefly by another room or microphone.
        await new Promise<void>((resolve) => setTimeout(resolve, 500 * (attempt + 1)))
        signal.throwIfAborted()
        continue
      }
      if (!response.ok) throw new Error('语音服务暂时不可用')
      return response.blob()
    }
    throw new Error('语音服务繁忙')
  }
  async function playBlob(blob: Blob, log: GameLog, run: number) {
    objectURL = URL.createObjectURL(blob)
    audio ??= new Audio()
    audio.src = objectURL
    const sync = () => {
      if (run === generation && state.value === 'playing')
        activeCue.value = log.audioCues?.length ? cueAt(log.audioCues, (audio!.currentTime || 0) * 1000) : logCue(log)
    }
    audio.ontimeupdate = sync
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('语音播放超时')), 120000)
      const finish = (error?: Error) => {
        clearTimeout(timer)
        if (error) reject(error)
        else resolve()
      }
      release = () => finish()
      audio!.onended = () => finish()
      audio!.onerror = () => finish(new Error('浏览器未能播放这段语音'))
      void audio!.play().then(() => {
        if (run === generation) {state.value = 'playing'; sync()}
      }).catch((error: Error) => finish(error))
    })
  }
  async function speakInBrowser(log: GameLog, run: number) {
    if (!('speechSynthesis' in window)) throw new Error('浏览器不支持语音播报')
    const text = spokenText(log)
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('浏览器语音未能完成，请重试')), Math.min(120000, Math.max(12000, text.length * 500 + 5000)))
      const finish = (error?: Error) => {
        clearTimeout(timer)
        if (error) reject(error)
        else resolve()
      }
      release = () => finish()
      const utterance = new SpeechSynthesisUtterance(text)
      utterance.lang = 'zh-CN'
      utterance.rate = 1
      const voice = window.speechSynthesis.getVoices().find((v) => /^zh[-_]/i.test(v.lang))
      if (voice) utterance.voice = voice
      utterance.onstart = () => { if (run === generation) {state.value = 'playing'; activeCue.value = logCue(log)} }
      utterance.onend = () => finish()
      utterance.onerror = (event) => finish(new Error(event.error))
      window.speechSynthesis.speak(utterance)
    })
  }
  async function playNext() {
    if (busy || priming || !enabled.value || !queue.length || state.value === 'blocked' || state.value === 'error') return
    busy = true
    const run = generation
    const next = queue[0]!
    activeLog.value = next.log.id
    current.value = next.log
    state.value = 'loading'
    let completed = false
    try {
      if (next.linx || next.log.audio) {
        playbackSource.value = next.log.audio ? 'recording' : 'linx'
        controller = new AbortController()
        const timeout = setTimeout(() => controller?.abort(), 40000)
        try {
          next.blob ??= await loadAudio(next, controller.signal)
          clearTimeout(timeout)
          if (run !== generation) return
          await playBlob(next.blob, next.log, run)
          fallback.value = false
        } catch (error) {
          if (run !== generation || isBlocked(error)) throw error
          clearMedia()
          fallback.value = true
          playbackSource.value = 'browser'
          onError('灵矽播报暂不可用，已尝试切换浏览器语音')
          await speakInBrowser(next.log, run)
        } finally {
          clearTimeout(timeout)
        }
      } else {
        playbackSource.value = 'browser'
        await speakInBrowser(next.log, run)
      }
      completed = true
    } catch (error) {
      if (run !== generation) return
      state.value = isBlocked(error) ? 'blocked' : 'error'
      if (state.value === 'error') onError('语音未能播放，请点击“重试播报”或重听这条发言')
    } finally {
      if (run === generation) {
        clearMedia()
        activeCue.value = null
        controller = null
        busy = false
        if (completed) {
          completedLog.value = next.log
          queue.shift()
          queuedCount.value = queue.length
          activeLog.value = ''
          current.value = null
          state.value = 'idle'
          void playNext()
        }
      }
    }
  }
  function enqueue(room: string, logs: GameLog[], linx: boolean) {
    if (!enabled.value) return
    for (const log of logs) {
      const key = `${room}/${log.id}`
      if (seen.has(key)) continue
      seen.add(key)
      queue.push({ room, log, linx })
    }
    queuedCount.value = queue.length
    void playNext()
  }
  function replay(room: string, log: GameLog, linx: boolean) {
    cancel()
    queue.push({ room, log, linx })
    queuedCount.value = 1
    activate()
  }
  return { enabled, activeLog, activeCue, completedLog, current, state, playbackSource, fallback, queuedCount, activate, toggle, cancel, enqueue, replay }
}
