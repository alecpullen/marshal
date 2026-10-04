import type { Tone } from '../glyphs'

/** Colors a skill's risk label; anything unrecognised reads as neutral. */
export function riskTone(risk: string): Tone {
  switch (risk.toLowerCase()) {
    case 'low':
    case 'safe':
      return 'ok'
    case 'medium':
      return 'warn'
    case 'high':
      return 'err'
    default:
      return 'neutral'
  }
}
