<script lang="ts">
  import { onMount } from 'svelte'
  import PendingActions from '../inbox/PendingActions.svelte'
  import Tag from '../ui/Tag.svelte'
  import { describePending, type AgentRow } from '../fleet'
  import { connectSSE } from '../sse'
  import { createStackStore, type StackState, type StackStore } from '../stack'
  import { gateState } from '../dock/gate'
  import { orderedNodes } from '../dock/nodes'
  import { compactDuration } from '../transcript/format'
  import { acquireSlot, onSlotFree, releaseSlot } from './wall'
  import { summarize } from '../runs/model'
  import { shortName } from '../utils'

  let {
    agent,
    onRefreshPending,
    onNavigate,
  }: {
    agent: AgentRow
    onRefreshPending: () => void
    onNavigate: (hash: string) => void
  } = $props()

  /*
    The wall can hold dozens of agents but only the tiles in view keep a
    stack store and a session stream (design §8.3). Entering the viewport
    builds both; leaving drops them, so the cost follows what is on screen.
  */
  let el = $state<HTMLElement | null>(null)
  let snap = $state<StackState | null>(null)
  let store: StackStore | null = null
  let unsubscribe: (() => void) | undefined
  let stopSSE: (() => void) | undefined
  let now = $state(Date.now())

  // In view and wanting a stream; it only gets one while a slot is free (see wall.ts).
  let visible = false

  function attach() {
    visible = true
    if (store) return
    // svelte-ignore state_referenced_locally
    const id = agent.id
    if (!acquireSlot(id)) return
    const s = createStackStore(id)
    store = s
    unsubscribe = s.subscribe((v) => (snap = v))
    void s.load()
    stopSSE = connectSSE({
      sessionId: id,
      onEvent: (e) => {
        if (e.type === 'connected') s.onEvent({ type: 'connected' })
        else if (e.type === 'message') {
          try {
            s.onEvent(JSON.parse(e.message.data))
          } catch {
            // Skipped; the next patch or snapshot catches up.
          }
        }
      },
    })
  }

  function detach() {
    visible = false
    release()
  }

  function release() {
    if (store) releaseSlot(agent.id)
    stopSSE?.()
    unsubscribe?.()
    store?.destroy()
    stopSSE = unsubscribe = undefined
    store = null
    snap = null
  }

  onMount(() => {
    // A slot freed elsewhere goes to the first waiting tile that is in view.
    const stopWaiting = onSlotFree(() => visible && !store && attach())
    if (!el || typeof IntersectionObserver === 'undefined') {
      attach()
      return () => {
        stopWaiting()
        detach()
      }
    }
    const io = new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (e.isIntersecting) attach()
        else detach()
      }
    })
    io.observe(el)
    return () => {
      stopWaiting()
      io.disconnect()
      detach()
    }
  })

  const running = $derived(agent.status === 'running')
  $effect(() => {
    if (!running) return
    now = Date.now()
    const t = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(t)
  })

  const nodes = $derived(snap?.status === 'ready' ? orderedNodes(snap) : [])
  const latestTurn = $derived(snap ? [...snap.roots].reverse().find((r) => snap!.nodes.get(r)?.kind === 'turn') : undefined)
  const segments = $derived(
    latestTurn && snap ? orderedNodes({ nodes: snap.nodes, roots: [latestTurn] }).filter((n) => n.task) : [],
  )
  const steps = $derived(nodes.filter((n) => n.kind === 'step' && n.step))
  const lastSteps = $derived(steps.slice(-3))
  const model = $derived(steps.at(-1)?.step?.model)
  const gate = $derived(gateState(agent.gate))
  const summary = $derived(summarize(agent.run, !!agent.pending))

  const started = $derived(summary?.startedAt ?? (agent.updatedAt ? new Date(agent.updatedAt).getTime() : NaN))
  const elapsed = $derived(Number.isFinite(started) ? compactDuration(Math.max(0, now - started)) : '')

  const segTone = (t: NonNullable<(typeof segments)[number]['task']>) =>
    t.unresolvedFailure ? 'bg-err' : t.status === 'completed' ? 'bg-ok' : t.status === 'in_progress' ? 'bg-accent' : 'bg-line'

  const open = () => onNavigate(`#chat/${agent.id}?dock=collapsed`)
  // Controls and dialogs keep their own clicks; anywhere else opens the chat.
  function onClick(e: MouseEvent) {
    if ((e.target as Element).closest('button, a, input, textarea, select, [role="dialog"]')) return
    open()
  }
</script>

<!-- The title is a real button so the tile is reachable by keyboard; the card click is the pointer shortcut. -->
<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
<article
  bind:this={el}
  class="flex min-h-40 cursor-pointer flex-col gap-2 rounded-lg border p-3 hover:bg-hover {agent.pending ? 'border-warn bg-warn/10' : 'border-border bg-surface'}"
  data-testid="tile"
  data-agent={agent.id}
  onclick={onClick}
>
  <header class="flex items-center gap-2">
    <button class="min-w-0 cursor-pointer truncate text-left text-sm font-medium" onclick={open}>{agent.name || agent.id}</button>
    <span class="truncate text-xs text-muted">· {shortName(agent.project)}</span>
    <span class="flex-1"></span>
    <Tag tone={running ? 'ok' : agent.status === 'error' ? 'err' : 'neutral'}>{agent.status}</Tag>
    <span class="font-mono text-xs text-muted">{elapsed}</span>
  </header>

  {#if segments.length > 0}
    <div class="flex gap-0.5" role="img" aria-label="Task progress" data-testid="segments">
      {#each segments as n (n.id)}
        <span class="h-1.5 flex-1 rounded-full {segTone(n.task!)}" title={n.task!.content}></span>
      {/each}
    </div>
  {/if}

  <ul class="flex flex-col gap-0.5 text-xs text-sub" data-testid="tile-steps">
    {#each lastSteps as n (n.id)}
      <li class="truncate">{n.step!.headline}</li>
    {:else}
      <li class="truncate text-muted">{agent.activity || (snap ? 'No steps yet' : '')}</li>
    {/each}
  </ul>

  <div class="mt-auto flex flex-wrap items-center gap-1.5">
    {#if model}<Tag tone="neutral">{model}</Tag>{/if}
    {#if gate !== 'none'}<Tag tone={gate === 'failed' ? 'err' : gate === 'passed' ? 'ok' : 'warn'}>gate {gate}</Tag>{/if}
    {#if summary}<Tag tone="info">{summary.kind === 'sdd' ? `${summary.done}/${summary.total} tasks` : 'swarm'}</Tag>{/if}
  </div>

  {#if agent.pending}
    <div class="flex flex-wrap items-center gap-2 border-t border-warn/30 pt-2">
      <div class="min-w-0 flex-1 truncate text-xs" title={describePending(agent.pending)}>{describePending(agent.pending)}</div>
      <PendingActions {agent} onResolved={onRefreshPending} onOpen={open} />
    </div>
  {/if}
</article>
