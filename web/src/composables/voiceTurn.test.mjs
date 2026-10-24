import { test } from 'node:test'
import assert from 'node:assert/strict'
import { canStartVoice, voiceTurnAllowed } from './voiceTurn.ts'

function room(own = 1) {
  return {phase:'speaking',myPlayerId:`p${own}`,currentPlayerId:'p2',isHost:own===1,agentActive:false,
    players:Array.from({length:4},(_,index)=>({id:`p${index+1}`,seat:index+1,alive:true,bot:false,hasVoted:false}))}
}
test('when player 2 has the turn only player 2 can start a voice session', () => {
  for (let own=1; own<=4; own++) {
    assert.equal(canStartVoice(room(own),false),own===2)
    const tied=room(own);tied.phase='tiebreak'
    assert.equal(canStartVoice(tied,false),own===2)
  }
})
test('playback finishes before the current player can record, including when the game already advanced', () => {
  assert.equal(canStartVoice(room(2),true),false)
  const advanced=room(3);advanced.currentPlayerId='p3'
  assert.equal(canStartVoice(advanced,true),false)
  assert.equal(canStartVoice(advanced,false),true)
  advanced.agentActive=true
  assert.equal(canStartVoice(advanced,false),false)
})
test('only the host can start or restart; dealing and eliminated players cannot interrupt', () => {
  for (const phase of ['waiting','finished']) {
    const host=room(1);host.phase=phase;host.players[0].alive=false
    const other=room(2);other.phase=phase
    assert.equal(voiceTurnAllowed(host),true)
    assert.equal(voiceTurnAllowed(other),false)
  }
  const dealing=room(1);dealing.phase='dealing'
  assert.equal(voiceTurnAllowed(dealing),false)
  const eliminated=room(2);eliminated.players[1].alive=false
  assert.equal(voiceTurnAllowed(eliminated),false)
})
test('voting permits each living player until that player has voted', () => {
  const value=room(1);value.phase='voting';value.currentPlayerId=''
  assert.equal(canStartVoice(value,false),true)
  value.players[0].hasVoted=true
  assert.equal(canStartVoice(value,false),false)
  value.players[0].hasVoted=false;value.players[0].alive=false
  assert.equal(canStartVoice(value,false),false)
  assert.equal(canStartVoice(null,false),false)
})
