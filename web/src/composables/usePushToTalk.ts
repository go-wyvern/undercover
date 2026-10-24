import { ref } from 'vue'
import type { AudioCue } from '../types'

export function usePushToTalk(
  onResult: (text: string, pcm: Uint8Array) => void,
  onError: (text: string) => void,
  onHeard: (id: string) => void = () => {},
  canPlay: () => boolean = () => true,
) {
  const recording = ref(false)
  const connecting = ref(false)
  const recognizing = ref(false)
  const seconds = ref(0)
  const assistant = ref(false)
  const transcript = ref('')
  const streaming = ref(false)
  const replyText = ref('')
  const activeCue = ref<AudioCue | null>(null)
  const completedReply = ref('')
  let playbackCue: AudioCue | null = null
  let pendingCue: AudioCue | null = null
  let scheduledCues: { at: number; cue: AudioCue }[] = []
  let playbackStart = 0
  let playbackTimer: ReturnType<typeof setInterval> | undefined
  function syncPlayback() {
    if (!context) return
    const time = context.currentTime
    streaming.value = sources.size > 0 && time >= playbackStart && time < playbackAt
    while (scheduledCues.length && scheduledCues[0]!.at <= time) {
      playbackCue = scheduledCues.shift()!.cue
      replyText.value = playbackCue.text
    }
    activeCue.value = streaming.value ? playbackCue : null
  }
  let playbackAt = 0
  let outputRate = 16000
  let sources = new Set<AudioBufferSourceNode>()
  let completePlayback: (() => void) | null = null
  let pressed = false
  let socket: WebSocket | null = null
  let stream: MediaStream | null = null
  let context: AudioContext | null = null
  let recorder: AudioWorkletNode | null = null
  let timer: ReturnType<typeof setInterval> | undefined
  let frames: Uint8Array[] = []
  let flushed: (() => void) | null = null
  let generation = 0
  let upstreamReady = false
  let inputFinished = false
  let stopping = false
  let stopSent = false
  let sentFrames = 0
  let connectionTimer: ReturnType<typeof setTimeout> | undefined

  function sendPendingInput() {
    if (!upstreamReady || socket?.readyState !== WebSocket.OPEN) return
    while (sentFrames < frames.length) socket.send(frames[sentFrames++]!)
    if (inputFinished && !stopSent) {
      socket.send(JSON.stringify({ type: 'stop' }))
      stopSent = true
    }
  }

  function stopTracks(keepContext = false) {
    stream?.getTracks().forEach((track) => track.stop())
    stream = null
    if (context && !keepContext) void context.close().catch(() => {})
    if (!keepContext) context = null
    recorder = null
    clearInterval(timer)
    recording.value = false
  }
  function muteOutput() {
    for (const source of sources) {try {source.stop()} catch { /* Already ended. */ }}
    sources.clear()
    playbackAt = 0
    streaming.value = false
    activeCue.value = null
    playbackCue = null
    pendingCue = null
    scheduledCues = []
    clearInterval(playbackTimer)
    playbackTimer = undefined
  }
  function cancel() {
    generation++
    pressed = false
    muteOutput()
    completePlayback?.()
    completePlayback = null
    stopTracks()
    socket?.close()
    socket = null
    connecting.value = false
    recognizing.value = false
    flushed?.()
    flushed = null
    frames = []
    upstreamReady = false
    inputFinished = false
    stopping = false
    stopSent = false
    sentFrames = 0
    clearTimeout(connectionTimer)
  }
  async function start(roomId: string, fullConversation = false) {
    if (pressed || connecting.value || recognizing.value || recording.value) return
    assistant.value = fullConversation
    transcript.value = ''
    replyText.value = ''
    pressed = true
    connecting.value = true
    const run = ++generation
    frames = []
    seconds.value = 0
    try {
      if (!navigator.mediaDevices?.getUserMedia)
        throw new Error('当前环境无法使用麦克风，请通过 HTTPS 或 localhost 打开')
      const localStream = await navigator.mediaDevices.getUserMedia({
        audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true },
      })
      if (run !== generation || !pressed) {
        localStream.getTracks().forEach((track) => track.stop())
        return
      }
      stream = localStream
      const localContext = new AudioContext()
      context = localContext
      const ws = new WebSocket(
        `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/rooms/${encodeURIComponent(roomId)}/${fullConversation ? 'assistant/voice' : 'voice'}`,
      )
      socket = ws
      ws.binaryType = 'arraybuffer'
      connectionTimer = setTimeout(() => {
        if (run !== generation || upstreamReady) return
        cancel()
        onError('灵矽连接超时，本次录音未提交，请重试')
      }, 25000)
      let received = false
      ws.onmessage = async ({ data }) => {
        if (run !== generation) return
        if (data instanceof ArrayBuffer) {
          if (!fullConversation || !canPlay() || !context || !data.byteLength || data.byteLength % 2) return
          const buffer = context.createBuffer(1, data.byteLength / 2, outputRate)
          const channel = buffer.getChannelData(0)
          const view = new DataView(data)
          for (let i=0; i<channel.length; i++) channel[i] = view.getInt16(i*2, true) / 32768
          const node = context.createBufferSource()
          node.buffer = buffer
          node.connect(context.destination)
          sources.add(node)
          node.onended = () => {sources.delete(node); node.disconnect()}
          playbackAt = Math.max(playbackAt, context.currentTime + 0.08)
          if (sources.size === 1) playbackStart = playbackAt
          if (pendingCue) {
            scheduledCues.push({ at: playbackAt, cue: pendingCue })
            pendingCue = null
          }
          node.start(playbackAt)
          playbackAt += buffer.duration
          playbackTimer ??= setInterval(syncPlayback, 25)
          syncPlayback()
          return
        }
        let message: { type: string; text?: string; message?: string; sampleRate?: string; id?: string; streamed?: string; logId?: string; playerId?: string; seat?: string; offsetMs?: string }
        try {
          message = JSON.parse(String(data))
        } catch {
          return
        }
        if (message.type === 'ready') {
          outputRate = Number(message.sampleRate) || 16000
          upstreamReady = true
          connecting.value = false
          clearTimeout(connectionTimer)
          sendPendingInput()
        } else if (message.type === 'transcript' && fullConversation) {
          transcript.value = message.text || ''
          recognizing.value = true
        } else if (message.type === 'assistant_sentence') {
          pendingCue = {text: message.text || '', offsetMs: Number(message.offsetMs) || 0, logId: message.logId, playerId: message.playerId, seat: Number(message.seat) || undefined}
        } else if (message.type === 'assistant_reply_id') {
          if (message.id) onHeard(message.id)
        } else if (message.type === 'reset_audio') {
          muteOutput()
          playbackTimer = undefined
        } else if (message.type === 'assistant_done') {
          received = true
          if (message.streamed === 'true' && message.id) onHeard(message.id)
          if (context && playbackAt > context.currentTime) {
            await new Promise<void>((resolve) => {
              const timer = setTimeout(resolve, Math.max(0, playbackAt - context!.currentTime) * 1000 + 100)
              completePlayback = () => {clearTimeout(timer); resolve()}
            })
          }
          if (run === generation) {completedReply.value = message.id || String(Date.now()); cancel()}
        } else if (message.type === 'transcript') {
          received = true
          const pcm = new Uint8Array(frames.reduce((total, frame) => total + frame.length, 0))
          let offset = 0
          for (const frame of frames) {
            pcm.set(frame, offset)
            offset += frame.length
          }
          const text = message.text || ''
          cancel()
          if (text.trim()) onResult(text, pcm)
        } else if (message.type === 'error') {
          received = true
          cancel()
          onError(message.message || '语音识别失败，请重试')
        }
      }
      ws.onerror = () => {
        if (run === generation) {
          cancel()
          onError('语音连接失败，请稍后重试或使用文字发言')
        }
      }
      ws.onclose = () => {
        if (!received && run === generation) {
          cancel()
          onError('语音连接已断开，请重试')
        }
      }
      // Capture immediately while the upstream OTA / WebSocket handshake runs.
      // Releasing before ready keeps the recorded frames and queues the stop.
      await localContext.audioWorklet.addModule('/pcm-worklet.js')
      if (run !== generation) return
      if (!pressed) {cancel(); return}
      await localContext.resume()
      if (run !== generation) return
      if (!pressed) {cancel(); return}
      const node = new AudioWorkletNode(localContext, 'pcm-recorder')
      recorder = node
      node.port.onmessage = ({ data }) => {
        if (run !== generation) return
        if (data.type === 'flushed') {
          flushed?.()
          flushed = null
          return
        }
        if (data.type !== 'pcm' || inputFinished) return
        frames.push(new Uint8Array(data.buffer))
        sendPendingInput()
      }
      const source = localContext.createMediaStreamSource(localStream)
      const silent = localContext.createGain()
      silent.gain.value = 0
      source.connect(node).connect(silent).connect(localContext.destination)
      recording.value = true
      timer = setInterval(() => {
        seconds.value++
        if (seconds.value >= 30) void stop()
      }, 1000)
    } catch (error) {
      if (run !== generation) return
      cancel()
      if (error instanceof DOMException && error.name === 'NotAllowedError')
        onError('麦克风权限未开启，你也可以使用文字发言')
      else onError(error instanceof Error ? error.message : '无法开启麦克风')
    }
  }
  async function stop() {
    pressed = false
    if (!recording.value) {
      if (connecting.value) cancel()
      return
    }
    if (stopping || inputFinished) return
    stopping = true
    recognizing.value = true
    const run = generation
    if (recorder) {
      const node = recorder
      await new Promise<void>((resolve) => {
        const timeout = setTimeout(() => {
          flushed = null
          resolve()
        }, 300)
        flushed = () => {
          clearTimeout(timeout)
          resolve()
        }
        node.port.postMessage('flush')
      })
    }
    if (run !== generation) return
    stopTracks(true)
    inputFinished = true
    sendPendingInput()
  }
  return { recording, connecting, recognizing, seconds, assistant, transcript, streaming, replyText, activeCue, completedReply, start, stop, cancel, muteOutput }
}
