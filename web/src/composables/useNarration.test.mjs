import { afterEach, beforeEach, test } from 'node:test'
import assert from 'node:assert/strict'
import { narrationEntries, useNarration } from './useNarration.ts'

const log = (id, text = `描述${id}`) => ({ id, kind: 'host', text, round: 1, at: 1 })
let media, spoken, fetches, blocked, failed, preferences
class FakeAudio {
  src = ''
  currentTime = 0
  ontimeupdate = null
  onended = null
  onerror = null
  async play() {
    // Activation uses a short silent WAV before the actual Ogg clip.
    if (this.src.startsWith('blob:') && blobs.get(this.src)?.type === 'audio/wav') return
    if (blocked) throw new DOMException('Click required', 'NotAllowedError')
    media.push(this)
  }
  pause() {}
  removeAttribute() { this.src = '' }
}
const blobs = new Map()
const oldCreate = URL.createObjectURL
const oldRevoke = URL.revokeObjectURL
let instance
async function until(predicate) {
  for (let i=0; i<100; i++) {
    if (predicate()) return
    await new Promise(resolve => setTimeout(resolve, 2))
  }
  assert.fail('Expected narration state did not arrive')
}
beforeEach(() => {
  media = []; spoken = []; fetches = []; blocked = false; failed = false; preferences = new Map()
  globalThis.Audio = FakeAudio
  globalThis.SpeechSynthesisUtterance = class { constructor(text) { this.text = text } }
  globalThis.localStorage = { getItem: key => preferences.get(key) ?? null, setItem: (key, value) => preferences.set(key, value) }
  globalThis.window = { speechSynthesis: {
    cancel() {}, getVoices: () => [{ lang: 'zh-CN' }],
    speak(utterance) { spoken.push(utterance); utterance.onstart?.() },
  } }
  globalThis.fetch = async path => {
    fetches.push(path)
    return failed ? { ok: false, status: 502 } : { ok: true, status: 200, blob: async () => new Blob(['speech'], { type: 'audio/ogg' }) }
  }
  URL.createObjectURL = blob => { const url = `blob:${Math.random()}`; blobs.set(url, blob); return url }
  URL.revokeObjectURL = url => blobs.delete(url)
})
afterEach(() => {
  instance?.cancel()
  instance = undefined
  URL.createObjectURL = oldCreate
  URL.revokeObjectURL = oldRevoke
  blobs.clear()
})
test('autoplay denial preserves the utterance and resumes it without another synthesis request', async () => {
  instance = useNarration(() => {})
  blocked = true
  instance.enqueue('room', [log('a')], true)
  await until(() => instance.state.value === 'blocked')
  assert.equal(instance.queuedCount.value, 1)
  assert.equal(instance.current.value.id, 'a')
  blocked = false
  instance.activate()
  await until(() => instance.state.value === 'playing')
  assert.equal(fetches.length, 1)
  media.at(-1).onended()
  await until(() => instance.state.value === 'idle')
  assert.equal(instance.queuedCount.value, 0)
})
test('public speeches play in order, including more than eight queued records, and duplicates are ignored', async () => {
  instance = useNarration(() => {})
  const entries = Array.from({length:10}, (_, i) => log(String(i)))
  instance.enqueue('room', entries, true)
  instance.enqueue('room', [entries[0]], true)
  for (let i=0; i<10; i++) {
    await until(() => instance.state.value === 'playing' && instance.activeLog.value === String(i))
    media.at(-1).onended()
  }
  await until(() => instance.queuedCount.value === 0)
  assert.equal(fetches.length, 10)
})
test('cloud failure uses the browser voice with a visible source and player identification', async () => {
  const errors = []
  instance = useNarration(text => errors.push(text))
  failed = true
  instance.enqueue('room', [{...log('a','冬天也能见到。'),kind:'speech',seat:2,name:'小北'}], true)
  await until(() => instance.state.value === 'playing')
  assert.equal(instance.playbackSource.value, 'browser')
  assert.equal(instance.fallback.value, true)
  assert.equal(spoken[0].text, '2号：冬天也能见到。')
  assert.ok(errors[0].includes('浏览器语音'))
  spoken[0].onend()
  await until(() => instance.state.value === 'idle')
})
test('recording cancellation stops queued narration, and manual replay works after muting', async () => {
  instance = useNarration(() => {})
  instance.enqueue('room', [log('a'),log('b')], true)
  await until(() => instance.state.value === 'playing')
  instance.cancel()
  await new Promise(resolve => setTimeout(resolve,5))
  assert.equal(fetches.length,1)
  assert.equal(instance.current.value,null)
  instance.toggle()
  assert.equal(instance.enabled.value,false)
  instance.replay('room',log('b'),true)
  await until(() => instance.state.value === 'playing')
  assert.equal(instance.enabled.value,true)
  assert.equal(instance.activeLog.value,'b')
})
test('an unusable browser voice leaves a retryable item instead of silently losing it', async () => {
  instance = useNarration(() => {})
  instance.enqueue('room',[log('a')],false)
  await until(() => spoken.length > 0)
  spoken[0].onerror({error:'voice-unavailable'})
  await until(() => instance.state.value === 'error')
  assert.equal(instance.queuedCount.value,1)
  instance.activate()
  await until(() => spoken.length === 2)
  spoken[1].onend()
  await until(() => instance.state.value === 'idle')
})

