<script lang="ts">
  import Badge from '../ui/Badge.svelte'
  import Card from '../ui/Card.svelte'
  import type { ProjectSettings } from '../api'
  import { formatFactoryCheck } from '../factory/config'
  import { formatProjectRoute } from '../routes'

  /**
   * A project's factory defaults, read-only (design C5). The rows say what a
   * new handoff in this project would start with and where each value came
   * from, using the same vocabulary the settings editor uses: a field the
   * factory chose itself, or one that follows the project's agent defaults.
   *
   * It renders and never fetches. `Factory.svelte` owns the read and its
   * failure state, so a bridge that cannot answer still shows the work items.
   *
   * The check text is the editor's own `kind:name[:appId]` syntax, rendered by
   * the shared `formatFactoryCheck` so this summary and the handoff's never
   * drift into two vocabularies.
   */
  export interface FactoryDefaultsSummaryProps {
    root: string
    settings: ProjectSettings
    onNavigate: (hash: string) => void
  }

  let { root, settings, onNavigate }: FactoryDefaultsSummaryProps = $props()

  const defaults = $derived(settings.factoryDefaults ?? {})

  const profileOf = (route: { profile?: unknown } | undefined): string => (typeof route?.profile === 'string' ? route.profile : '')

  /** An explicit factory choice, or the project default it inherits. */
  const inherited = (own: string, fallback: string): string =>
    own ? own : `Following the project default: ${fallback || 'Not configured'}`

  const checksText = $derived(
    defaults.expectedChecks?.length ? defaults.expectedChecks.map(formatFactoryCheck).join(', ') : 'Follows the handoff',
  )

  const ciText = $derived.by(() => {
    const parts: string[] = []
    if (defaults.ciPollSeconds) parts.push(`polls every ${defaults.ciPollSeconds}s`)
    if (defaults.ciWaitSeconds) parts.push(`waits up to ${defaults.ciWaitSeconds}s`)
    return parts.length ? parts.join(' · ') : 'Follows the handoff'
  })

  const rows = $derived([
    { id: 'workspace', label: 'Workspace', value: inherited(defaults.workspace ?? '', settings.workspace ?? '') },
    { id: 'implementer', label: 'Implementer', value: inherited(profileOf(defaults.implementerRouting), profileOf(settings.routing)) },
    { id: 'reviewer', label: 'Reviewer', value: inherited(profileOf(defaults.reviewerRouting), profileOf(settings.routing)) },
    { id: 'target-branch', label: 'Target branch', value: defaults.targetBranch || 'Follows the handoff' },
    { id: 'checks', label: 'Expected checks', value: checksText },
    { id: 'review-instructions', label: 'Review instructions', value: defaults.reviewerInstructions || 'None' },
    { id: 'rounds', label: 'Rounds', value: defaults.maxRounds ? String(defaults.maxRounds) : 'Follows the handoff' },
    { id: 'ci-timing', label: 'CI timing', value: ciText },
  ])

  const settingsHash = $derived(formatProjectRoute(root))
  const workflowHash = '#roadmap/workflow'
</script>

<Card class="flex flex-col gap-3" data-testid="factory-defaults">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <h2 class="text-sm font-semibold">Factory defaults</h2>
    <div class="flex flex-wrap items-center gap-2 text-xs">
      <a class="text-accent hover:underline" href={settingsHash} onclick={(e) => { e.preventDefault(); onNavigate(settingsHash) }}>Customize</a>
      <a class="flex items-center gap-1 text-accent hover:underline" href={workflowHash} onclick={(e) => { e.preventDefault(); onNavigate(workflowHash) }}>
        Workflow editor <Badge tone="attention">Planned</Badge>
      </a>
    </div>
  </div>
  <dl class="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[auto_1fr]">
    {#each rows as row (row.id)}
      <dt class="text-xs text-muted sm:text-sm">{row.label}</dt>
      <dd class="min-w-0 break-words" data-testid={'defaults-' + row.id}>{row.value}</dd>
    {/each}
  </dl>
</Card>
