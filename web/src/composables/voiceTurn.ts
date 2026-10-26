import type { RoomView } from '../types'

// Playback and an active connection are handled separately from game turn access.
export function voiceTurnAllowed(room: RoomView | null) {
  if (!room) return false
  const player = room.players.find(player => player.id === room.myPlayerId)
  if (!player || player.bot) return false
  if (room.phase === 'waiting' || room.phase === 'finished') return room.isHost
  if (room.action?.state === 'waiting_playback') return false
  if (!player.alive) return false
  if (room.phase === 'speaking' || room.phase === 'tiebreak')
    return room.currentPlayerId === player.id
  return room.phase === 'voting' && !player.hasVoted
}

export function canStartVoice(room: RoomView | null, playbackPending: boolean) {
  return voiceTurnAllowed(room) && !room?.agentActive && !playbackPending
}
