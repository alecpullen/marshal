<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte'
  import { createSessionStore, transcriptEntries, type Mode } from '../lib/store.js'
  import Composer from '../lib/Composer.svelte'
  import ModeSwitcher from '../lib/ModeSwitcher.svelte'
  import ToolCallCard from '../lib/ToolCallCard.svelte'
  import PermissionModal from '../lib/PermissionModal.svelte'
  import QuestionModal from '../lib/QuestionModal.svelte'
  import ExitPanel from '../lib/ExitPanel.svelte'
  import { renderMarkdown, initHighlighter } from '../lib/markdown'
  import { listAgents, APIError, type AgentStatus } from '../lib/api'
  import { createStackStore, type StackStore } from '../lib/stack'
  import { get } from 'svelte/store'
  import { formatChatRoute, type ChatRoute } from '../lib/routes'
  import type { AgentRow } from '../lib/fleet'
  import Dock from '../lib/dock/Dock.svelte'
  import InspectTab from '../lib/dock/InspectTab.svelte'
  import ChangesTab from '../lib/dock/ChangesTab.svelte'
  import FilesTab, { type OpenRequest } from '../lib/dock/FilesTab.svelte'
  import { createNodeCache } from '../lib/dock/nodeCache'
  import { initialDock, load as loadDock, reduce, save as saveDock, type DockAction, type DockState } from '../lib/dock/dock'
  import { gateState } from '../lib/dock/gate'
  import { latestEdit, editCalls, liveStep, nodeFile, nodeLabel, subagentIdOf } from '../lib/dock/nodes'
  import Transcript from '../lib/transcript/Transcript.svelte'
  import NowBar from '../lib/transcript/NowBar.svelte'
  import Segmented from '../lib/ui/Segmented.svelte'
  import { browseKey, flattenVisible, nodeText } from '../lib/transcript/browse'
  import { nextOverride, parseDensity, effective, type Density } from '../lib/transcript/density'
  import type { TranscriptCtx } from '../lib/transcript/ctx'

  interface Props {
    sessionId: string
    onBack: () => void
    /** The parsed #chat route; read once, for the dock state it carries. */
    route?: ChatRoute | null
    /** This agent's live fleet row, for its gate and change count. */
    agent?: AgentRow
  }

  let { sessionId, onBack, route = null, agent = undefined }: Props = $props()

  // Chat instances are keyed by session route, so this store intentionally
  // captures the session ID once for the lifetime of the component.
  // svelte-ignore state_referenced_locally
  const stack = createStackStore(sessionId)
  /*
    One stream carries every transcript's patches, so the session store hands
    each event to the parent store and to every drilled-in child store; each
    keeps only the patches that are its own.
  */
  function routeEvent(e: unknown) {
    stack.onEvent(e)
    for (const d of drill) d.store.onEvent(e)
  }
  // svelte-ignore state_referenced_locally
  const { state: session, actions } = createSessionStore(sessionId, '/', routeEvent)

  // The transcript on screen: the parent's, or the subagent drilled into.
  interface Drill {
    subagentId: number
    label: string
    store: StackStore
    /** The parent's browse cursor, restored on the way back. */
    parentCursor: string | null
  }
  let drill = $state<Drill[]>([])
  const shown = $derived<StackStore>(drill.at(-1)?.store ?? stack)
  const subagentId = $derived(drill.at(-1)?.subagentId)

  /*
    The stack transcript is the view; the legacy message list renders only
    for agents that predate session/stack (501) or when the snapshot cannot
    be fetched at all.
  */
  const useStack = $derived($stack.status === 'ready')
  const legacy = $derived($stack.status === 'unsupported' || $stack.status === 'error')

  const DENSITY_KEY = 'marshal.ui.density'
  function readDensity(): Density {
    try {
      return parseDensity(localStorage.getItem(DENSITY_KEY))
    } catch {
      return 'steps'
    }
  }
  let density = $state<Density>(readDensity())
  function setDensity(v: string) {
    density = parseDensity(v)
    try {
      localStorage.setItem(DENSITY_KEY, density)
    } catch {
      // The choice just does not persist.
    }
  }
  let overrides = $state(new Map<string, Density>())
  let unfolded = $state(new Set<string>())

  /*
    The dock. Its size and width persist per browser; the selection, tab and
    size can also arrive on the URL, which wins over the stored size.
  */
  // svelte-ignore state_referenced_locally
  let dock = $state<DockState>(
    initialDock({
      ...loadDock(),
      ...(route?.dock ? { size: route.dock } : {}),
      ...(route?.tab ? { tab: route.tab } : {}),
      ...(route?.node ? { mode: 'select' as const, selected: route.node } : {}),
    }),
  )
  function dispatch(a: DockAction) {
    dock = reduce(dock, a)
    saveDock(dock)
  }
  // The expanded dock turns the transcript into an outline without touching the saved choice.
  const effGlobal = $derived<Density>(dock.size === 'expanded' ? 'outline' : density)
  const cache = createNodeCache()
  let fileRequest = $state<OpenRequest | undefined>(undefined)
  let fileSeq = 0
  function openFileInDock(path: string, line?: number) {
    fileRequest = { path, line, seq: ++fileSeq }
    dispatch({ type: 'openTab', tab: 'files' })
  }
  const kindOf = (id: string) => $shown.nodes.get(id)?.kind ?? ''
  function selectNode(id: string, reveal = true) {
    dispatch({ type: 'select', nodeId: id, kind: kindOf(id), reveal })
  }

  // Keep the URL in step with the dock without growing the history.
  $effect(() => {
    if (route?.view === 'review') return
    const next = formatChatRoute({
      id: sessionId,
      view: 'session',
      node: dock.mode === 'select' ? dock.selected : undefined,
      dock: dock.size,
      tab: dock.tab,
    })
    try {
      if (location.hash !== next) history.replaceState(null, '', next)
    } catch {
      // A sandboxed frame may refuse; the URL just stops tracking.
    }
  })

  // A new edit call switches a following dock to Changes, or marks it unseen.
  // The first snapshot sets the baseline and is not an edit.
  let editKey: string | undefined
  $effect(() => {
    if ($stack.status !== 'ready') return
    const e = latestEdit($stack)
    const key = e ? `${e.id}:${editCalls(e).length}` : ''
    const prev = editKey
    editKey = key
    if (prev !== undefined && key && key !== prev) dispatch({ type: 'liveEdit' })
  })

  const gateFailed = $derived(gateState(agent?.gate) === 'failed')
  const selectedLabel = $derived(dock.selected ? nodeLabel($shown.nodes.get(dock.selected), dock.selected) : '')

  // Drilling into a subagent swaps the transcript for its own.
  function popDrill() {
    const top = drill.at(-1)
    if (!top) return
    top.store.destroy()
    drill = drill.slice(0, -1)
    cursor = top.parentCursor
    scrollCursorIntoView()
  }
  function popTo(depth: number) {
    while (drill.length > depth) popDrill()
  }
  async function drillInto(id: string) {
    const nodes = $shown.nodes
    const n = nodes.get(id)
    const subNode =
      n?.kind === 'subagent' ? n : (n?.children ?? []).map((c) => nodes.get(c)).find((c) => c?.kind === 'subagent')
    const subId = subNode ? subagentIdOf(subNode.id) : undefined
    if (!subNode || subId === undefined) {
      flash('No subagent here')
      return
    }
    const store = createStackStore(sessionId, { subagentId: subId })
    await store.load()
    if (get(store).status !== 'ready') {
      // No transcript of its own: stay put and look at the card.
      store.destroy()
      flash('This subagent has no separate transcript')
      selectNode(subNode.id)
      return
    }
    drill = [...drill, { subagentId: subId, label: subNode.subagent?.label ?? `subagent ${subId}`, store, parentCursor: cursor }]
    cursor = null
    dispatch({ type: 'backToLive' })
    scrollToLatest()
  }

  // The browse cursor is the selection.
  $effect(() => {
    const c = cursor
    if (!browsing || !c) return
    untrack(() => {
      if (dock.mode === 'select' && dock.selected === c) return
      selectNode(c, false)
    })
  })

  function jumpToLive() {
    const id = liveStep($shown)?.id
    if (!id) return
    transcriptEl?.querySelector(`[data-node-id="${CSS.escape(id)}"]`)?.scrollIntoView?.({ block: 'center' })
  }

  // Clicking a row selects it.
  function onTranscriptClick(e: MouseEvent) {
    const target = e.target as HTMLElement
    // Fold and density buttons, links and text selections are not selections.
    if (target.closest?.('button, a') || window.getSelection()?.toString()) return
    const el = target.closest?.('[data-node-id]') as HTMLElement | null
    const id = el?.dataset.nodeId
    if (!id || !$shown.nodes.has(id)) return
    cursor = id
    selectNode(id, false)
  }

  // Browse mode: the TUI's Esc mode, a cursor over transcript rows.
  let browsing = $state(false)
  let cursor = $state<string | null>(null)
  let follow = $state(true)
  let toast = $state('')
  let toastTimer: ReturnType<typeof setTimeout> | undefined

  function flash(text: string) {
    toast = text
    clearTimeout(toastTimer)
    toastTimer = setTimeout(() => (toast = ''), 2000)
  }

  let now = $state(Date.now())
  const anyLive = $derived(useStack && [...$stack.nodes.values()].some((n) => n.live))
  $effect(() => {
    if (!anyLive) return
    now = Date.now()
    const t = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(t)
  })

  const tctx = $derived<TranscriptCtx>({
    nodes: $shown.nodes,
    global: effGlobal,
    overrides,
    foldTasks: true,
    unfolded,
    cursor,
    now,
  })
  const flat = $derived(flattenVisible(tctx, $shown.roots))

  function toggleFold(id: string) {
    const next = new Set(unfolded)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    unfolded = next
  }
  function toggleDensity(id: string) {
    const next = new Map(overrides)
    const cur = effective(id, overrides, (n) => $shown.nodes.get(n)?.parent, effGlobal)
    next.set(id, nextOverride(cur))
    overrides = next
  }

  function enterBrowse() {
    if (!useStack || flat.ids.length === 0) return
    browsing = true
    follow = false
    cursor = cursor && flat.ids.includes(cursor) ? cursor : flat.ids[flat.ids.length - 1]
    ;(document.activeElement as HTMLElement | null)?.blur?.()
    scrollCursorIntoView()
  }

  function scrollCursorIntoView() {
    requestAnimationFrame(() => {
      if (!cursor) return
      const el = transcriptEl?.querySelector(`[data-node-id="${CSS.escape(cursor)}"]`)
      el?.scrollIntoView?.({ block: 'nearest' })
    })
  }

  function typingTarget(t: EventTarget | null) {
    const el = t as HTMLElement | null
    return !!el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT' || el.isContentEditable)
  }

  function onDocKey(e: KeyboardEvent) {
    // Ctrl+C twice within a second stops the turn; one press only warns.
    if (e.ctrlKey && e.key.toLowerCase() === 'c' && ($session.busy || anyLive)) {
      // A selection in the page or inside the composer is a copy, not a stop;
      // getSelection() is empty for text selected within a textarea or input.
      if (window.getSelection()?.toString()) return
      const el = e.target as HTMLInputElement | HTMLTextAreaElement | null
      if (typingTarget(el) && el && el.selectionStart !== el.selectionEnd) return
      e.preventDefault()
      const t = Date.now()
      if (t - lastCtrlC < 1000) {
        lastCtrlC = 0
        stopHint = ''
        void actions.cancel()
      } else {
        lastCtrlC = t
        stopHint = 'Press Ctrl+C again to stop'
        clearTimeout(hintTimer)
        hintTimer = setTimeout(() => (stopHint = ''), 1000)
      }
      return
    }
    // ⌘P pins the dock tab; the print dialog is never wanted here.
    if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === 'p') {
      e.preventDefault()
      dispatch({ type: 'togglePin' })
      return
    }
    if (!typingTarget(e.target) && !e.altKey && (((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'j') || (e.key === '\\' && !e.metaKey && !e.ctrlKey))) {
      e.preventDefault()
      dispatch({ type: 'cycleSize' })
      return
    }
    if (!browsing || typingTarget(e.target) || e.metaKey || e.ctrlKey || e.altKey) return
    // Backspace climbs out of a subagent's transcript.
    if (e.key === 'Backspace' && drill.length) {
      e.preventDefault()
      popDrill()
      return
    }
    const out = browseKey({ cursor, follow }, e.key, flat)
    // An unbound printable key leaves browse mode and is typed.
    if (!out.bound && e.key.length === 1) {
      browsing = false
      return
    }
    e.preventDefault()
    cursor = out.state.cursor
    follow = out.state.follow
    if (follow) scrollToLatest()
    else scrollCursorIntoView()
    const fx = out.effect
    if (!fx) return
    if ('exitBrowse' in fx) {
      browsing = false
      dispatch({ type: 'backToLive' })
    } else if ('inspect' in fx) {
      selectNode(fx.inspect)
      dispatch({ type: 'openTab', tab: 'inspect' })
    } else if ('openFile' in fx) {
      const n = $shown.nodes.get(fx.openFile)
      const f = n && nodeFile(n)
      if (f) openFileInDock(f.path, f.line)
      else flash('No file for this row')
    } else if ('drill' in fx) void drillInto(fx.drill)
    else if ('toggleDensity' in fx) toggleDensity(fx.toggleDensity)
    else if ('toggleFold' in fx) toggleFold(fx.toggleFold)
    else if ('toast' in fx) flash(fx.toast)
    else if ('copy' in fx) {
      const n = $shown.nodes.get(fx.copy)
      const text = n ? nodeText(n) : ''
      navigator.clipboard?.writeText(text).then(() => flash('Copied'), () => flash('Copy failed'))
    }
  }

  // Esc in the composer is the way into browse mode; it never cancels a turn.
  function onComposerKey(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault()
      enterBrowse()
    }
  }

  let lastCtrlC = 0
  let stopHint = $state('')
  let hintTimer: ReturnType<typeof setTimeout> | undefined

  // Shiki loads its grammars asynchronously. Messages render unhighlighted
  // until it is ready, then `ready` flips and the transcript re-renders —
  // rather than withholding the transcript behind a loading state.
  let ready = $state(false)

  /* A 404 from load() means the bridge no longer maps this session to a
     live agent — resuming it is impossible, so the transcript is replaced
     with an honest note rather than an empty room that implies the session
     never had content. */
  let missing = $state(false)

  /*
    The route carries the agent id, which is also the key the bridge maps
    to a session. It is not a name a person chose, so the header resolves
    it to one and keeps the id only as a fallback.
  */
  let agentName = $state<string | null>(null)
  let projectName = $state<string | null>(null)
  let info = $state<AgentStatus | null>(null)
  const row = $derived(agent ?? info)

  let transcriptEl = $state<HTMLDivElement | null>(null)
  // Whether the view is following the tail. Scrolling up to read
  // releases it; returning to the bottom re-arms it.
  let pinned = $state(true)

  const entries = $derived(transcriptEntries($session))

  /*
    Streaming appends to the last message's text without changing the
    entry count, so following the tail cannot key off length alone. This
    signature changes on both a new entry and a growing one.
  */
  const tailSignature = $derived(
    useStack
      ? 'stack:' + $shown.rev + ':' + drill.length
      : entries.length + ':' + ($session.messages.at(-1)?.text.length ?? 0) + ':' + ($session.busy ? 1 : 0),
  )

  const PIN_THRESHOLD_PX = 48

  function atBottom(el: HTMLElement): boolean {
    return el.scrollHeight - el.scrollTop - el.clientHeight <= PIN_THRESHOLD_PX
  }

  function onScroll() {
    if (transcriptEl) pinned = atBottom(transcriptEl)
  }

  function scrollToLatest() {
    if (!transcriptEl) return
    transcriptEl.scrollTop = transcriptEl.scrollHeight
    pinned = true
  }

  $effect(() => {
    // Referenced so the effect re-runs as the tail grows.
    tailSignature
    if (!pinned || (browsing && !follow) || !transcriptEl) return
    // After the DOM has taken the new content, not before.
    requestAnimationFrame(() => {
      if (transcriptEl && pinned) transcriptEl.scrollTop = transcriptEl.scrollHeight
    })
  })

  onMount(() => {
    void stack.load()
    document.addEventListener('keydown', onDocKey)
    listAgents()
      .then((agents) => {
        const a = agents.find((x) => x.id === sessionId)
        if (!a) return
        info = a
        agentName = a.name || null
        projectName = a.project.split('/').filter(Boolean).pop() ?? null
      })
      .catch(() => {
        // The header falls back to the id; a failed lookup is not worth
        // an error banner over.
      })
    initHighlighter().then(() => (ready = true)).catch(() => {
      // Highlighting is an enhancement. A failure here leaves fenced code
      // as escaped plain blocks, which is still readable.
    })
    actions.connect()
    actions.load().catch((e) => {
      if (e instanceof APIError && e.status === 404) {
        missing = true
        return
      }
      // other load failures are surfaced via the session error if severe;
      // the SSE stream will still deliver live events.
    })
  })

  onDestroy(() => {
    document.removeEventListener('keydown', onDocKey)
    stack.destroy()
    for (const d of drill) d.store.destroy()
    clearTimeout(toastTimer)
    clearTimeout(hintTimer)
    actions.disconnect()
  })

  async function send(text: string) {
    if ($session.busy) {
      await actions.steer(text)
    } else {
      await actions.prompt(text)
    }
  }

  async function changeMode(mode: Mode) {
    await actions.setMode(mode)
  }
