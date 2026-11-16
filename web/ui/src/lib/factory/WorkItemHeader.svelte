<script lang="ts">
  import Button from '../ui/Button.svelte'
  import Card from '../ui/Card.svelte'
  import PageHeader, { type Breadcrumb } from '../ui/PageHeader.svelte'
  import Tabs from '../ui/Tabs.svelte'
  import Tag from '../ui/Tag.svelte'
  import { formatFactoryRoute } from '../routes'
  import type { FactoryItem, FactoryItemView } from '../factory'

  /**
   * One work item's header (design C5): where the item sits, the state, stage
   * and round it actually reports, the views it opens as, and the one
   * secondary action that changes nothing about what the work is.
   *
   * Approval is a draft's view: once a person approved, that form would be an
   * empty confirmation, so the tab is offered only while there is a draft to
   * approve — and it stays while it is the selected view, so a state change
   * under the reader cannot leave the tab strip with nothing selected.
   *
   * Every tab is a `formatFactoryRoute` hash, so a view is always a route the
   * shell can restore, and the header itself decides no policy beyond which
   * tabs exist.
   */
  export interface WorkItemHeaderProps {
    item: FactoryItem
    view: FactoryItemView
    onNavigate: (hash: string) => void
    onStop: () => void
    stopping?: boolean
  }

  let { item, view, onNavigate, onStop, stopping = false }: WorkItemHeaderProps = $props()

  const VIEWS: { value: FactoryItemView; label: string }[] = [
    { value: 'approval', label: 'Approval' },
    { value: 'activity', label: 'Activity' },
    { value: 'review', label: 'Review' },
    { value: 'prs', label: 'Pull requests' },
    { value: 'documents', label: 'Documents' },
  ]

  const tabs = $derived(VIEWS.filter((tab) => tab.value === view || tab.value !== 'approval' || item.state === 'draft'))
  const crumbs = $derived<Breadcrumb[]>([
    { label: 'Factory', hash: formatFactoryRoute({ projectRoot: item.projectRoot }) },
    { label: item.title },
  ])
  const subtitle = $derived(`${item.repoId} · ${item.branch || item.draft?.config?.targetBranch || ''}`)
  const rounds = $derived(item.rounds?.length ?? 0)
  const roundText = $derived(
    rounds === 0 ? '' : item.draft?.config?.maxRounds ? `Round ${rounds} of ${item.draft.config.maxRounds}` : `Round ${rounds}`,
  )
  // Draft asks for approval; a stop is only meaningful for work that is live.
  const stoppable = $derived(!['closed', 'stopped', 'stopping', 'draft'].includes(item.state))

  function open(next: string) {
    onNavigate(formatFactoryRoute({ itemId: item.id, view: next as FactoryItemView }))
  }
</script>

<div class="grid gap-3" data-testid="work-item-header">
  <PageHeader breadcrumbs={crumbs} title={item.title} subtitle={subtitle} {onNavigate}>
    {#snippet status()}
      <div class="flex flex-wrap items-center gap-2">
        <Tag tone={item.decision ? 'warn' : 'neutral'}>{item.state}</Tag>
        {#if item.stage}<span class="text-sm">{item.stage}</span>{/if}
        {#if roundText}<span class="text-sm text-muted">{roundText}</span>{/if}
      </div>
    {/snippet}
    {#snippet actions()}
      {#if stoppable}
        <Button variant="ghost" disabled={stopping} onclick={onStop}>Request stop</Button>
      {/if}
    {/snippet}
  </PageHeader>
  <Tabs tabs={tabs} value={view} onchange={open} label="Work item views" />
  {#if item.state === 'stopping'}
    <Card role="status" class="text-sm">Stop requested. Worker shutdown is not yet confirmed.</Card>
  {/if}
</div>
