<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Tag from '../lib/ui/Tag.svelte'
  import Segmented from '../lib/ui/Segmented.svelte'
  import Lanes from '../lib/runs/Lanes.svelte'
  import Graph from '../lib/runs/Graph.svelte'
  import Timeline, { type Sample } from '../lib/runs/Timeline.svelte'
  import Dock from '../lib/dock/Dock.svelte'
  import InspectTab from '../lib/dock/InspectTab.svelte'
  import ChangesTab from '../lib/dock/ChangesTab.svelte'
  import FilesTab from '../lib/dock/FilesTab.svelte'
  import Transcript from '../lib/transcript/Transcript.svelte'
  import { createNodeCache } from '../lib/dock/nodeCache'
  import { initialDock, load as loadDock, reduce, save as saveDock, type DockAction, type DockState } from '../lib/dock/dock'
  import { gateState } from '../lib/dock/gate'
  import { nodeLabel } from '../lib/dock/nodes'
  import { answerRun, errMessage, getRoster, getRun, type Roster, type RunDetail, type SDDRun } from '../lib/api'
  import { connectSSE } from '../lib/sse'
  import { createStackStore } from '../lib/stack'
  import { formatRunRoute, type RunRoute, type RunView } from '../lib/routes'
  import type { AgentRow } from '../lib/fleet'
  import { lanes, stageSteps, STAGES, summarize, type StageKey } from '../lib/runs/model'
  import { compactDuration } from '../lib/transcript/format'

  let {
    agentId,
    route,
    agent = undefined,
    onNavigate,
  }: {
    agentId: string
    route: RunRoute
    agent?: AgentRow
    onNavigate: (hash: string) => void
  } = $props()

  // The page is keyed by agent in App, so the id never changes in place.
  // svelte-ignore state_referenced_locally
  const stack = createStackStore(agentId)
  const cache = createNodeCache()

  let run = $state<RunDetail | 'unsupported' | null>(null)
  let loadError = $state('')
  let roster = $state<Roster | null>(null)
  let samples = $state<Sample[]>([])
  let now = $state(Date.now())

  const detail = $derived(typeof run === 'object' && run ? run : null)
  const sdd = $derived<SDDRun | undefined>(detail?.kind === 'sdd' ? detail.sdd : undefined)
  const swarm = $derived(detail?.kind === 'swarm' ? detail.swarm : undefined)
  const summary = $derived(summarize(detail ?? undefined, !!agent?.pending))
  const tokensUsed = $derived(sdd?.tokensUsed ?? swarm?.tokensUsed ?? 0)
  const tokensMax = $derived(sdd?.tokensMax ?? swarm?.tokensMax ?? 0)

  function setRun(r: RunDetail) {
    run = r
    const tokens = r.sdd?.tokensUsed ?? r.swarm?.tokensUsed ?? 0
    // One point per change: the curve only needs where the total moved.
    if (samples.at(-1)?.tokens !== tokens) samples = [...samples, { t: Date.now(), tokens }]
  }

  // Fleet deltas carry the latest digest; the first fetch is only the seed.
  $effect(() => {
    const r = agent?.run
    if (r) untrack(() => setRun(r))
  })

  const live = $derived(!!summary?.running || [...$stack.nodes.values()].some((n) => n.live))
  $effect(() => {
    if (!live) return
    now = Date.now()
    const t = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(t)
  })

  let stopSSE: (() => void) | undefined
  onMount(() => {
    void stack.load()
    // One stream carries every patch; the store keeps the ones it needs.
    stopSSE = connectSSE({
      sessionId: agentId,
      onEvent: (e) => {
        if (e.type === 'connected') stack.onEvent({ type: 'connected' })
        else if (e.type === 'message') {
          try {
            stack.onEvent(JSON.parse(e.message.data))
          } catch {
            // A malformed event is skipped; the next patch or snapshot catches up.
          }
        }
      },
    })
    getRun(agentId)
      .then((r) => {
        // A delta that landed first is newer than this fetch.
        if (r === 'unsupported') run = run ?? r
        else if (!run || run === 'unsupported') setRun(r)
      })
      .catch((e) => (loadError = errMessage(e)))
    getRoster(agentId)
      .then((r) => r !== 'unsupported' && (roster = r))
      .catch(() => {
        // The legend is optional.
      })
  })
  onDestroy(() => {
    stack.destroy()
    stopSSE?.()
  })

  // ---- selection and dock ----
  const sel = $derived.by(() => {
    const m = /^(\d+):(implement|verify|review|commit)$/.exec(route.node ?? '')
    return m ? { task: Number(m[1]), stage: m[2] as StageKey } : null
  })
  const selTask = $derived(sel ? sdd?.tasks.find((t) => t.n === sel.task) : undefined)
  const steps = $derived(sel && selTask ? stageSteps($stack.nodes, selTask, sel.stage, now) : [])
  const stepsKey = $derived(steps.join(','))

  // svelte-ignore state_referenced_locally
  let dock = $state<DockState>(initialDock({ ...loadDock(), ...(route.dock ? { size: route.dock } : {}) }))
  function dispatch(a: DockAction) {
    dock = reduce(dock, a)
    saveDock(dock)
  }

  // Inspect follows the stage: the first matching step, unless a chosen one still matches.
  $effect(() => {
    const ids = stepsKey ? stepsKey.split(',') : []
    untrack(() => {
      if (ids.length === 0) {
        if (dock.mode === 'select') dispatch({ type: 'backToLive' })
      } else if (!dock.selected || !ids.includes(dock.selected)) {
        dispatch({ type: 'select', nodeId: ids[0], kind: 'step', reveal: false })
      }
    })
  })

  const go = (over: Partial<RunRoute>) => onNavigate(formatRunRoute({ id: agentId, view: route.view, node: route.node, dock: dock.size, ...over }))
  function select(s: { task: number; stage: StageKey }) {
    if (dock.size === 'collapsed') dispatch({ type: 'setSize', size: 'docked' })
    go({ node: `${s.task}:${s.stage}` })
  }
  const setView = (v: string) => go({ view: v as RunView })

  // Keep the URL in step with the dock size without growing the history.
  $effect(() => {
    const next = formatRunRoute({ id: agentId, view: route.view, node: route.node, dock: dock.size })
    try {
      if (location.hash !== next) history.replaceState(null, '', next)
    } catch {
      // A sandboxed frame may refuse; the URL just stops tracking.
    }
  })

  const selectedLabel = $derived(dock.selected ? nodeLabel($stack.nodes.get(dock.selected), dock.selected) : '')
  const gateFailed = $derived(gateState(agent?.gate) === 'failed')

  // ---- gate answer ----
  let answer = $state('')
  let answering = $state(false)
  let answerError = $state('')
  async function submitAnswer() {
    const text = answer.trim()
    if (!text || answering) return
    answering = true
    answerError = ''
    try {
      await answerRun(agentId, text)
      answer = ''
    } catch (e) {
      answerError = errMessage(e)
    } finally {
      answering = false
    }
  }

  const elapsed = $derived(summary?.startedAt ? compactDuration(Math.max(0, (sdd?.endedAt || now) - summary.startedAt)) : '')
  const stageLabel = (k: string) => k[0].toUpperCase() + k.slice(1)
