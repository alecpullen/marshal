/*
  Browser notifications for fleet events. The bridge's webhooks cover people
  who are away from the Studio; this covers the tab sitting in the background.
  The event names match the bridge's (web/bridge/notify.go).
*/
import type { NotifyEvent } from './api'

export const NOTIFY_EVENTS: { id: NotifyEvent; label: string }[] = [
  { id: 'needs_you', label: 'An agent needs you' },
  { id: 'run_finished', label: 'A run finished' },
  { id: 'budget', label: 'A budget cap was reached' },
  { id: 'automation', label: 'An automation ran' },
  { id: 'watch_fired', label: 'A watch fired' },
  { id: 'network_block', label: 'A request was blocked' },
]

export type NotifyPrefs = Record<NotifyEvent, boolean>
export interface Toast { event: NotifyEvent; title: string; body: string; url: string }

const KEY = 'marshal.ui.notify'

const allOn = (): NotifyPrefs => Object.fromEntries(NOTIFY_EVENTS.map((e) => [e.id, true])) as NotifyPrefs

/** Stored choices over an all-on default; unreadable storage leaves everything on. */
export function loadPrefs(): NotifyPrefs {
  const prefs = allOn()
  try {
    const raw = JSON.parse(localStorage.getItem(KEY) ?? 'null') as Record<string, unknown> | null
    if (raw && typeof raw === 'object') for (const e of NOTIFY_EVENTS) if (typeof raw[e.id] === 'boolean') prefs[e.id] = raw[e.id] as boolean
  } catch {
    // Unreadable storage leaves the defaults.
  }
  return prefs
}

export function savePrefs(prefs: NotifyPrefs): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(prefs))
  } catch {
    // The choice just does not persist.
  }
}

/** The loosest shape of a fleet delta: only the fields notifications read. */
export interface FleetLike {
  kind: string
  sessionId?: string
  agentId?: string
  pendingKind?: string
  run?: { sdd?: { finished?: boolean; succeeded?: boolean; planName?: string; endedAt?: number } }
  event?: { name?: string; state?: string }
  scope?: string
  spentUsd?: number
  capUsd?: number
  host?: string
  title?: string
  text?: string
  budget?: { scope?: string; spentUsd?: number; capUsd?: number }
}

export interface NotifyContext {
  /** An agent's display name; the id is used when unknown. */
  nameOf?: (id: string) => string
  /** Finished runs already announced, so a repeated delta is silent. */
  seen?: Set<string>
}

/**
 * Maps a fleet delta to a notification, or null when it is not one, the
 * preference is off, or the tab is visible (the page already shows it).
 */
export function maybeNotify(d: FleetLike, prefs: NotifyPrefs, visible: boolean, ctx: NotifyContext = {}): Toast | null {
  if (visible) return null
  const id = d.agentId || d.sessionId || ''
  const agent = id && id !== 'studio' ? id : ''
  const name = (agent && ctx.nameOf?.(agent)) || agent || 'An agent'
  let t: Toast | null = null
  switch (d.kind) {
    case 'pending': {
      const what = d.pendingKind === 'approval' ? 'an approval' : d.pendingKind === 'question' ? 'an answer' : 'a decision'
      t = { event: 'needs_you', title: `${name} needs you`, body: `${name} is waiting for ${what}.`, url: agent ? `#chat/${encodeURIComponent(agent)}` : '#' }
      break
    }
    case 'run': {
      const sdd = d.run?.sdd
      if (!sdd?.finished) return null
      const key = `${agent}|${sdd.planName ?? ''}|${sdd.endedAt ?? ''}`
      if (ctx.seen?.has(key)) return null
      ctx.seen?.add(key)
      const outcome = sdd.succeeded ? 'succeeded' : 'failed'
      t = { event: 'run_finished', title: `${name} run ${outcome}`, body: `The run on ${name} ${outcome}.`, url: agent ? `#runs/${encodeURIComponent(agent)}` : '#runs' }
      break
    }
    case 'budget': {
      const b = d.budget ?? d
      t = { event: 'budget', title: 'Budget cap reached', body: `The ${b.scope ?? 'daily'} budget reached $${(b.spentUsd ?? 0).toFixed(2)} of its $${(b.capUsd ?? 0).toFixed(2)} cap.`, url: '#usage' }
      break
    }
    case 'automation':
      t = { event: 'automation', title: d.title || 'Automation ran', body: d.text || d.title || 'An automation ran.', url: agent ? `#chat/${encodeURIComponent(agent)}` : '#' }
      break
    case 'watch':
      if (d.event?.state !== 'fired') return null
      t = { event: 'watch_fired', title: 'Watch fired', body: `The watch ${d.event.name ?? ''} fired.`.replace('  ', ' '), url: '#watches' }
      break
    case 'network_block':
      t = { event: 'network_block', title: 'Network request blocked', body: `${name} tried to reach ${d.host ?? 'a host'}, which its policy blocks.`, url: agent ? `#network?agent=${encodeURIComponent(agent)}` : '#' }
      break
    default:
      return null
  }
  return prefs[t.event] ? t : null
}

export function permission(): NotificationPermission | 'unsupported' {
  return typeof Notification === 'undefined' ? 'unsupported' : Notification.permission
}

/** Shows a notification when permission is granted; a click focuses the tab and goes to the event. */
export function show(t: Pick<Toast, 'title' | 'body' | 'url'>): void {
  if (permission() !== 'granted') return
  try {
    const n = new Notification(t.title, { body: t.body })
    n.onclick = () => {
      window.focus()
      window.location.hash = t.url
      n.close()
    }
  } catch {
    // Some browsers refuse constructed notifications; the in-app inbox still shows the event.
  }
}
