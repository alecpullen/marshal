import type { MemorySuggestion } from '../api'

const KEY = 'marshal.ui.memory.dismissed'

/** A suggestion is dismissed per memory and matching project. */
export const dismissKey = (s: Pick<MemorySuggestion, 'memoryId' | 'matchProjectRoot'>): string => `${s.memoryId}:${s.matchProjectRoot}`

export function loadDismissed(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? '[]')
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
  } catch {
    return []
  }
}

export function saveDismissed(keys: string[]): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(keys))
  } catch {
    // Not persisted; the suggestion shows again next visit.
  }
}
