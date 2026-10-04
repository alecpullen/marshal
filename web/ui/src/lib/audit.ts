export interface AuditEvent {
  ts: string
  event: string
  ownerId?: string
  agentId?: string
  clientId?: string
  repoId?: string
  origin?: string
  reason?: string
  detail?: string
  bytes?: number
}

export function describeAuditEvent(e: AuditEvent): string {
  switch (e.event) {
    case 'spawn':
      return `spawned ${e.agentId ?? '?'} via ${e.origin ?? '?'}${e.clientId ? ` (${e.clientId})` : ''}`
    case 'spawn_denied':
      return `denied spawn${e.agentId ? ` of ${e.agentId}` : ''}${e.reason ? `: ${e.reason}` : ''}`
    case 'pending_approved':
      return `approved ${e.agentId ?? 'a submission'}`
    case 'pending_denied':
      return `denied a pending submission${e.reason ? `: ${e.reason}` : ''}`
    case 'gate_override':
      return `overrode the gate on ${e.agentId ?? '?'}${e.reason ? `: ${e.reason}` : ''}`
    case 'push':
      return `pushed ${e.agentId ?? '?'}${e.detail ? ` to ${e.detail}` : ''}`
    case 'patch_export':
      return `exported a patch from ${e.agentId ?? '?'}`
    case 'client_created':
      return `created client ${e.clientId ?? '?'}${e.detail ? ` (${e.detail})` : ''}`
    case 'client_revoked':
      return `revoked client ${e.clientId ?? '?'}`
    case 'repo_registered':
      return `registered repo ${e.repoId ?? '?'}`
    case 'repo_removed':
      return `removed repo ${e.repoId ?? '?'}`
    case 'prune':
      return `pruned ${e.bytes ?? 0} bytes`
    case 'skill_installed':
      return `installed skill ${e.detail ?? ''}`.trim()
    case 'skill_removed':
      return `removed skill ${e.detail ?? ''}`.trim()
    case 'plugin_installed':
      return `installed plugin ${e.detail ?? ''}`.trim()
    case 'plugin_removed':
      return `removed plugin ${e.detail ?? ''}`.trim()
    case 'memory_deleted':
      return `deleted a project memory${e.detail ? ` (${e.detail})` : ''}`
    case 'models_changed':
      return `changed model settings${e.detail ? ` (${e.detail})` : ''}${e.reason ? `: ${e.reason}` : ''}`
    case 'budgets_changed':
      return 'changed budgets'
    case 'budget_override':
      return `overrode the budget pause on ${e.agentId ?? '?'}`
    case 'watch_started':
      return `started watch ${e.detail ?? ''}`.trim()
    case 'watch_stopped':
      return `stopped watch ${e.detail ?? ''}`.trim()
    default:
      return e.event
  }
}

/** The event names the audit tab can filter on. */
export const AUDIT_EVENTS = [
  'spawn',
  'spawn_denied',
  'pending_approved',
  'pending_denied',
  'gate_override',
  'push',
  'patch_export',
  'client_created',
  'client_revoked',
  'repo_registered',
  'repo_removed',
  'prune',
  'skill_installed',
  'skill_removed',
  'plugin_installed',
  'plugin_removed',
  'memory_deleted',
  'models_changed',
  'budgets_changed',
  'budget_override',
  'watch_started',
  'watch_stopped',
] as const
