/*
  The session dock's state machine (spec §6.2). Pure: the component holds
  the state, applies actions through `reduce`, and persists the size and
  width only.
*/

export type DockSize = 'collapsed' | 'docked' | 'expanded'
export type DockMode = 'follow' | 'select'
export type DockTab = 'inspect' | 'changes' | 'files' | 'terminal' | 'preview'

export interface DockState {
  size: DockSize
  width: number
  mode: DockMode
  tab: DockTab
  pinned: boolean
  selected?: string
  unseenChanges: boolean
  /** Terminal output arrived while the tab was not showing. */
  unseenTerminal: boolean
}

export type DockAction =
  | { type: 'cycleSize' }
  | { type: 'setSize'; size: DockSize }
  | { type: 'resize'; px: number }
  | { type: 'select'; nodeId: string; kind: string; reveal?: boolean }
  | { type: 'backToLive' }
  | { type: 'openTab'; tab: DockTab }
  | { type: 'togglePin' }
  | { type: 'liveEdit' }
  | { type: 'terminalOutput' }

export const MIN_WIDTH = 320
export const MAX_WIDTH = 720
export const DEFAULT_WIDTH = 440
const STORAGE_KEY = 'marshal.ui.dock'

// Node kinds whose selection switches an unpinned dock to Inspect.
const INSPECTABLE = new Set(['tool', 'step', 'task', 'subagent'])
const NEXT_SIZE: Record<DockSize, DockSize> = { collapsed: 'docked', docked: 'expanded', expanded: 'collapsed' }

export function initialDock(over: Partial<DockState> = {}): DockState {
  return { size: 'docked', width: DEFAULT_WIDTH, mode: 'follow', tab: 'inspect', pinned: false, unseenChanges: false, unseenTerminal: false, ...over }
}

export function clampWidth(px: number): number {
  return Math.max(MIN_WIDTH, Math.min(MAX_WIDTH, Math.round(px)))
}

export function reduce(s: DockState, a: DockAction): DockState {
  switch (a.type) {
    case 'cycleSize':
      return { ...s, size: NEXT_SIZE[s.size] }
    case 'setSize':
      return { ...s, size: a.size }
    case 'resize':
      return { ...s, width: clampWidth(a.px) }
    case 'select': {
      const next: DockState = { ...s, mode: 'select', selected: a.nodeId }
      if (!s.pinned && INSPECTABLE.has(a.kind)) next.tab = 'inspect'
      if (a.reveal !== false && s.size === 'collapsed') next.size = 'docked'
      return next
    }
    case 'backToLive':
      return { ...s, mode: 'follow', selected: undefined }
    case 'openTab':
      return {
        ...s,
        tab: a.tab,
        size: s.size === 'collapsed' ? 'docked' : s.size,
        unseenChanges: a.tab === 'changes' ? false : s.unseenChanges,
        unseenTerminal: a.tab === 'terminal' ? false : s.unseenTerminal,
      }
    case 'togglePin':
      return { ...s, pinned: !s.pinned }
    case 'liveEdit':
      // Following, unpinned and visible: show the edit. Otherwise leave a mark.
      if (s.mode === 'follow' && !s.pinned && s.size !== 'collapsed') return { ...s, tab: 'changes', unseenChanges: false }
      if (s.size === 'collapsed' || s.tab !== 'changes') return { ...s, unseenChanges: true }
      return s
    case 'terminalOutput':
      return s.size === 'collapsed' || s.tab !== 'terminal' ? (s.unseenTerminal ? s : { ...s, unseenTerminal: true }) : s
  }
}

/** The part of the state that outlives the page. */
export function persistable(s: DockState): { size: DockSize; width: number } {
  return { size: s.size, width: s.width }
}

export function load(): { size: DockSize; width: number } {
  const out = { size: 'docked' as DockSize, width: DEFAULT_WIDTH }
  try {
    const raw = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? 'null') as { size?: unknown; width?: unknown } | null
    if (raw && (raw.size === 'collapsed' || raw.size === 'docked' || raw.size === 'expanded')) out.size = raw.size
    if (raw && typeof raw.width === 'number' && Number.isFinite(raw.width)) out.width = clampWidth(raw.width)
  } catch {
    // Unreadable storage leaves the defaults.
  }
  return out
}

export function save(s: DockState): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(persistable(s)))
  } catch {
    // The choice just does not persist.
  }
}
