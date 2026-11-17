import type { FactoryCheckKey, FactoryConfig, FactorySubagentOverrides, FactorySubagentSettings, SubagentRoleView, SubagentSource } from '../factory'
import { subagentValueLine } from '../factory'

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

/** How one source reads on a surface, per the surface's own words. */
export interface SubagentSourceLabels {
  item: string
  project: string
  runtime: string
  unset: string
}

/** A saved draft's review: its own overrides, then the project's defaults, then the session's. */
export const DRAFT_SUBAGENT_SOURCES: SubagentSourceLabels = {
  item: "This draft's own setting",
  project: 'Inherited from the project factory defaults',
  runtime: "Inherited from the session's reported defaults",
  unset: "Inherited from the session's reported defaults, which this screen cannot read",
}

/** A project's own record: only the session's defaults are beyond its reach. */
export const PROJECT_SUBAGENT_SOURCES: SubagentSourceLabels = {
  item: 'Your project setting',
  project: 'Your project setting',
  runtime: "Inherited from the session's reported defaults",
  unset: "Inherited from the session's reported defaults, which this screen cannot read",
}

/** Where one field of one role came from, worded for the surface showing it. */
export function describeSubagentSource(source: SubagentSource, labels: SubagentSourceLabels = DRAFT_SUBAGENT_SOURCES): string {
  return labels[source]
}

/**
 * The two roles' effective settings as summary rows. The value is the resolved
 * line and the source says where each row's fields came from, so a reader sees
 * both without the surface resolving a fallback of its own.
 */
export function subagentSummaryRows(
  views: SubagentRoleView[],
  labels: SubagentSourceLabels = DRAFT_SUBAGENT_SOURCES,
): ConfigSummaryRow[] {
  return views.map((view) => ({
    label: view.label,
    value: subagentValueLine(view),
    source: describeSubagentSource(view.inherited ? (view.fields[0]?.source ?? 'unset') : 'item', labels),
  }))
}

/**
 * An approval's frozen settings as readable text. A revision approved before
 * these were frozen carries the zero value, and a zero concurrency is that
 * marker — it is never shown as a cap of zero.
 */
export function frozenSubagentText(settings: FactorySubagentSettings | undefined): string {
  if (!settings || settings.concurrency <= 0) return 'Not recorded — this revision was approved before subagent settings were frozen'
  const parts = [settings.enabled ? 'On' : 'Off', settings.model || 'Normal child routing', `${settings.concurrency} at a time`]
  return parts.join(' · ')
}

/** One row per role for an approved revision, so a later default change is visibly not applied to it. */
export function frozenSubagentRows(
  approval: { implementerSubagents?: FactorySubagentSettings; reviewerSubagents?: FactorySubagentSettings } | null | undefined,
): ConfigSummaryRow[] {
  return [
    { label: 'Implementer subagents', value: frozenSubagentText(approval?.implementerSubagents) },
    { label: 'Reviewer subagents', value: frozenSubagentText(approval?.reviewerSubagents) },
  ]
}

/**
 * What an explicit override needs from the backend. A session that reports no
 * per-session settings support still lets a draft be written; the approval is
 * what is refused, so this belongs beside the approval and never as a silent
 * disabled control.
 *
 * `undefined` or null capabilities mean the caller does not know, and nothing
 * is claimed either way.
 */
export function subagentUnsupportedNotice(capabilities: { settings: boolean } | null | undefined): string {
  if (!capabilities || capabilities.settings) return ''
  return 'This backend reports no per-session subagent settings, so an override here would be refused at approval rather than applied. Clear the overrides to inherit what the session does apply.'
}

/** True when the version a review displayed is no longer the current one. */
export function settingsVersionMoved(reviewed: string, current: string): boolean {
  return !!reviewed && !!current && reviewed !== current
}

/**
 * A settings-version conflict, as the person reading the review needs it: the
 * two versions and the one way out. It is never worded as a generic failure,
 * because nothing failed — the settings the review would freeze moved.
 */
export function subagentConflictNotice(reviewed: string, current: string): string {
  return `The settings moved after this review was opened (reviewed ${reviewed || 'unversioned'}, now ${current || 'unversioned'}). Reload to review the current settings; approving this page would freeze settings nobody read.`
}

