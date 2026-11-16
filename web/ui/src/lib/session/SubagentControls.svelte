<script lang="ts">
  import { APIError } from '../api'
  import Button from '../ui/Button.svelte'
  import type { SubagentControls, WireSubagentControls } from '../stack'

  /*
    One live child's controls, shared by every screen that shows a child: the
    fleet's session screen drills into a child with this strip, and so does a
    factory worker's conversation. It holds no transport of its own — `controls`
    is `createSubagentControls(sessionId, childId)`, minted by whoever owns the
    session — and reads support only from the capability report the session
    itself sends.

    The three ways a control can be missing are kept apart, because they mean
    different things and a person acts differently on each:
      - unsupported: the session reports no child controls at all;
      - unavailable: the child (or its transcript) is not reachable from here,
        or the one control a session does not register;
      - unknown: the request went out and no answer came back, so nothing is
        claimed about the outcome.

    Acceptance is never consumption: a child's queue taking an instruction is
    not the child reading it, and a stop is acknowledged by the bridge, not
    observed, until the child's own status leaves `running`.
  */
  export interface SubagentControlsProps {
    /** The session that owns the child; child ids are session-local. */
    sessionId: string
    childId: number
    /** The child's label, for the controls' accessible names. */
    label?: string
    /**
     * The child's status as its owning session last reported it. Empty means
     * it reported none, which is unknown — never finished.
     */
    status?: string
    /** What the owning session reports it supports. */
    capabilities: WireSubagentControls
    /** This child's steer and stop, minted by the session that owns it. */
    controls: SubagentControls
    /** False when the child or its transcript cannot be reached from here. */
    reachable?: boolean
    /** A narrow screen: the same controls, stacked, with short labels. */
    compact?: boolean
  }

  let {
    sessionId,
    childId,
    label = '',
    status = '',
    capabilities,
    controls,
    reachable = true,
    compact = false,
  }: SubagentControlsProps = $props()

  type Steer =
    | { kind: 'idle' }
    | { kind: 'sending' }
    | { kind: 'accepted'; requestId: string }
    | { kind: 'unknown'; reason: string }
    | { kind: 'failed'; reason: string }

  type Stop =
    | { kind: 'idle' }
    | { kind: 'requesting' }
    | { kind: 'pending' }
    | { kind: 'unknown'; reason: string }
    | { kind: 'failed'; reason: string }

  let draft = $state('')
  let steer = $state<Steer>({ kind: 'idle' })
  let stop = $state<Stop>({ kind: 'idle' })

  /** The statuses the bridge refuses with: its own answer, never an unknown outcome. */
  const REFUSED = new Set([400, 401, 404, 409, 501])

  /** A definite refusal is a failure; anything else leaves the outcome unknown. */
  function classify(e: unknown): 'failed' | 'unknown' {
    return e instanceof APIError && REFUSED.has(e.status) ? 'failed' : 'unknown'
  }

  function reasonOf(e: unknown): string {
    if (e instanceof APIError) return `the bridge refused with ${e.status}`
    const message = e instanceof Error ? e.message : String(e)
    return message ? `no answer (${message})` : 'no answer'
  }

  /**
   * The child's status leaving `running` is the only observation that makes a
   * stop terminal. An acknowledgement is a receipt about the request.
   */
  const terminal = $derived(status !== '' && status !== 'running')

  /**
   * The stop's receipt, with an observation folded in: a stop that was
   * requested and whose child is no longer running is `stopped`, and nothing
   * else turns an acknowledgement into one.
   */
  const stopReceipt = $derived<{ kind: string; reason: string }>(
    stop.kind === 'pending' && terminal
      ? { kind: 'stopped', reason: '' }
      : { kind: stop.kind, reason: 'reason' in stop ? stop.reason : '' },
  )

  const fieldId = $derived(`subagent-instruction-${sessionId}-${childId}`)

  async function send() {
    const message = draft.trim()
    if (!message || steer.kind === 'sending') return
    if (!reachable || !capabilities.steer) return
    steer = { kind: 'sending' }
    try {
      // One request ID per instruction, kept across the transport's own
      // retries: a re-sent instruction is deduplicated by the session.
      const out = await controls.steer(message)
      if (out.status === 'accepted') {
        steer = { kind: 'accepted', requestId: out.requestId }
        draft = ''
      } else if (!out.status) {
        steer = { kind: 'unknown', reason: 'the session reported no status for it' }
      } else {
        steer = { kind: 'unknown', reason: `the session reported “${out.status}”, which this build does not know` }
      }
    } catch (e) {
      const kind = classify(e)
      steer = { kind, reason: reasonOf(e) } as Steer
    }
  }

  async function stopChild() {
    if (stop.kind === 'requesting' || stopReceipt.kind === 'stopped') return
    if (!reachable || !capabilities.stop) return
    stop = { kind: 'requesting' }
    try {
      const out = await controls.stop()
      if (out.cancelled) {
        // Acknowledged, not observed: the child is asked to stop, and the
        // status it reports is what ends the stop.
        stop = { kind: 'pending' }
        return
      }
      stop = { kind: 'unknown', reason: 'the bridge did not acknowledge the stop' }
    } catch (e) {
      const kind = classify(e)
      stop = { kind, reason: reasonOf(e) } as Stop
    }
  }

  function onInstructionKey(e: KeyboardEvent) {
    // The field is multi-line, so Enter alone is a line break and the send is
    // the same chord the composer uses.
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault()
      void send()
    }
  }

  const tone = (kind: string) =>
    kind === 'failed'
      ? 'text-danger'
      : kind === 'unknown'
        ? 'text-warn'
        : kind === 'accepted' || kind === 'stopped'
          ? 'text-ok'
          : 'text-muted'