</script>

<div class="chat">
  <header>
    <button class="back" onclick={onBack}>← Fleet</button>
    <div class="title">
      {#if row?.origin}
        <span class="origin" title="started from {row.origin}">{({ ui: 'U', cli: 'C', mcp: 'M', issue: '#' } as Record<string, string>)[row.origin] ?? row.origin[0].toUpperCase()}</span>
      {/if}
      <span class="name">{agentName ?? sessionId}</span>
      {#if projectName}<span class="project">{projectName}</span>{/if}
      {#if row?.branch}<span class="project" title="branch">⎇ {row.branch}</span>{/if}
    </div>
    {#if useStack}
      <Segmented
        label="Detail"
        value={density}
        onchange={setDensity}
        options={[
          { value: 'outline', label: 'Outline' },
          { value: 'steps', label: 'Steps' },
          { value: 'full', label: 'Full' },
        ]}
      />
    {/if}
    <ModeSwitcher mode={$session.mode} onChange={changeMode} />
    <a class="review" href="#chat/{sessionId}/review">Review</a>
    <span class="connection" class:connected={$session.connected} title={$session.connected ? 'connected' : 'disconnected'}>
      {$session.connected ? '●' : '○'}
    </span>
  </header>

  <div class="body">
  <div class="main">
  {#if drill.length}
    <nav class="crumbs" aria-label="Transcript path">
      <button type="button" onclick={() => popTo(0)}>{agentName ?? sessionId}</button>
      {#each drill as d, i (i)}
        <span aria-hidden="true">›</span>
        <button type="button" disabled={i === drill.length - 1} onclick={() => popTo(i + 1)}>{d.label}</button>
      {/each}
    </nav>
  {/if}

  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class="transcript" bind:this={transcriptEl} onscroll={onScroll} onclick={onTranscriptClick}>
    {#if missing}
      <div class="empty">
        <p>This session could not be resumed — its agent is no longer tracked by the bridge.</p>
      </div>
    {:else if useStack}
      <Transcript
        store={shown}
        density={effGlobal}
        {overrides}
        {unfolded}
        {cursor}
        onToggleFold={toggleFold}
        onToggleDensity={toggleDensity}
      />
      {#if $shown.roots.length === 0 && !$session.busy}
        <div class="empty">
          <p>No messages yet.</p>
          <p class="hint">Describe a task below to start this agent working.</p>
        </div>
      {/if}
      {#if toast}<div class="typing" role="status">{toast}</div>{/if}
      {#if $session.error}
        <div class="error-banner">
          {$session.error}
          <button onclick={() => actions.dismissError()}>Dismiss</button>
        </div>
      {/if}
    {:else if legacy}
    {#each entries as entry (entry.key)}
      {#if entry.kind === 'message'}
        {@const message = entry.value}
        <div class="message {message.role}">
          <div class="bubble">
            {#if message.reasoning}
              <details class="reasoning">
                <summary>Thought</summary>
                <pre>{message.reasoning}</pre>
              </details>
            {/if}
            {#if message.role === 'user'}
              <!-- The user's own text is shown as typed; rendering it as
                   markdown would reformat their input under them. -->
              <div class="text">{message.text}</div>
            {:else}
              {#key ready}
                <div class="text prose">{@html renderMarkdown(message.text)}</div>
              {/key}
            {/if}
          </div>
        </div>
      {:else}
        <ToolCallCard toolCall={entry.value} />
      {/if}
    {/each}

    {#if entries.length === 0 && !$session.busy}
      <div class="empty">
        <p>No messages yet.</p>
        <p class="hint">Describe a task below to start this agent working.</p>
      </div>
    {/if}

    {#if $session.busy}
      <div class="typing">Marshal is working…</div>
    {/if}

    {#if $session.error}
      <div class="error-banner">
        {$session.error}
        <button onclick={() => actions.dismissError()}>Dismiss</button>
      </div>
    {/if}
    {/if}
  </div>

  {#if !pinned && entries.length > 0}
    <div class="jump-wrap">
      <button class="jump" onclick={scrollToLatest}>Jump to latest ↓</button>
    </div>
  {/if}

  {#if useStack}
    <NowBar stack={$stack} {now} hint={stopHint} selecting={dock.mode === 'select'} onStop={() => actions.cancel()} onJumpLive={jumpToLive} />
    {#if browsing}
      <div class="browse-hint" role="status">browse · j/k move · J/K jump · Enter detail · z fold · y copy · Esc exit</div>
    {/if}
  {/if}

  <!-- Capture phase would swallow typing; bubbling is enough for Esc. -->
  <div class="composer" onkeydown={onComposerKey} role="presentation">
    <Composer busy={$session.busy} onSend={send} onCancel={actions.cancel} />
  </div>
  </div>

  {#if !missing}
    <Dock {dock} {selectedLabel} {gateFailed} onAction={dispatch}>
      {#if dock.tab === 'inspect'}
        <InspectTab {sessionId} {subagentId} stack={$shown} {dock} {cache} onSelect={(id) => selectNode(id)} />
      {:else if dock.tab === 'changes'}
        <ChangesTab agentId={sessionId} {sessionId} stack={$stack} {dock} drilled={drill.length > 0} gate={agent?.gate} changedFiles={row?.changedFiles ?? 0} />
      {:else}
        <FilesTab agentId={sessionId} stack={$shown} {dock} request={fileRequest} />
      {/if}
    </Dock>
  {/if}
  </div>

  <ExitPanel agentId={sessionId} onDone={onBack} />
</div>

{#if $session.pendingPermission}
  <PermissionModal permission={$session.pendingPermission} onResolve={actions.resolvePermission} onDeny={() => actions.resolvePermission({ approved: false })} />
{/if}

{#if $session.pendingQuestion}
  <QuestionModal question={$session.pendingQuestion} onResolve={actions.resolveQuestion} onDecline={() => actions.resolveQuestion({ declined: true })} />
{/if}

<style>
  .chat {
    display: flex;
    flex-direction: column;
    /*
      100% of the shell's main pane, not the viewport. With 100vh the chat
      is as tall as the window while sitting inside an already-bounded
      pane, so the composer is pushed below the fold.
    */
    height: 100%;
    background: var(--color-surface);
  }
  .body {
    display: flex;
    flex: 1;
    min-height: 0;
  }
  .main {
    display: flex;
    flex: 1;
    min-width: 0;
    flex-direction: column;
  }
  .composer {
    width: 100%;
    max-width: 780px;
    margin: 0 auto;
  }
  .origin {
    display: inline-flex;
    width: 1.5rem;
    height: 1.5rem;
    flex-shrink: 0;
    align-items: center;
    justify-content: center;
    border-radius: 999px;
    background: var(--color-raise);
    font-family: var(--font-mono);
    font-size: 0.75rem;
    align-self: center;
  }
  .review {
    font-size: 0.8125rem;
    color: var(--color-accent);
    text-decoration: none;
    border: 1px solid var(--color-border);
    border-radius: 6px;
    padding: 0.25rem 0.6rem;
  }
  .crumbs {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    padding: 0.4rem 1rem;
    font-size: 0.8125rem;
    color: var(--color-muted);
    border-bottom: 1px solid var(--color-border);
  }
  .crumbs button {
    background: transparent;
    border: none;
    cursor: pointer;
    font: inherit;
    color: var(--color-accent);
  }
  .crumbs button:disabled {
    color: var(--color-fg);
    cursor: default;
  }
  header {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.75rem 1rem;
    border-bottom: 1px solid var(--color-border);
  }
  .back {
    background: transparent;
    border: none;
    cursor: pointer;
    font: inherit;
    color: var(--color-muted);
  }
  .title {
    display: flex;
    min-width: 0;
    flex: 1;
    align-items: baseline;
    gap: 0.5rem;
  }
  .name {
    overflow: hidden;
    font-weight: 600;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .project {
    overflow: hidden;
    flex-shrink: 0;
    font-size: 0.8125rem;
    color: var(--color-muted);
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .connection {
    color: var(--color-danger);
  }
  .connection.connected {
    color: var(--color-running);
  }
  .transcript {
    flex: 1;
    overflow-y: auto;
    padding: 1rem;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }
  /*
    A flex column shrinks its children to fit before it will overflow, and
    the transcript scrolls instead — so nothing in it may be shrinkable.
    Without this, tool call cards collapse to a 2px line as soon as the
    transcript is taller than the viewport: text present, height gone.
  */
  .transcript > :global(*) {
    flex-shrink: 0;
    width: 100%;
    max-width: 780px;
    margin-inline: auto;
  }
  .message {
    display: flex;
  }
  .message.user {
    justify-content: flex-end;
  }
  .message.assistant {
    justify-content: flex-start;
  }
  .bubble {
    max-width: 80%;
    padding: 0.75rem 1rem;
    border-radius: 12px;
    background: var(--color-bg);
    word-break: break-word;
  }
  /*
    pre-wrap belongs to plain text only. Rendered markdown carries its own
    block elements, and pre-wrap on the container doubles their whitespace.
  */
  .text:not(.prose) {
    white-space: pre-wrap;
  }
  .message.user .bubble {
    background: var(--color-accent);
    color: var(--color-bg);
  }
  .reasoning {
    margin-bottom: 0.5rem;
    font-size: 0.85rem;
    color: var(--color-muted);
  }
  .reasoning pre {
    margin: 0.25rem 0 0;
    white-space: pre-wrap;
    font-family: inherit;
  }
  .browse-hint {
    padding: 0.25rem 1rem;
    font-size: 0.75rem;
    color: var(--color-violet);
    border-top: 1px solid var(--color-border);
  }
  .typing {
    color: var(--color-muted);
    font-style: italic;
  }
  /*
    Sits above the composer rather than floating over the transcript, so
    it never covers the newest message — the thing it exists to reach.
  */
  .jump-wrap {
    display: flex;
    justify-content: center;
    padding: 0 1rem;
    margin-bottom: -0.5rem;
  }
  .jump {
    border: 1px solid var(--color-border);
    background: var(--color-bg);
    color: var(--color-fg);
    border-radius: 999px;
    padding: 0.35rem 0.9rem;
    font: inherit;
    font-size: 0.8125rem;
    cursor: pointer;
    box-shadow: 0 2px 8px rgb(0 0 0 / 0.35);
  }
  .jump:hover {
    background: var(--color-surface);
  }
  .empty {
    margin: auto;
    text-align: center;
    color: var(--color-muted);
  }
  .empty p {
    margin: 0;
  }
  .empty .hint {
    margin-top: 0.25rem;
    font-size: 0.875rem;
    opacity: 0.75;
  }

  /*
    Markdown styling for agent output. Kept tight rather than airy: a
    transcript is read in sequence, so generous vertical rhythm costs more
    than it buys. :global is required because this HTML is injected with
    {@html} and never passes through Svelte's style scoping.
  */
  .prose :global(> :first-child) {
    margin-top: 0;
  }
  .prose :global(> :last-child) {
    margin-bottom: 0;
  }
  .prose :global(p) {
    margin: 0.5rem 0;
  }
  .prose :global(h1),
  .prose :global(h2),
  .prose :global(h3),
  .prose :global(h4) {
    margin: 1rem 0 0.5rem;
    font-weight: 600;
    line-height: 1.3;
  }
  .prose :global(h1) {
    font-size: 1.25rem;
  }
  .prose :global(h2) {
    font-size: 1.125rem;
  }
  .prose :global(h3),
  .prose :global(h4) {
    font-size: 1rem;
  }
  /*
    Tailwind's preflight sets list-style: none on ul/ol, so markdown lists
    render as unmarked lines unless the marker is restored here. An agent's
    numbered steps losing their numbers is a real loss of meaning.
  */
  .prose :global(ul),
  .prose :global(ol) {
    margin: 0.5rem 0;
    padding-left: 1.5rem;
  }
  .prose :global(ul) {
    list-style: disc;
  }
  .prose :global(ol) {
    list-style: decimal;
  }
  .prose :global(li) {
    margin: 0.125rem 0;
  }
  .prose :global(a) {
    color: var(--color-accent);
    text-decoration: underline;
  }
  .prose :global(code) {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.875em;
    background: var(--color-surface);
    padding: 0.1em 0.35em;
    border-radius: 4px;
  }
  /* Shiki paints its own background and colours on the pre it emits. */
  .prose :global(pre) {
    margin: 0.75rem 0;
    padding: 0.75rem 1rem;
    border-radius: 8px;
    border: 1px solid var(--color-border);
    overflow-x: auto;
  }
  .prose :global(pre code) {
    background: none;
    padding: 0;
    border-radius: 0;
    font-size: 0.8125rem;
    line-height: 1.5;
  }
  .prose :global(pre.shiki-fallback) {
    background: var(--color-surface);
  }
  .prose :global(blockquote) {
    margin: 0.5rem 0;
    padding-left: 0.75rem;
    border-left: 2px solid var(--color-border);
    color: var(--color-muted);
  }
  .prose :global(table) {
    border-collapse: collapse;
    margin: 0.5rem 0;
    font-size: 0.875rem;
    display: block;
    overflow-x: auto;
  }
  .prose :global(th),
  .prose :global(td) {
    border: 1px solid var(--color-border);
    padding: 0.25rem 0.5rem;
    text-align: left;
  }
  .prose :global(hr) {
    border: none;
    border-top: 1px solid var(--color-border);
    margin: 0.75rem 0;
  }
  .error-banner {
    background: color-mix(in oklch, var(--color-danger) 18%, var(--color-bg));
    color: var(--color-danger);
    padding: 0.75rem;
    border-radius: 6px;
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .error-banner button {
    background: var(--color-surface);
    border: 1px solid var(--color-danger);
    border-radius: 4px;
    cursor: pointer;
  }
</style>