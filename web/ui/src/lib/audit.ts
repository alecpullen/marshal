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
    default:
      return e.event
  }
}