</script>

<div class="flex h-full min-h-0">
  <div class="min-w-0 flex-1 overflow-y-auto">
    <div class="mx-auto flex max-w-6xl flex-col gap-4 p-6">
      <header class="flex flex-col gap-2">
        <button class="w-fit cursor-pointer text-xs text-muted hover:text-fg" onclick={() => onNavigate('#runs')}>← Runs</button>
        {#if run === 'unsupported'}
          <Card class="text-sm">
            Run detail needs a newer agent.
            <button class="cursor-pointer text-accent underline" onclick={() => onNavigate(`#chat/${agentId}`)}>Open the session</button>
          </Card>
        {:else if loadError}
          <Card class="border-attention text-sm">{loadError}</Card>
        {:else if !detail}
          <p class="text-sm text-muted">Loading run…</p>
        {:else if detail.kind === 'none'}
          <Card class="text-sm">
            This agent has no run.
            <button class="cursor-pointer text-accent underline" onclick={() => onNavigate(`#chat/${agentId}`)}>Open the session</button>
          </Card>
        {:else if summary}
          <div class="flex flex-wrap items-center gap-2" data-testid="run-header">
            <h1 class="text-lg font-semibold">{summary.title}</h1>
            <Tag tone={summary.running ? 'ok' : summary.phase === 'failed' ? 'err' : 'neutral'}>{summary.phase || (summary.running ? 'running' : 'finished')}</Tag>
            {#if sdd?.branch}<span class="font-mono text-xs text-muted">⎇ {sdd.branch}</span>{/if}
            <span class="flex-1"></span>
            <span class="font-mono text-xs text-muted">{summary.done}/{summary.total} {summary.kind === 'sdd' ? 'tasks' : 'roles'}</span>
            <span class="font-mono text-xs text-muted">{tokensUsed.toLocaleString()}{tokensMax ? ` / ${tokensMax.toLocaleString()}` : ''} tokens</span>
            {#if elapsed}<span class="font-mono text-xs text-muted">{elapsed}</span>{/if}
          </div>
          {#if sdd?.detail}<p class="text-sm text-muted">{sdd.detail}</p>{/if}
          {#if sdd?.error}<Card class="border-err text-sm text-err">{sdd.error}</Card>{/if}
        {/if}
      </header>

      {#if sdd?.gate}
        <Card class="flex flex-col gap-2 border-attention" data-testid="gate">
          <div class="text-xs tracking-wide text-attention uppercase">Needs you · task {sdd.gate.taskN}</div>
          <p class="text-sm whitespace-pre-wrap">{sdd.gate.question}</p>
          <textarea
            class="w-full rounded-md border border-border bg-bg px-2 py-1.5 text-sm"
            rows="3"
            aria-label="Answer"
            placeholder="Your answer"
            bind:value={answer}
          ></textarea>
          {#if answerError}<p class="text-sm text-err" role="alert">{answerError}</p>{/if}
          <div class="flex justify-end">
            <Button onclick={submitAnswer} disabled={answering || !answer.trim()}>Answer</Button>
          </div>
        </Card>
      {/if}

      {#if sdd}
        <Segmented
          label="View"
          value={route.view}
          onchange={setView}
          options={[
            { value: 'lanes', label: 'Lanes' },
            { value: 'graph', label: 'Graph' },
            { value: 'timeline', label: 'Timeline' },
          ]}
        />
        {#if route.view === 'graph'}
          <Graph tasks={sdd.tasks} {now} selected={sel} onSelect={select} />
        {:else if route.view === 'timeline'}
          <Timeline
            nodes={$stack.nodes}
            startedAt={sdd.startedAt}
            endedAt={sdd.endedAt}
            {now}
            {samples}
            {tokensUsed}
            {tokensMax}
            onSelectStep={(id) => dispatch({ type: 'select', nodeId: id, kind: 'step' })}
          />
        {:else}
          <Lanes lanes={lanes(sdd)} selected={sel} {roster} onSelect={select} />
        {/if}
      {:else if swarm}
        <ul class="flex flex-col gap-1" data-testid="swarm-roles">
          {#each swarm.roles as r (r.name)}
            <li class="flex items-center gap-2 rounded-md bg-surface px-3 py-1.5 text-sm">
              <span class="font-mono">{r.name}</span>
              <Tag tone={r.status === 'done' ? 'ok' : r.status === 'failed' ? 'err' : 'neutral'}>{r.status}</Tag>
              <span class="flex-1 truncate text-xs text-muted">{r.detail ?? ''}</span>
              <span class="font-mono text-xs text-muted">{r.tokens.toLocaleString()}</span>
            </li>
          {/each}
        </ul>
        <Timeline nodes={$stack.nodes} {now} {samples} {tokensUsed} {tokensMax} onSelectStep={(id) => dispatch({ type: 'select', nodeId: id, kind: 'step' })} />
      {/if}

      {#if sel && selTask}
        <section class="flex flex-col gap-2" aria-label="Selected stage" data-testid="stage-steps">
          <h2 class="text-xs tracking-wide text-muted uppercase">
            Task {sel.task} · {stageLabel(sel.stage)} · {steps.length} step{steps.length === 1 ? '' : 's'}
          </h2>
          {#if steps.length === 0}
            <p class="text-sm text-muted">{sel.stage === 'commit' ? 'Commits run in the controller, so there are no steps.' : 'No steps recorded for this stage yet.'}</p>
          {:else}
            <div class="flex flex-wrap gap-1">
              {#each steps as id (id)}
                <button
                  type="button"
                  class="max-w-xs cursor-pointer truncate rounded border border-border px-2 py-0.5 text-xs hover:bg-hover {dock.selected === id ? 'bg-raise text-accent' : 'text-muted'}"
                  onclick={() => dispatch({ type: 'select', nodeId: id, kind: 'step' })}
                >
                  {nodeLabel($stack.nodes.get(id), id)}
                </button>
              {/each}
            </div>
            <Transcript store={stack} density="steps" foldTasks={false} onlyNodes={new Set(steps)} cursor={dock.selected ?? null} />
          {/if}
        </section>
      {/if}
    </div>
  </div>

  <Dock {dock} {selectedLabel} {gateFailed} onAction={dispatch}>
    {#if dock.tab === 'inspect'}
      <InspectTab sessionId={agentId} stack={$stack} {dock} {cache} onSelect={(id) => dispatch({ type: 'select', nodeId: id, kind: 'step' })} />
    {:else if dock.tab === 'changes'}
      <ChangesTab {agentId} sessionId={agentId} stack={$stack} {dock} gate={agent?.gate} changedFiles={agent?.changedFiles ?? 0} />
    {:else}
      <FilesTab {agentId} stack={$stack} {dock} />
    {/if}
  </Dock>
</div>
