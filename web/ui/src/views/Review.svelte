<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import GateStrip from '../lib/dock/GateStrip.svelte'
  import GateResultView from '../lib/GateResult.svelte'
  import Segmented from '../lib/ui/Segmented.svelte'
  import ShipPanel from '../lib/review/ShipPanel.svelte'
  import FileDiffView from '../lib/review/FileDiff.svelte'
  import ByStep from '../lib/review/ByStep.svelte'
  import { fileHash, isViewed, parseUnified, type FileDiff } from '../lib/review/unified'
  import { createStackStore, type StackState } from '../lib/stack'
  import { connectSSE, type SSEEvent } from '../lib/sse'
  import { getDiff, listReviewComments, postReviewComment, resolveReviewComment, errMessage, type DiffFile, type ReviewComment } from '../lib/api'
  import { gateState, pickGate } from '../lib/dock/gate'
  import { exitDestination } from '../lib/exit'
  import { renderMarkdown } from '../lib/markdown'
  import type { AgentRow } from '../lib/fleet'

  let { agentId, agent = undefined, onBack = () => {}, onShipped = () => {} }: { agentId: string; agent?: AgentRow; onBack?: () => void; onShipped?: (outcome: string) => void } = $props()

  // Review instances are keyed by agent route in App, so the id is fixed for the component's life.
  // svelte-ignore state_referenced_locally
  const stack = createStackStore(agentId)
  let stackState = $state<StackState>({ status: 'loading', rev: 0, roots: [], nodes: new Map() })
  const unsubStack = stack.subscribe((s) => (stackState = s))

  const MODE_KEY = 'marshal.ui.review.view'
  function readView(): 'unified' | 'split' {
    try {
      return localStorage.getItem(MODE_KEY) === 'split' ? 'split' : 'unified'
    } catch {
      return 'unified'
    }
  }
  let view = $state<'unified' | 'split'>(readView())
  function setView(v: string) {
    view = v === 'split' ? 'split' : 'unified'
    try {
      localStorage.setItem(MODE_KEY, view)
    } catch {
      // Not persisted; fine.
    }
  }
  let scope = $state<'net' | 'step'>('net')

  let files = $state<DiffFile[]>([])
  let filesLoaded = $state(false)
  let loadError = $state('')
  // Parsed diff per path; absent until its section scrolls into view.
  let diffs = $state<Record<string, FileDiff | null>>({})
  let viewedTick = $state(0)
  let comments = $state<ReviewComment[]>([])
  let reloadKey = $state(0)
  // Hash of each file when a comment on it was first seen, to tell when it has since changed.
  let baseline = $state<Record<string, string>>({})

  async function loadFile(path: string) {
    try {
      const r = await getDiff(agentId, path)
      const f = parseUnified(r.diff ?? '').find((x) => x.path === path) ?? parseUnified(r.diff ?? '')[0] ?? null
      diffs[path] = f
    } catch {
      diffs[path] = null
    }
  }

  async function loadFiles(refresh = false) {
    try {
      const r = await getDiff(agentId)
      files = r.files ?? []
      loadError = ''
      if (refresh) {
        // Refetch what was already on screen; the rest loads on scroll.
        for (const p of Object.keys(diffs)) void loadFile(p)
      }
    } catch (e) {
      loadError = errMessage(e)
    } finally {
      filesLoaded = true
    }
  }

  async function loadComments() {
    try {
      comments = await listReviewComments(agentId)
    } catch {
      // Keep what we have.
    }
  }

  // Loads a file's diff as its placeholder scrolls into view; without the observer, loads at once.
  function lazy(node: HTMLElement, path: string) {
    if (diffs[path] !== undefined) return
    if (typeof IntersectionObserver === 'undefined') {
      void loadFile(path)
      return
    }
    const io = new IntersectionObserver((es) => {
      if (es.some((e) => e.isIntersecting)) {
        io.disconnect()
        if (diffs[path] === undefined) void loadFile(path)
      }
    }, { rootMargin: '400px' })
    io.observe(node)
    return { destroy: () => io.disconnect() }
  }

  async function post(c: { path: string; line: number; side: 'old' | 'new'; quote: string; body: string }) {
    const d = diffs[c.path]
    if (d) baseline[c.path] = fileHash(d)
    await postReviewComment(agentId, c)
    await loadComments()
  }
  async function resolve(id: string) {
    await resolveReviewComment(agentId, id)
    await loadComments()
  }

  // A comment whose file now differs from when it was first shown has been acted on.
  $effect(() => {
    for (const c of comments) {
      const d = diffs[c.path]
      if (d && baseline[c.path] === undefined) baseline[c.path] = fileHash(d)
    }
  })
  const changedSince = $derived(
    Object.fromEntries(comments.map((c) => [c.id, !!diffs[c.path] && baseline[c.path] !== undefined && baseline[c.path] !== fileHash(diffs[c.path]!)])),
  )

  let telemetryTimer: ReturnType<typeof setTimeout> | undefined
  function onSSE(e: SSEEvent) {
    if (e.type === 'connected') {
      stack.onEvent({ type: 'connected' })
      return
    }
    if (e.type !== 'message') return
    let payload: unknown
    try {
      payload = JSON.parse(e.message.data)
    } catch {
      return
    }
    stack.onEvent(payload)
    const kind = (payload as { params?: { update?: { kind?: string } } })?.params?.update?.kind
    if (kind === 'session_telemetry') {
      clearTimeout(telemetryTimer)
      telemetryTimer = setTimeout(() => {
        void loadFiles(true)
        void loadComments()
        reloadKey++
      }, 500)
    }
  }

  let disconnect: (() => void) | undefined
  onMount(() => {
    void stack.load()
    void loadFiles()
    void loadComments()
    disconnect = connectSSE({ sessionId: agentId, onEvent: onSSE })
  })
  onDestroy(() => {
    disconnect?.()
    stack.destroy()
    unsubStack()
    clearTimeout(telemetryTimer)
  })

  const record = $derived(agent ? pickGate(agent.gate, null) : null)
  const gk = $derived(gateState(record))
  const dest = $derived(agent ? exitDestination({ sourceKind: agent.sourceKind ?? '', readOnly: agent.readOnly ?? false }) : 'merge')
  let ship: { pushWithOverride: (reason: string) => Promise<void> } | undefined = $state()

  // One final message per turn, newest first.
  const summary = $derived(
    [...stackState.nodes.values()]
      .filter((n) => n.kind === 'final' && n.message)
      .sort((a, b) => (b.message!.at ?? 0) - (a.message!.at ?? 0)),
  )

  const commentsFor = (path: string) => comments.filter((c) => c.path === path)
  function jumpTo(path: string) {
    document.getElementById(`file-${encodeURIComponent(path)}`)?.scrollIntoView?.({ block: 'start' })
  }
