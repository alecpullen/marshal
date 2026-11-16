import type { FactoryCheckKey, FactoryConfig } from '../factory'

/**
 * Factory configuration presentation (design C5): the pure pieces creation
 * and the saved draft's approval both read, so one vocabulary describes what
 * a handoff will do.
 *
 * Nothing here resolves a fallback of its own: `resolvedConfigRows` reads a
 * configuration the bridge has already resolved, and the creation form owns
 * its own inheritance rules. A routing object is only ever read for its
 * profile — its other members belong to the route it came from.
 */

/** One option in a choice control: the value a handoff stores, and its label. */
export interface ChoiceOption {
  value: string
  label: string
}

/** One line of an approval summary: the effective value and where it came from. */
export interface ConfigSummaryRow {
  label: string
  value: string
  source?: string
  warning?: boolean
}

/**
 * A routing object only ever contributes its profile; every other member
 * belongs to the route it came from and is left untouched.
 */
export function profileOf(route: { profile?: unknown } | undefined): string {
  return typeof route?.profile === 'string' ? route.profile : ''
}

/**
 * Kept visible when a saved value is no longer in a picker, so the choice is
 * never silently discarded — and never replaced by the blank follow option.
 */
export function keepUnavailable(options: ChoiceOption[], value: string): ChoiceOption[] {
  return value && !options.some((option) => option.value === value)
    ? [...options, { value, label: `${value} (unavailable)` }]
    : options
}

/**
 * One check identity in the editor's syntax: a check run is identified by its
 * GitHub App, a commit status by its context alone.
 */
export function formatFactoryCheck(check: FactoryCheckKey): string {
  return check.kind === 'commit_status' ? `${check.kind}:${check.name}` : `${check.kind}:${check.name}:${check.appId}`
}

/**
 * A saved draft's resolved configuration as readable rows. The bridge has
 * already resolved every fallback, so no row names a source, and an absent
 * value is stated rather than printed as JSON.
 */
export function resolvedConfigRows(config: FactoryConfig): ConfigSummaryRow[] {
  const checks = config.expectedChecks ?? []
  const acknowledged = config.noRequiredCiAcknowledged
  return [
    { label: 'Repository', value: config.repoId || 'Not set' },
    { label: 'Project', value: config.projectRoot || 'Not set' },
    { label: 'Target branch', value: config.targetBranch || 'Not set' },
    { label: 'Host', value: config.hostId || 'local' },
    { label: 'Environment', value: config.native ? 'Native' : config.workspace || 'No workspace' },
    { label: 'Implementer', value: profileOf(config.implementerRouting) || 'Not set' },
    { label: 'Reviewer', value: profileOf(config.reviewerRouting) || 'Not set' },
    { label: 'Reviewer instructions', value: config.reviewerInstructions || 'None' },
    {
      label: 'Expected checks',
      value: checks.length ? checks.map(formatFactoryCheck).join(', ') : 'None',
      // An empty list is only a decision when the handoff acknowledged it.
      source: !checks.length && acknowledged ? 'No required CI acknowledged' : undefined,
      warning: !checks.length && !acknowledged,
    },
    { label: 'Auto permission', value: config.implementerAuto ? 'Automatic' : 'Asks before each action' },
    { label: 'Total rounds', value: String(config.maxRounds) },
    { label: 'CI poll / wait', value: `${config.ciPollSeconds}s / ${config.ciWaitSeconds}s` },
  ]
}
