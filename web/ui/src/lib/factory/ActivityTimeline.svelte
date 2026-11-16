<script lang="ts">
  import Button from '../ui/Button.svelte'
  import Card from '../ui/Card.svelte'
  import Tag from '../ui/Tag.svelte'
  import { sortedFactoryHistory } from '../studio/model'
  import { provenanceLine, type FactoryEvent, type FactoryItem } from '../factory'

  /**
   * One work item's factory activity (design C5): every recorded event, oldest
   * first, with the worker role it ran as where the item can map it.
   *
   * It renders `item` and nothing else: no poller, no session stream and no
   * merged transcript — events the factory cannot map to an effect stay
   * factory-level rather than being attributed to a worker. The list is not a
   * progress bar either: it shows the events that happened, never a percentage.
   *
   * The newest event is where the reader starts and stays while they are
   * there. A reader who scrolled back keeps their place, and a new event then
   * waits behind a cue instead of yanking the list out from under them.
   *
   * `hidden` conceals the pane without unmounting it, so scroll position and
   * disclosures survive a drill-in and come back with the reader.
   */
  export interface ActivityTimelineProps {
    item: FactoryItem
    hidden?: boolean
    /** Client names for provenance lines; a missing name falls back to the stable ID. */
    clientNames?: Record<string, string>
  }

  let { item, hidden = false, clientNames = {} }: ActivityTimelineProps = $props()

  const events = $derived(sortedFactoryHistory(item))

  /**
   * The role each recorded effect ran as. A worker names the effect that
   * spawned it, and the effect itself names its role; both are real
   * server-reported facts, so either can label the event.
   */
  function roleByEffect(subject: FactoryItem): Map<string, string> {
    const roles = new Map<string, string>()
    for (const worker of subject.workers ?? []) if (worker.spawnEffectId && worker.role) roles.set(worker.spawnEffectId, worker.role)
    for (const effect of subject.effects ?? []) {
      const role = effect.workerRole || effect.worker?.role
      if (effect.id && role) roles.set(effect.id, role)
    }
    return roles
  }

  const roles = $derived(roleByEffect(item))
  const roleOf = (event: FactoryEvent) => roles.get(event.effectId) ?? 'Factory'

  let viewport = $state<HTMLDivElement | null>(null)
  let atLatest = $state(true)
  /** Events the reader had already reached the end for. */
  let seen = $state(0)
  /** The reader's place while they are not at the latest event. */
  let savedTop = 0

  // The end of the list is within a line of the bottom, so a rounding scroll
  // never reads as "the reader scrolled away".
  const atEnd = (list: HTMLDivElement) => list.scrollHeight - list.scrollTop - list.clientHeight <= 8

  function onScroll() {
    if (!viewport) return
    savedTop = viewport.scrollTop
    atLatest = atEnd(viewport)
  }

  function jumpToLatest() {
    atLatest = true
    seen = events.length
    savedTop = 0
    if (viewport) viewport.scrollTop = viewport.scrollHeight
  }

  $effect(() => {
    const count = events.length
    // Reaching the latest event is what marks events as seen; a reader who is
    // behind keeps their count, so the cue only appears for genuinely new
    // activity.
    if (atLatest) seen = count
    if (hidden || !viewport) return
    viewport.scrollTop = atLatest ? viewport.scrollHeight : savedTop
  })
</script>

<Card class={hidden ? 'hidden' : 'grid min-w-0 gap-2'} data-testid="activity-timeline" aria-hidden={hidden}>
  <div class="flex flex-wrap items-center justify-between gap-2">
    <h2 class="font-semibold">Activity</h2>
    {#if !atLatest && events.length > seen}
      <Button variant="ghost" onclick={jumpToLatest}>New activity</Button>
    {/if}
  </div>
  {#if !events.length}
    <p class="text-sm text-muted">No activity yet.</p>
  {:else}
    <div class="max-h-[36rem] overflow-y-auto" bind:this={viewport} onscroll={onScroll} data-testid="activity-scroll">
      <ol class="grid gap-3 text-sm">
        {#each events as event}
          <li class="min-w-0 border-b border-border pb-2" data-testid="activity-event">
            <div class="flex flex-wrap items-center gap-2">
              <time class="text-xs text-muted" datetime={event.at}>{event.at}</time>
              <Tag role={roleOf(event)} />
            </div>
            <p class="whitespace-pre-wrap break-words">{event.message}</p>
            {#if event.provenance}<p class="mt-1 break-words text-xs text-muted" data-testid="factory-provenance">{provenanceLine(event.provenance, clientNames[event.provenance.clientId])}</p>{/if}
          </li>
        {/each}
      </ol>
    </div>
  {/if}
</Card>
