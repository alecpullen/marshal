<script lang="ts">
  import Badge from '../ui/Badge.svelte'
  import Button from '../ui/Button.svelte'
  import Card from '../ui/Card.svelte'
  import type { FactoryItem } from '../factory'
  import { factoryItemRoute, factoryWorkerIds, isFactoryActive, partitionFactoryChecks } from './model'

  /**
   * One factory work item on the project dashboard (design C5): its title and
   * repository, the backend state and stage it actually reports, how many pull
   * requests it has, how the latest round's checks stand, how many agents it
   * holds, and the one action that moves it forward.
   *
   * Everything shown is server-reported: no percentage estimate and no
   * workflow strip. A state this build does not know is shown verbatim rather
   * than mapped onto one it does, and a long title wraps instead of being
   * clipped, because the card must never hide what the work is.
   *
   * The card navigates and renders only: the route comes from C2
   * `factoryItemRoute`, so the card and the attention lists always agree on
   * where an item opens.
   */
  export interface WorkItemCardProps {
    item: FactoryItem
    onNavigate: (hash: string) => void
  }

  let { item, onNavigate }: WorkItemCardProps = $props()

  const href = $derived(factoryItemRoute(item))
  const checks = $derived(partitionFactoryChecks(item.rounds?.at(-1)))
  const checkTotal = $derived(checks.failed.length + checks.passed.length + checks.other.length)
  // A worker ID held twice is one agent; an empty ID is no agent at all.
  const agentCount = $derived(factoryWorkerIds([item]).size)
  const prCount = $derived(item.prs?.length ?? 0)

  const prText = $derived(prCount === 0 ? 'No PRs' : prCount === 1 ? '1 PR' : `${prCount} PRs`)
  const agentText = $derived(agentCount === 0 ? 'No agents' : agentCount === 1 ? '1 agent' : `${agentCount} agents`)
  const checkText = $derived(
    checkTotal === 0
      ? 'No checks yet'
      : `${checks.failed.length} failed · ${checks.passed.length} passed · ${checks.other.length} other`,
  )

  // A decision or a draft waits on a person; live work is the running state.
  const tone = $derived(item.decision ? 'attention' : isFactoryActive(item) ? 'running' : 'muted')

  // The order matters: a draft's action is approving it, then resolving a
  // blocking decision, then a ready review. Everything else just opens.
  const action = $derived(
    item.state === 'draft'
      ? 'Review draft'
      : item.decision
        ? 'Resolve decision'
        : item.state === 'ready'
          ? 'Review work'
          : 'Open item',
  )
</script>

<Card class="flex flex-col gap-2 p-3" data-testid="work-item-card">
  <div class="flex flex-wrap items-start justify-between gap-2">
    <div class="min-w-0 flex-1">
      <h2 class="break-words text-sm font-medium">{item.title}</h2>
      <p class="break-words text-xs text-muted">{item.repoId}{#if item.branch} · {item.branch}{/if}</p>
    </div>
    <span data-testid="work-item-state"><Badge {tone}>{item.state}</Badge></span>
  </div>

  <!-- Every count is shown, so a quiet item reads as empty rather than blank. -->
  <dl class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted">
    <div class="flex gap-1">
      <dt>stage</dt>
      <dd class="text-fg" data-testid="work-item-stage">{item.stage || 'none'}</dd>
    </div>
    <div class="flex gap-1">
      <dt>pull requests</dt>
      <dd class="text-fg" data-testid="work-item-prs">{prText}</dd>
    </div>
    <div class="flex gap-1">
      <dt>checks</dt>
      <dd class="text-fg" data-testid="work-item-checks">{checkText}</dd>
    </div>
    <div class="flex gap-1">
      <dt>agents</dt>
      <dd class="text-fg" data-testid="work-item-workers">{agentText}</dd>
    </div>
  </dl>

  <div class="flex flex-wrap items-center gap-2">
    {#if item.decision}
      <p class="min-w-0 flex-1 break-words text-xs text-attention">{item.decision.question || 'A decision is waiting on you.'}</p>
    {/if}
    <Button variant="ghost" onclick={() => onNavigate(href)}>{action}</Button>
  </div>
</Card>
