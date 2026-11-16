<script lang="ts">
  import Card from '../ui/Card.svelte'
  import Badge from '../ui/Badge.svelte'
  import Button from '../ui/Button.svelte'
  import type { ProjectStatus } from '../api'
  import { formatFactoryRoute } from '../routes'
  import { shortName } from '../utils'
  import type { FactoryProjectSummary } from './model'

  /**
   * One project's factory summary: what the project holds, what is waiting on
   * a person, what is ready, and how many agents its current work uses.
   *
   * A root the bridge does not list as a project still gets a card: factory
   * work can exist for a directory that was never registered, and dropping it
   * would hide that work. The card says the project is not registered instead
   * of claiming it is unavailable — nobody checked, so nothing is known to be
   * wrong with it.
   */
  export interface ProjectFactoryCardProps {
    /** The bridge's project row, absent for a factory-only root. */
    project?: ProjectStatus
    summary: FactoryProjectSummary
    onNavigate: (hash: string) => void
  }

  let { project, summary, onNavigate }: ProjectFactoryCardProps = $props()

  const name = $derived(shortName(summary.root))
  const linked = $derived(summary.linkedAgentIds.length)
  const factoryHash = $derived(formatFactoryRoute({ projectRoot: summary.root }))
  const newItemHash = $derived(formatFactoryRoute({ create: true, projectRoot: summary.root }))
  // The new-agent page's own route; a factory-only root cannot start an agent.
  const newAgentHash = $derived(`#new?project=${encodeURIComponent(summary.root)}`)
  const counts = $derived([
    { label: 'current', value: summary.current },
    { label: 'needs you', value: summary.needsYou },
    { label: 'ready', value: summary.ready },
    { label: linked === 1 ? 'linked agent' : 'linked agents', value: linked },
  ])
</script>

<Card class="flex flex-col gap-3" data-testid="project-factory-card">
  <div class="flex items-start justify-between gap-3">
    <div class="min-w-0">
      <div class="break-words font-medium" title={summary.root}>{name}</div>
      <div class="break-words text-xs text-muted" title={summary.root}>{summary.root}</div>
    </div>
    {#if !project}
      <Badge tone="muted">Project not registered</Badge>
    {/if}
  </div>

  <!-- Every count is shown, so a project with nothing in flight reads as empty rather than blank. -->
  <dl class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted">
    {#each counts as c (c.label)}
      <div class="flex gap-1">
        <dt>{c.label}</dt>
        <dd class="font-medium text-fg" data-testid={'count-' + c.label.replace(' ', '-')}>{c.value}</dd>
      </div>
    {/each}
  </dl>

  <div class="flex flex-wrap items-center gap-2">
    <Button variant="ghost" onclick={() => onNavigate(factoryHash)}>Open factory</Button>
    {#if project}
      <Button variant="ghost" onclick={() => onNavigate(newItemHash)}>New work item</Button>
      <Button variant="ghost" onclick={() => onNavigate(newAgentHash)}>Start agent</Button>
    {/if}
  </div>
</Card>
