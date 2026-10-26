import { test } from 'node:test'
import assert from 'node:assert/strict'
import { playbackReadyResult, pendingPlaybackLogs } from './playbackGate.ts'
import { canStartVoice } from './voiceTurn.ts'

const gate = () => ({id:'turn-2',boundaryId:'prompt-2',logIds:['speech-1','speech-ai','prompt-2'],state:'waiting_playback',required:true,ready:false,textOnly:false})
test('all preceding live utterances must finish, even if the boundary arrives early', () => {
  const action=gate(), played=new Set(['prompt-2'])
  assert.equal(playbackReadyResult(action,true,played),undefined)
  played.add('speech-1')
  assert.equal(playbackReadyResult(action,true,played),undefined)
  played.add('speech-ai')
  assert.equal(playbackReadyResult(action,true,played),'played')
})
test('muting explicitly completes the gate without pretending that audio played', () => {
  assert.equal(playbackReadyResult(gate(),false,new Set()),'muted')
  for (const patch of [{ready:true},{required:false},{state:'active'}])
    assert.equal(playbackReadyResult({...gate(),...patch},false,new Set()),undefined)
})
test('reconnect selects only unfinished prerequisite records, in their committed order', () => {
  const room={action:gate(), logs:[{id:'old'},{id:'speech-1'},{id:'speech-ai'},{id:'prompt-2'},{id:'later-query'}]}
  assert.deepEqual(pendingPlaybackLogs(room,new Set(['speech-1'])).map(l=>l.id),['speech-ai','prompt-2'])
})
test('server gate controls both speaking and voting even when local audio is idle', () => {
  for (const phase of ['speaking','tiebreak','voting']) {
    const room={phase,myPlayerId:'me',currentPlayerId:'me',agentActive:false,players:[{id:'me',alive:true,bot:false,hasVoted:false}],action:gate()}
    assert.equal(canStartVoice(room,false),false)
    room.action.state='active'
    assert.equal(canStartVoice(room,false),true)
    room.myPlayerId='other'
    assert.equal(canStartVoice(room,false),false)
  }
})
