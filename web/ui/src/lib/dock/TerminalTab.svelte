<script lang="ts">
  import Button from '../ui/Button.svelte'
  import Terminal from './Terminal.svelte'
  import { errMessage, openTerminal, terminalRelease, type CallDetail, type NodeDetailResponse } from '../api'
  import type { StackState, WireNode } from '../stack'
  import type { createNodeCache } from './nodeCache'
  import type { DockState } from './dock'

  let {
    agentId,
    sessionId,
    subagentId,
    stack,
    dock,
    cache,
    held = false,
    onOutput,
  }: {
    agentId: string
    sessionId: string
    subagentId?: number
    stack: StackState
    dock: DockState
    cache: ReturnType<typeof createNodeCache>
    /** The fleet row's `held`: the agent is paused while someone types here. */
    held?: boolean
    /** Output arrived; the dock marks the tab unread while it is not showing. */
    onOutput?: () => void
  } = $props()

  /** The bridge hands the agent back by itself after this long without input. */
  const IDLE_MS = 2 * 60 * 1000

  const isShell = (n: WireNode | undefined) => !!n?.tool && /^(shell|test)\./.test(n.tool.name)
  const node = $derived(dock.mode === 'select' && dock.selected ? stack.nodes.get(dock.selected) : undefined)
  const replayNode = $derived(isShell(node) ? node : undefined)
  const command = $derived(replayNode?.tool?.calls?.[0]?.target ?? replayNode?.tool?.display ?? '')

  let call = $state<CallDetail | undefined>()
  let loading = $state(false)
  $effect(() => {
    const n = replayNode
    call = undefined
    if (!n) return
    loading = true
    let stale = false
    cache
      .get(sessionId, n.id, n, subagentId)
      .then((r: NodeDetailResponse | 'unsupported') => {
        if (stale) return
        call = r === 'unsupported' ? undefined : r.detail?.calls?.[0]
      })
      .catch(() => {})
      .finally(() => !stale && (loading = false))
    return () => (stale = true)
  })
  const replayOutput = $derived(call?.error || call?.output || replayNode?.tool?.calls?.[0]?.output || '')

  let started = $state(false)
  let tid = ''
  let lastInput = $state(0)
  let now = $state(Date.now())
  let error = $state('')
  let releasing = $state(false)

  const open = async (size: { cols: number; rows: number }) => {
    const r = await openTerminal(agentId, size)
    tid = r.terminalId
    return r
  }

  // The countdown only ticks while the agent is held.
  $effect(() => {
    if (!held) return
    now = Date.now()
    const t = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(t)
  })
  const remaining = $derived(Math.max(0, Math.ceil((lastInput + IDLE_MS - now) / 1000)))
  const countdown = $derived(lastInput ? `${Math.floor(remaining / 60)}:${String(remaining % 60).padStart(2, '0')}` : '')

  async function handBack() {
    if (!tid) return
    releasing = true
    error = ''
    try {
      await terminalRelease(agentId, tid)
    } catch (e) {
      error = errMessage(e)
    } finally {
      releasing = false
    }
  }
</script>

<div class="flex h-full min-h-0 flex-col gap-2 p-3 text-sm" data-testid="terminal-tab">
  {#if replayNode}
    <section class="flex flex-col gap-1" data-testid="terminal-replay">
      <h3 class="truncate text-xs text-muted">Replay of <span class="font-mono text-sub">{command}</span></h3>
      <pre class="max-h-60 overflow-auto rounded bg-bg p-2 font-mono text-xs whitespace-pre-wrap">{loading ? 'Loading…' : replayOutput || '(no output)'}</pre>
    </section>
  {/if}

  {#if held}
    <div class="flex items-center gap-2 rounded border border-border bg-raise px-2 py-1 text-xs" data-testid="hold-strip">
      <span class="text-warn">agent paused</span>
      {#if countdown}<span class="font-mono text-muted" title="The agent is handed back after two minutes without input">{countdown}</span>{/if}
      <Button variant="ghost" class="ml-auto min-h-8 px-2 py-1 text-xs" disabled={releasing} onclick={handBack}>Hand back</Button>
    </div>
  {/if}
  {#if error}<p class="text-xs text-err" role="alert">{error}</p>{/if}

  {#if started}
    <Terminal
      target={{ agentId }}
      {open}
      onInput={() => (lastInput = Date.now())}
      {onOutput}
      onClose={() => (started = false)}
    />
  {:else}
    <div class="flex flex-col items-start gap-2">
      <p class="text-xs text-muted">Open a shell in this agent's container. The agent pauses while you type and resumes when you hand back.</p>
      <Button variant="ghost" onclick={() => (started = true)}>Open shell</Button>
    </div>
  {/if}
</div>