</script>

<div class="flex h-full flex-col" data-testid="review">
  <header class="flex items-center gap-3 border-b border-border px-4 py-2">
    <a href={`#chat/${agentId}`} class="text-sm text-muted hover:text-fg" onclick={onBack}>← Session</a>
    <h1 class="min-w-0 flex-1 truncate text-sm font-semibold">{agent?.name || agentId}</h1>
    <Segmented
      label="Diff scope"
      value={scope}
      options={[{ value: 'net', label: 'Net effect' }, { value: 'step', label: 'By step' }]}
      onchange={(v) => (scope = v === 'step' ? 'step' : 'net')}
    />
    {#if scope === 'net'}
      <Segmented label="Diff view" value={view} options={[{ value: 'unified', label: 'Unified' }, { value: 'split', label: 'Split' }]} onchange={setView} />
    {/if}
  </header>

  <div class="flex min-h-0 flex-1">
    <aside class="flex w-80 shrink-0 flex-col gap-4 overflow-y-auto border-r border-border p-3" data-testid="review-left">
      <section class="flex flex-col gap-2">
        <h3 class="text-xs tracking-wide text-muted uppercase">Gate</h3>
        <GateStrip {agentId} gate={agent?.gate} />
        {#if dest === 'push' && record && (gk === 'failed' || gk === 'skipped')}
          <GateResultView result={record.result} onOverride={(reason) => ship?.pushWithOverride(reason)} />
        {/if}
      </section>

      <section class="flex flex-col gap-1">
        <h3 class="text-xs tracking-wide text-muted uppercase">Files</h3>
        {#if !filesLoaded}<div class="text-xs text-muted">Loading…</div>
        {:else if files.length === 0}<div class="text-xs text-muted">No changes.</div>{/if}
        {#each files as f (f.path)}
          {#key viewedTick}
            <button type="button" class="flex items-center gap-2 text-left text-xs hover:text-accent" onclick={() => jumpTo(f.path)}>
              <span class="w-3 text-ok">{diffs[f.path] && isViewed(agentId, diffs[f.path]!) ? '✓' : ''}</span>
              <span class="min-w-0 flex-1 truncate font-mono">{f.path}</span>
              <span class="text-ok">+{f.added}</span><span class="text-err">−{f.removed}</span>
            </button>
          {/key}
        {/each}
      </section>

      <section class="flex flex-col gap-2">
        <h3 class="text-xs tracking-wide text-muted uppercase">Summary</h3>
        {#each summary as n (n.id)}
          <div class="rounded border border-border bg-surface p-2 text-sm" data-testid="summary-item">{@html renderMarkdown(n.message!.content)}</div>
        {:else}
          <div class="text-xs text-muted">No messages yet.</div>
        {/each}
      </section>

      {#if agent}
        <ShipPanel bind:this={ship} {agent} onDone={onShipped} />
      {/if}
    </aside>

    <main class="min-w-0 flex-1 overflow-y-auto p-4">
      {#if scope === 'step'}
        <ByStep {agentId} sessionId={agentId} stack={stackState} {reloadKey} onUnsupported={() => (scope = 'net')} />
      {:else if loadError}
        <div class="rounded border border-danger p-3 text-sm">{loadError}</div>
      {:else}
        <div class="flex flex-col gap-4">
          {#each files as f (f.path)}
            {@const d = diffs[f.path]}
            {#if d}
              <FileDiffView {agentId} file={d} {view} comments={commentsFor(f.path)} stack={stackState} {changedSince} onComment={post} onResolve={resolve} onviewed={() => viewedTick++} />
            {:else}
              <div use:lazy={f.path} class="rounded-md border border-border p-3 text-xs text-muted" id={`file-${encodeURIComponent(f.path)}`} data-testid="file-placeholder">
                <span class="font-mono">{f.path}</span> {d === null ? '— could not load' : '— loading…'}
              </div>
            {/if}
          {/each}
          {#if filesLoaded && files.length === 0}<div class="text-sm text-muted">The agent has not changed any files.</div>{/if}
        </div>
      {/if}
    </main>
  </div>
</div>
