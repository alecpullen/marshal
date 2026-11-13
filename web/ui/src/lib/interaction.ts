/**
 * One strict model for the interaction tuple.
 *
 * Every browser surface — chat, fleet cards, stored defaults — parses the
 * agent's committed state through these helpers. That buys three properties
 * the old scattered mode handling lost:
 *
 *   - an explicit false survives, because presence is never collapsed into a
 *     truthiness check;
 *   - an unknown or malformed live mode is rejected, never treated as some
 *     default that would claim authority;
 *   - a report from an older revision cannot overwrite a newer one, and a
 *     revision is only compared within the session that issued it.
 *
 * This module holds no policy and grants nothing. It parses, formats and
 * reduces what the agent reported.
 */

export type Mode = 'ask' | 'act'

/**
 * The committed state as the bridge puts it on the wire. Generated from the Go
 * contract (`web/bridge/contract`), so it has exactly one definition; the
 * app-side parse below is what turns it into the stricter `AccessState`.
 */
import type { AccessState as ContractAccessState } from './contract'
export type AccessStateWire = ContractAccessState

/** The two interaction modes, in picker order. Auto and System are modifiers. */
export const MODES: readonly Mode[] = ['ask', 'act']

export interface AccessState {
  mode: Mode
  auto: boolean
  /** Broader filesystem access: a session grant, never a starting default. */
  system: boolean
  revision: number
}

/** One atomic access change. Absent fields are left alone; false is a value. */
export interface AccessPatch {
  mode?: string
  auto?: boolean
  system_access?: boolean
}

/** A session's committed state, tagged with the session that issued it. */
export interface AccessHolder {
  sessionId: string
  access: AccessState
}

/**
 * A stored starting default. `auto` is absent when the source did not set it,
 * which is not the same as an explicit false.
 */
export interface StoredDefaults {
  mode: Mode
  auto?: boolean
}

/** One capability request, forwarded verbatim from the agent. */
export interface ModeAccessRequest {
  requestId: string
  sessionId: string
  actor?: string
  stepId?: number
  reason: string
  current: AccessStateWire
  missingEditing: boolean
  missingSystem: boolean
}

/** The typed answer to a capability request. */
export interface ModeAccessChoice {
  requestId: string
  sessionId: string
  current: AccessStateWire
  mode?: string
}

/** Parse one live mode spelling. Anything but ask/act is unknown, not a default. */
export function parseMode(raw: unknown): Mode | null {
  if (typeof raw !== 'string') return null
  const mode = raw.trim().toLowerCase()
  return mode === 'ask' || mode === 'act' ? (mode as Mode) : null
}

/**
 * Parse a committed access report strictly. A missing, mistyped or unknown
 * field yields null: the caller keeps presenting the state it had rather than
 * inventing a grant. `ask` with Auto is impossible and is rejected too.
 */
export function parseAccessState(raw: unknown): AccessState | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  const mode = parseMode(r.mode)
  if (!mode) return null
  if (typeof r.auto !== 'boolean' || typeof r.system_access !== 'boolean') return null
  if (typeof r.access_revision !== 'number' || !Number.isInteger(r.access_revision) || r.access_revision < 0) return null
  if (mode === 'ask' && r.auto) return null
  return { mode, auto: r.auto, system: r.system_access, revision: r.access_revision }
}

/** Project an app-side state back onto the wire shape. */
export function toAccessWire(a: AccessState): AccessStateWire {
  return { mode: a.mode, auto: a.auto, system_access: a.system, access_revision: a.revision }
}

/** Whether two states are the same committed tuple. */
export function sameAccess(a: AccessState | null, b: AccessState | null): boolean {
  if (a === null || b === null) return a === b
  return a.mode === b.mode && a.auto === b.auto && a.system === b.system && a.revision === b.revision
}

/**
 * Reduce one committed report. Returns null when the report must be ignored:
 * a malformed state, or a replay from an older revision within the same
 * session. A report for a different session replaces the comparison, because
 * a revision is only comparable within the session that issued it.
 */
export function applyAccessReport(current: AccessHolder | null, sessionId: string, raw: unknown): AccessHolder | null {
  const access = parseAccessState(raw)
  if (!access) return null
  if (current && current.sessionId === sessionId && access.revision < current.access.revision) return null
  return { sessionId, access }
}

/** A compact label for the committed tuple: "Ask", "Act", "Act + Auto". */
export function accessLabel(a: AccessState): string {
  const parts = [a.mode === 'act' ? 'Act' : 'Ask']
  if (a.mode === 'act' && a.auto) parts.push('Auto')
  if (a.system) parts.push('System')
  return parts.join(' + ')
}

/**
 * The JSON body for PATCH /api/sessions/{id}/access. Absent fields are
 * omitted, so "leave it alone" stays distinct from "set it false".
 */
export function accessPatchBody(p: AccessPatch): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  if (p.mode !== undefined) out.mode = p.mode
  if (p.auto !== undefined) out.auto = p.auto
  if (p.system_access !== undefined) out.system_access = p.system_access
  return out
}

/**
 * Translate one stored starting default from the retired five-mode spellings.
 * Presence of Auto is preserved: the key is copied only when the source set
 * it. An unknown spelling returns null so the caller can report it — it is
 * never promoted to Act.
 */
export function migrateStoredDefaults(raw: { mode?: unknown; auto?: unknown }): StoredDefaults | null {
  let auto: boolean | undefined
  if (raw.auto === true) auto = true
  else if (raw.auto === false) auto = false
  else if (raw.auto !== undefined) return null

  const withAuto = (mode: Mode): StoredDefaults | null => {
    // Ask cannot carry Auto; that pair is impossible, not a value to keep.
    if (mode === 'ask' && auto === true) return null
    const d: StoredDefaults = { mode }
    if (auto !== undefined) d.auto = auto
    return d
  }

  const mode = typeof raw.mode === 'string' ? raw.mode.trim().toLowerCase() : ''
  switch (mode) {
    case '':
      // A source that names no mode takes the engine's starting default.
      return withAuto('act')
    case 'ask':
      return withAuto('ask')
    case 'act':
      return withAuto('act')
    case 'read':
    case 'plan':
    case 'default':
    case 'edit':
      return withAuto('ask')
    case 'copilot':
      return withAuto('act')
    case 'auto':
      return { mode: 'act', auto: auto ?? true }
    default:
      return null
  }
}

/**
 * Parse the typed capability presentation of a mode.request. A missing or
 * malformed bundle yields null, which callers treat as an ordinary exact-only
 * approval: it grants the presented action, never a standing capability.
 */
export function parseModeAccess(raw: unknown): ModeAccessRequest | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  if (typeof r.requestId !== 'string' || r.requestId === '') return null
  if (typeof r.sessionId !== 'string' || r.sessionId === '') return null
  const current = parseAccessState(r.current)
  if (!current) return null
  return {
    requestId: r.requestId,
    sessionId: r.sessionId,
    actor: typeof r.actor === 'string' ? r.actor : undefined,
    stepId: typeof r.stepId === 'number' && Number.isInteger(r.stepId) ? r.stepId : undefined,
    reason: typeof r.reason === 'string' ? r.reason : '',
    current: toAccessWire(current),
    missingEditing: r.missingEditing === true,
    missingSystem: r.missingSystem === true,
  }
}
