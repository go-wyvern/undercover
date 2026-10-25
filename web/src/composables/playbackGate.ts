import type { ActionWindow, RoomView } from '../types'

export function playbackReadyResult(gate: ActionWindow, enabled: boolean, played: ReadonlySet<string>) {
  if (!gate.required || gate.ready || gate.state !== 'waiting_playback') return undefined
  if (!enabled) return 'muted'
  if (gate.logIds.every(id => played.has(id))) return 'played'
  return undefined
}

export function pendingPlaybackLogs(room: RoomView, played: ReadonlySet<string>) {
  const ids = new Set(room.action?.logIds || [])
  return room.logs.filter(log => ids.has(log.id) && !played.has(log.id))
}
