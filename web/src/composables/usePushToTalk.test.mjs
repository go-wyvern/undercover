import { beforeEach, afterEach, test } from 'node:test'
import assert from 'node:assert/strict'
import { usePushToTalk } from './usePushToTalk.ts'
let ws, ctx, worklet, stopped, recorded, heard, errors, allowed, voice
class Context {
  currentTime = 0
  destination = {}
  audioWorklet = { addModule: async () => {} }
  closed = false
  sources = []
  constructor() {ctx = this}
  async resume() {}
  async close() {this.closed = true}
  createMediaStreamSource() {return {connect: node => node}}
  createGain() {return {gain:{value:1},connect:()=>{}}}
  createBuffer(channels,length,rate) {return {duration:length/rate,rate,getChannelData:()=>new Float32Array(length)}}
  createBufferSource() {const node = {connect(){},disconnect(){},start(at){this.at=at},stop(){this.stopped=true},buffer:null};this.sources.push(node);return node}
}
class Worklet {
  port = {onmessage:null, postMessage: () => this.port.onmessage({data:{type:'flushed'}})}
  constructor() {worklet=this}
  connect(node) {return node}
}
class Socket {
  static OPEN=1
  readyState=1
  sent=[]
  constructor(url) {ws=this;this.url=url}
  send(value) {this.sent.push(value)}
  close() {this.readyState=3}
}
beforeEach(()=>{
  stopped=false;recorded=[];heard=[];errors=[];allowed=true
  Object.defineProperty(globalThis,'navigator',{configurable:true,value:{mediaDevices:{getUserMedia:async()=>({getTracks:()=>[{stop(){stopped=true}}]})}}})
  globalThis.AudioContext=Context;globalThis.AudioWorkletNode=Worklet;globalThis.WebSocket=Socket
  globalThis.location={protocol:'http:',host:'localhost:18080'}
  voice=usePushToTalk((...args)=>recorded.push(args),text=>errors.push(text),id=>heard.push(id),()=>allowed)
})
afterEach(()=>voice.cancel())
async function ready(full=true) {await voice.start('my-room',full);await ws.onmessage({data:JSON.stringify({type:'ready',sampleRate:'24000'})})}
async function message(value){await ws.onmessage({data:JSON.stringify(value)})}
test('full conversation survives transcription and plays PCM before assistant_done',async()=>{
  await ready()
  assert.ok(ws.url.endsWith('/assistant/voice'))
  worklet.port.onmessage({data:{type:'pcm',buffer:new ArrayBuffer(640)}})
  await voice.stop()
  assert.equal(ctx.closed,false)
  assert.equal(stopped,true)
  await message({type:'transcript',text:'开始游戏'})
  assert.equal(voice.recognizing.value,true)
  assert.equal(recorded.length,0)
  assert.equal(ws.readyState,1)
  await ws.onmessage({data:new ArrayBuffer(640)})
  assert.equal(voice.streaming.value,false)
  ctx.currentTime=.081
  await new Promise(resolve=>setTimeout(resolve,35))
  assert.equal(voice.streaming.value,true)
  assert.equal(ctx.sources[0].buffer.rate,24000)
  await message({type:'assistant_reply_id',id:'reply-1'})
  assert.deepEqual(heard,['reply-1'])
  ctx.currentTime=10
  await message({type:'assistant_done',id:'reply-1',streamed:'true'})
  assert.equal(ctx.closed,true)
  assert.equal(voice.streaming.value,false)
  assert.deepEqual(errors,[])
})
test('muting stops scheduled output while keeping the model conversation alive',async()=>{
  await ready()
  await voice.stop()
  await ws.onmessage({data:new ArrayBuffer(640)})
  allowed=false;voice.muteOutput()
  assert.equal(ctx.sources[0].stopped,true)
  await ws.onmessage({data:new ArrayBuffer(640)})
  assert.equal(ctx.sources.length,1)
  assert.equal(ws.readyState,1)
})
test('legacy transcription still returns a draft and closes the microphone',async()=>{
  await ready(false)
  worklet.port.onmessage({data:{type:'pcm',buffer:new ArrayBuffer(640)}})
  await voice.stop()
  await message({type:'transcript',text:'这是我的描述'})
  assert.equal(recorded[0][0],'这是我的描述')
  assert.equal(recorded[0][1].length,640)
  assert.equal(ctx.closed,true)
  assert.equal(ws.readyState,3)
})

