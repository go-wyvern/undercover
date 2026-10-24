export interface User {
  id: string
  username: string
  displayName: string
  avatar: string
  guest: boolean
}
export interface Config {
  voiceConfigured: boolean
  aiConfigured: boolean
  authEnabled: boolean
  allowGuest: boolean
}
export interface Player {
  id: string
  name: string
  avatar?: string
  seat: number
  bot: boolean
  alive: boolean
  hasVoted: boolean
}
export interface AudioCue {
  offsetMs: number
  text: string
  logId?: string
  playerId?: string
  seat?: number
}
export interface GameLog {
  id: string
  kind: 'host' | 'speech' | 'votes'
  source?: 'linx_agent'
  playerId?: string
  seat?: number
  name?: string
  text: string
  round: number
  at: number
  audioCues?: AudioCue[]
  audio?: boolean
}
export type Phase = 'waiting' | 'dealing' | 'speaking' | 'tiebreak' | 'voting' | 'finished'
export interface RoomSummary {
  id: string
  name: string
  theme: string
  capacity: number
  humanCount: number
  phase: Phase
  players: Player[]
}
export interface RoomView extends RoomSummary {
  agentActive: boolean
  gameId: string
  version: number
  isHost: boolean
  myPlayerId: string
  myDraft?: string
  myWord?: string
  round: number
  currentPlayerId: string
  candidateIds: string[]
  legalTargets: string[]
  logs: GameLog[]
  winner?: 'civilian' | 'undercover'
  deadline: number
  reveals: {
    playerId: string
    seat: number
    name: string
    word: string
    role: 'civilian' | 'undercover'
  }[]
  lastVotes: { playerId: string; targetId: string }[]
}
export interface AccountInfo {
  user: User | null
  currentRoomId: string
  enabled: boolean
  allowGuest: boolean
}
