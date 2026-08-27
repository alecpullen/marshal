export type ExitDestination = 'merge' | 'push' | 'patch'

/**
 * Mirrors the server's exitDestination. The destination follows the
 * agent's source and is never a user choice, so the UI must not offer
 * one — it only renders the outcome the server will take.
 */
export function exitDestination(a: { sourceKind: string; readOnly: boolean }): ExitDestination {
  if (a.sourceKind !== 'git') return 'merge'
  return a.readOnly ? 'patch' : 'push'
}