test('cached group narration advances avatar cues by media time, not download time', async () => {
  instance = useNarration(() => {})
  const cues = [
    {offsetMs:1000,text:'二号的描述',playerId:'bot-2',logId:'s2',seat:2},
    {offsetMs:4000,text:'三号的描述',playerId:'bot-3',logId:'s3',seat:3},
  ]
  instance.enqueue('room',[{...log('reply'),audioCues:cues}],true)
  await until(() => instance.state.value === 'playing')
  assert.equal(instance.activeCue.value,null)
  const player = media.at(-1)
  player.currentTime=1.2; player.ontimeupdate()
  assert.equal(instance.activeCue.value.playerId,'bot-2')
  player.currentTime=3.5; player.ontimeupdate()
  assert.equal(instance.activeCue.value.playerId,'bot-2')
  player.currentTime=4.2; player.ontimeupdate()
  assert.equal(instance.activeCue.value.playerId,'bot-3')
  player.onended()
  await until(() => instance.state.value === 'idle')
  assert.equal(instance.activeCue.value,null)
  assert.equal(instance.completedLog.value.id,'reply')
})


test('a committed human or AI speech plays before the next prompt without waiting for a group reply', async () => {
  instance = useNarration(() => {})
  const own = {...log('human', '这是一个吃的东西。'), kind:'speech', seat:1, playerId:'me', audio:true}
  const prompt = log('next', '请2号发言。')
  const ai = {...log('ai3', '它经常出现在人们聚会的时候。'), kind:'speech', seat:3, playerId:'bot3'}
  instance.enqueue('room', narrationEntries([own,prompt], true), true)
  await until(() => instance.state.value === 'playing')
  assert.equal(instance.activeLog.value, 'human')
  assert.equal(instance.playbackSource.value, 'recording')
  assert.equal(instance.activeCue.value.playerId, 'me')
  // A later committed AI does not interrupt the current speaker or prompt.
  instance.enqueue('room', narrationEntries([ai], true), true)
  assert.equal(fetches.length, 1)
  media.at(-1).onended()
  await until(() => instance.activeLog.value === 'next' && instance.state.value === 'playing')
  media.at(-1).onended()
  await until(() => instance.activeLog.value === 'ai3' && instance.state.value === 'playing')
  assert.equal(instance.activeCue.value.seat, 3)
  assert.equal(fetches.length, 3)
  media.at(-1).onended()
  await until(() => instance.state.value === 'idle')
  assert.deepEqual(narrationEntries([{...log('free'),source:'linx_agent'}],true), [])
})