/** The value a "follow" choice carries in a control, distinct from every stated value. */
export const SUBAGENT_INHERIT = 'inherit'

/** The tri-state an enabled control shows: absent is inherit, and `false` is stated, not missing. */
export type SubagentEnabledChoice = 'inherit' | 'on' | 'off'

export function enabledChoice(o: FactorySubagentOverrides | null | undefined): SubagentEnabledChoice {
  if (!o || o.enabled === undefined) return 'inherit'
  return o.enabled ? 'on' : 'off'
}

/** A copy of the overrides with only this field changed; `inherit` deletes it. */
export function applyEnabledChoice(o: FactorySubagentOverrides | null | undefined, choice: SubagentEnabledChoice): FactorySubagentOverrides {
  const next: FactorySubagentOverrides = { ...(o ?? {}) }
  if (choice === 'inherit') delete next.enabled
  else next.enabled = choice === 'on'
  return next
}

/** A model control's value: `inherit`, `''` for normal child routing, or a profile name. */
export function modelChoice(o: FactorySubagentOverrides | null | undefined): string {
  if (!o || o.model === undefined) return SUBAGENT_INHERIT
  return o.model
}

export function applyModelChoice(o: FactorySubagentOverrides | null | undefined, choice: string): FactorySubagentOverrides {
  const next: FactorySubagentOverrides = { ...(o ?? {}) }
  // An explicit empty choice is not "follow": it is normal child routing.
  if (choice === SUBAGENT_INHERIT) delete next.model
  else next.model = choice
  return next
}

/** A concurrency control's value: `inherit`, or the stated count as text. */
export function concurrencyChoice(o: FactorySubagentOverrides | null | undefined): string {
  return o?.concurrency === undefined ? SUBAGENT_INHERIT : String(o.concurrency)
}

/** A concurrency text control's value: blank follows, a count is stated as written. */
export function concurrencyText(o: FactorySubagentOverrides | null | undefined): string {
  return o?.concurrency === undefined ? '' : String(o.concurrency)
}

/**
 * A concurrency control's value as an override. A blank or `inherit` choice
 * follows; a stated count is kept exactly as written, because the backend
 * refuses an out-of-range cap at approval and nothing here may clamp it.
 */
export function applyConcurrencyChoice(o: FactorySubagentOverrides | null | undefined, choice: string): FactorySubagentOverrides {
  const next: FactorySubagentOverrides = { ...(o ?? {}) }
  if (choice === SUBAGENT_INHERIT || choice.trim() === '') delete next.concurrency
  else {
    const n = Number(choice)
    if (!Number.isInteger(n)) throw new Error('Subagent concurrency must be a whole number; nothing was saved for it.')
    next.concurrency = n
  }
  return next
}

/** The overrides a save writes: unset fields are absent, so a default can still reach them. */
export function subagentOverridesBody(
  enabled: SubagentEnabledChoice,
  model: string,
  concurrency: string,
): FactorySubagentOverrides | undefined {
  let o = applyEnabledChoice(undefined, enabled)
  o = applyModelChoice(o, model)
  o = applyConcurrencyChoice(o, concurrency)
  return Object.keys(o).length ? o : undefined
}

/**
 * The overrides a surface's three controls describe right now. It is the same
 * body a save writes, with an unparsable cap left out of the description
 * instead of clamped into it.
 */
export function subagentOverridesOfControls(
  enabled: SubagentEnabledChoice,
  model: string,
  concurrency: string,
): FactorySubagentOverrides {
  try {
    return subagentOverridesBody(enabled, model, concurrency) ?? {}
  } catch {
    return subagentOverridesBody(enabled, model, '') ?? {}
  }
}

/** One role's three fields and their sources as a single readable line. */
export function subagentFieldLines(view: SubagentRoleView, labels: SubagentSourceLabels = DRAFT_SUBAGENT_SOURCES): string {
  return view.fields.map((f) => `${f.label}: ${f.value} — ${describeSubagentSource(f.source, labels)}`).join(' · ')
}
