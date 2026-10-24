import type { AudioCue, GameLog } from '../types'

// Cues advance only with the audio clock, never with network receipt time.
export function cueAt(cues: AudioCue[], milliseconds: number): AudioCue | null {
  let result: AudioCue | null = null
  for (const cue of cues) {
    if (cue.offsetMs > milliseconds) break
    result = cue
  }
  return result
}
export function logCue(log: GameLog): AudioCue {
  return { offsetMs: 0, text: log.text, logId: log.id, playerId: log.playerId, seat: log.seat }
}
