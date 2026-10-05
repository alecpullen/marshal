import type { NetAgentRow, NetHostRow, NetRecord, NetRule } from '../api'

export type HostSortKey = 'host' | 'rule' | 'requests' | 'bytes' | 'agents' | 'lastSeen'
export interface HostSort { key: HostSortKey; dir: 'asc' | 'desc' }

/**
 * A host row with the derived columns the table shows. The bridge aggregates
 * per workspace or per agent and does not say how many agents reached a host,
 * so `agents` is 1 for an agent-scoped row and absent for a workspace one.
 */
export interface HostView extends NetHostRow { bytes: number; agents?: number }

export function hostViews(rows: NetHostRow[]): HostView[] {
  return rows.map((r) => ({ ...r, bytes: r.bytesUp + r.bytesDown, ...(r.agentId ? { agents: 1 } : {}) }))
}

const val = (h: HostView, k: HostSortKey): string | number => (k === 'agents' ? (h.agents ?? 0) : h[k])

export function sortHosts(rows: HostView[], s: HostSort): HostView[] {
  const sign = s.dir === 'asc' ? 1 : -1
  return [...rows].sort((a, b) => {
    const x = val(a, s.key)
    const y = val(b, s.key)
    const c = typeof x === 'number' && typeof y === 'number' ? x - y : String(x).localeCompare(String(y))
    return sign * c || a.host.localeCompare(b.host)
  })
}

/** Records whose host contains `filter` (case-insensitive), newest first. */
export function filterRequests(rows: NetRecord[], filter: string): NetRecord[] {
  const f = filter.trim().toLowerCase()
  return [...rows].filter((r) => !f || r.host.toLowerCase().includes(f)).sort((a, b) => b.at - a.at)
}

export const RULE_LABEL: Record<NetRule, string> = {
  allowlisted: 'allowlisted',
  granted: 'granted',
  open: 'open',
  injected: 'injected',
  blocked: 'blocked',
  allowed: 'allowed',
}

export function ruleTone(rule: string): 'ok' | 'err' | 'warn' | 'info' | 'neutral' {
  switch (rule) {
    case 'blocked':
      return 'err'
    case 'injected':
      return 'info'
    case 'granted':
      return 'warn'
    case 'allowlisted':
    case 'allowed':
      return 'ok'
    default:
      return 'neutral'
  }
}

export const PROCESS_MODE_BANNER = 'Not isolated: agents run as processes, so the proxy is advisory'

export function bytesLabel(n: number): string {
  if (n < 1024) return `${n} B`
  const u = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)} ${u[i]}`
}

export function agoMs(at: number, now = Date.now()): string {
  if (!at) return ''
  const s = Math.max(0, Math.floor((now - at) / 1000))
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  return h < 48 ? `${h}h ago` : `${Math.floor(h / 24)}d ago`
}

export const clockTime = (at: number) => new Date(at).toISOString().slice(11, 19)

export type { NetAgentRow }