</script>

<!--
  `data-session` and `data-child` are the control's identity: child ids are
  session-local, so the same number in two sessions is two different children
  and their controls are never shared.
-->
<section
  class={compact ? 'grid min-w-0 gap-2' : 'flex min-w-0 flex-wrap items-start gap-2'}
  data-testid="subagent-controls"
  data-session={sessionId}
  data-child={childId}
  data-layout={compact ? 'compact' : 'wide'}
  aria-label={label ? `Controls for ${label}` : 'Child controls'}
>
  {#if !reachable}
    <p class="w-full text-sm text-muted" data-testid="subagent-unavailable" role="status">
      Unavailable — this child cannot be reached from here, so it cannot be steered or stopped.
    </p>
  {:else if !capabilities.steer && !capabilities.stop}
    <p class="w-full text-sm text-muted" data-testid="subagent-unsupported" role="status">
      Unsupported — this session reports no child controls, so this child cannot be steered or stopped from here.
    </p>
  {:else}
    {#if capabilities.steer}
      <div class="grid min-w-0 flex-1 gap-1">
        <label class="text-xs text-muted" for={fieldId}>Instruction for {label || `child ${childId}`}</label>
        <textarea
          id={fieldId}
          class="min-h-16 w-full resize-y rounded-md border border-border bg-bg px-2 py-1 text-sm"
          rows={compact ? 2 : 3}
          bind:value={draft}
          onkeydown={onInstructionKey}
          placeholder="Tell this child what to change…"
        ></textarea>
        <div class="flex flex-wrap items-center gap-2">
          <Button onclick={() => void send()} disabled={!draft.trim() || steer.kind === 'sending'}>
            {compact ? 'Send' : 'Send instruction'}
          </Button>
          <span class="text-xs text-muted">⌘⏎ sends</span>
        </div>
      </div>
    {:else}
      <p class="text-sm text-muted" data-testid="steer-unavailable" role="status">
        Unavailable — this session does not support steering a child.
      </p>
    {/if}
    {#if capabilities.stop}
      <Button
        variant="danger"
        onclick={() => void stopChild()}
        disabled={stop.kind === 'requesting' || stopReceipt.kind === 'stopped'}
        aria-label={label ? `Stop ${label}` : 'Stop child'}
      >Stop child</Button>
    {:else}
      <p class="text-sm text-muted" data-testid="stop-unavailable" role="status">
        Unavailable — this session does not support stopping a child.
      </p>
    {/if}
  {/if}

  {#if steer.kind !== 'idle'}
    <p class="w-full text-sm {tone(steer.kind)}" data-testid="steer-receipt" data-state={steer.kind} role="status">
      {#if steer.kind === 'sending'}
        Sending the instruction…
      {:else if steer.kind === 'accepted'}
        Accepted — the child's queue has it. It is not read yet: the child reads it at its next step,
        and a child that settles first never does.
        <span class="text-xs text-muted" data-testid="steer-request">request {steer.requestId}</span>
      {:else if steer.kind === 'unknown'}
        Outcome unknown — {steer.reason}. The instruction may or may not be queued, so check the child
        before sending it again.
      {:else}
        Refused — {steer.reason}, so the instruction was not queued.
      {/if}
    </p>
  {/if}

  {#if stopReceipt.kind !== 'idle'}
    <p class="w-full text-sm {tone(stopReceipt.kind)}" data-testid="stop-receipt" data-state={stopReceipt.kind} role="status">
      {#if stopReceipt.kind === 'requesting'}
        Asking the child to stop…
      {:else if stopReceipt.kind === 'pending'}
        Stop requested — pending. The bridge acknowledged the request; the child is not stopped until
        its own status leaves running.
      {:else if stopReceipt.kind === 'stopped'}
        The child is no longer running ({status}), so the stop is done.
      {:else if stopReceipt.kind === 'unknown'}
        Outcome unknown — {stopReceipt.reason}. The child may still be running.
      {:else}
        Refused — {stopReceipt.reason}, so the child was not asked to stop.
      {/if}
    </p>
  {/if}
</section>