test('sentences arriving ahead of audio remain hidden until their scheduled playback',async()=>{
  await ready()
  await voice.stop()
  await message({type:'assistant_sentence',text:'二号的描述',playerId:'bot-2',logId:'speech-2',seat:'2'})
  await ws.onmessage({data:new ArrayBuffer(48000)}) // One second at 24 kHz.
  await message({type:'assistant_sentence',text:'三号的描述',playerId:'bot-3',logId:'speech-3',seat:'3'})
  await ws.onmessage({data:new ArrayBuffer(48000)})
  assert.equal(voice.activeCue.value,null)
  assert.equal(voice.replyText.value,'')
  ctx.currentTime=.2
  await new Promise(resolve=>setTimeout(resolve,35))
  assert.equal(voice.activeCue.value.playerId,'bot-2')
  assert.equal(voice.replyText.value,'二号的描述')
  ctx.currentTime=1.2
  await new Promise(resolve=>setTimeout(resolve,35))
  assert.equal(voice.activeCue.value.playerId,'bot-3')
  assert.equal(voice.replyText.value,'三号的描述')
})


test('captures the beginning while connecting and sends it before live audio',async()=>{
  await voice.start('my-room',true)
  assert.equal(voice.recording.value,true)
  assert.equal(voice.connecting.value,true)
  const early = new Uint8Array([1,0,2,0])
  worklet.port.onmessage({data:{type:'pcm',buffer:early.buffer}})
  assert.equal(ws.sent.length,0)
  await message({type:'ready',sampleRate:'24000'})
  assert.deepEqual(ws.sent,[early])
  const live = new Uint8Array([3,0,4,0])
  worklet.port.onmessage({data:{type:'pcm',buffer:live.buffer}})
  await voice.stop()
  assert.deepEqual(ws.sent.slice(0,2),[early,live])
  assert.deepEqual(JSON.parse(ws.sent[2]),{type:'stop'})
  assert.equal(voice.recognizing.value,true)
  assert.deepEqual(errors,[])
})

test('releasing before ready preserves the whole utterance and queues one stop',async()=>{
  await voice.start('my-room',true)
  const pcm = new Uint8Array(640).fill(1)
  worklet.port.onmessage({data:{type:'pcm',buffer:pcm.buffer}})
  await voice.stop()
  assert.equal(stopped,true)
  assert.equal(ctx.closed,false)
  assert.equal(voice.recognizing.value,true)
  assert.equal(ws.sent.length,0)
  assert.equal(ws.readyState,1)
  await message({type:'ready'})
  await message({type:'ready'})
  assert.equal(ws.sent.length,2)
  assert.deepEqual(ws.sent[0],pcm)
  assert.deepEqual(JSON.parse(ws.sent[1]),{type:'stop'})
  await message({type:'transcript',text:'这是连接期间完整录下的描述'})
  assert.equal(voice.transcript.value,'这是连接期间完整录下的描述')
  assert.deepEqual(errors,[])
})

test('explicit cancellation discards pending audio and ignores a late ready',async()=>{
  await voice.start('my-room',true)
  worklet.port.onmessage({data:{type:'pcm',buffer:new ArrayBuffer(640)}})
  voice.cancel()
  await message({type:'ready'})
  assert.equal(ws.sent.length,0)
  assert.equal(ws.readyState,3)
  assert.equal(voice.recording.value,false)
  assert.deepEqual(errors,[])
})


test('flushes the final partial frame before a queued stop',async()=>{
  await voice.start('my-room',true)
  const tail = new Uint8Array([7,0,8,0])
  worklet.port.postMessage = () => {
    worklet.port.onmessage({data:{type:'pcm',buffer:tail.buffer}})
    worklet.port.onmessage({data:{type:'flushed'}})
  }
  await voice.stop()
  assert.equal(ws.sent.length,0)
  await message({type:'ready'})
  assert.deepEqual(ws.sent[0],tail)
  assert.deepEqual(JSON.parse(ws.sent[1]),{type:'stop'})
})

test('an early transcript does not prevent release from stopping the microphone',async()=>{
  await ready()
  await message({type:'transcript',text:'提前识别的片段'})
  await voice.stop()
  assert.equal(stopped,true)
  assert.equal(voice.recording.value,false)
  assert.deepEqual(JSON.parse(ws.sent.at(-1)),{type:'stop'})
})
