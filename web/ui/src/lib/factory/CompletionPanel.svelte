<script lang="ts">
  import Button from '../ui/Button.svelte'
  import Card from '../ui/Card.svelte'
  import Tag from '../ui/Tag.svelte'
  import {
    completionCriteria,
    completionGoal,
    completionLinks,
    completionStatus,
    linksStale,
    remainingMergeLinks,
    revisionLabel,
    reviewedPR,
    runNeedsChoice,
    selectableLinks,
  } from '../factory'
  import type {
    FactoryAssessment,
    FactoryCommand,
    FactoryCompletionCommand,
    FactoryCompletionPR,
    FactoryItem,
    FactoryMergeStep,
    FactoryMergeStrategy,
    FactoryPRLink,
  } from '../factory'
  import type { Tone } from '../glyphs'

  /** What a completion control hands back; the caller owns request identity. */
  export interface FactoryCommandInput {
    completion?: FactoryCompletionCommand
  }

  export type RunFactoryCommand = (kind: FactoryCommand['kind'], input?: FactoryCommandInput) => Promise<void>

  export interface CompletionPanelProps {
    item: FactoryItem
    busy: boolean
    run: RunFactoryCommand
  }

  /**
   * The item's business completion (design C5, plan step 8): whether a person
   * calls the work done, which is a separate fact from what the run is doing.
   *
   * Everything here renders `item` and nothing else. No status is shown as
   * achieved until the bridge reports it, so a command's only visible effect
   * is the item the next read carries. The panel owns the selection, the
   * order, the strategy and the run choice; request identity, the expected
   * version and the decision binding stay with the caller, which is why every
   * action is handed back through `run` instead of being sent here.
   *
   * The evidence a merge authorizes is exactly the evidence the confirmation
   * displayed: `reviewedPR` copies the link's identity at that moment, so a
   * pull request that moves after the review is refused by the bridge rather
   * than merged under a head nobody saw.
   */
  let { item, busy, run }: CompletionPanelProps = $props()

  const completion = $derived(item.completion ?? null)
  /** The person's decision, read apart from the run's `state`. */
  const status = $derived(completionStatus(item))
  const links = $derived(completionLinks(item))
  /** The pull requests a merge can select; a closed or merged one is history. */
  const selectable = $derived(selectableLinks(item))
  const linkStale = $derived(linksStale(links))
  const goal = $derived(completionGoal(item))
  const criteria = $derived(completionCriteria(item))
  const assessment = $derived(completion?.assessment ?? null)
  const batch = $derived(completion?.batch ?? null)
  const stopState = $derived(completion?.stopState ?? '')
  /** The bridge requires a continue/stop decision while the item owns a run. */
  const needsRunChoice = $derived(runNeedsChoice(item))

  /** A link's identity inside this item: the repository and the pull request's number. */
  const keyOf = (link: FactoryPRLink) => `${link.repoId}#${link.pr.number}`

  /*
    The chosen pull requests in merge order. Keys rather than objects, so a
    poll that re-reads the same links keeps the person's order while a link
    that is no longer selectable simply drops out of the batch.
  */
  let chosen = $state<string[]>([])
  const orderedLinks = $derived(
    chosen
      .map((key) => selectable.find((link) => keyOf(link) === key) ?? null)
      .filter((link): link is FactoryPRLink => link !== null),
  )

  let reference = $state('')
  let strategy = $state<FactoryMergeStrategy>('merge')
  /** Empty until the person decides, because the bridge refuses to guess for them. */
  let choice = $state<'' | 'continue' | 'request_stop'>('')

  /**
   * The merge a person is confirming. It holds the reviewed identities and the
   * strategy as they were displayed, so what is sent is what was read, even if
   * a poll lands while the confirmation is open.
   */
  interface Confirmation {
    origin: 'selected' | 'remaining'
    completeAfter: boolean
    strategy: FactoryMergeStrategy
    runChoice: '' | 'continue' | 'request_stop'
    links: FactoryPRLink[]
    prs: FactoryCompletionPR[]
  }
  let confirming = $state<Confirmation | null>(null)
  let confirmEl = $state<HTMLElement | null>(null)

  // Focus moves into the confirmation, so a keyboard or screen-reader user
  // lands on the decision they opened rather than at the top of the panel.
  $effect(() => {
    if (confirming && confirmEl) confirmEl.focus()
  })

  function toggle(link: FactoryPRLink, on: boolean) {
    const key = keyOf(link)
    if (!on) { chosen = chosen.filter((candidate) => candidate !== key); return }
    if (!chosen.includes(key)) chosen = [...chosen, key]
  }

  /** Move one chosen pull request one place, keeping every other relative order. */
  function moveKey(key: string, delta: number) {
    const at = chosen.indexOf(key), to = at + delta
    if (at < 0 || to < 0 || to >= chosen.length) return
    const next = [...chosen]
    next.splice(at, 1)
    next.splice(to, 0, key)
    chosen = next
  }

  function linkPR() {
    const link = reference.trim()
    if (!link) return
    void run('link_pr', { completion: { link } })
  }

  /** Whether this set of merges could be authorized right now, and why not. */
  const staleSelection = $derived(linksStale(orderedLinks))
  const mergeBlocked = $derived(busy || !orderedLinks.length || staleSelection || (needsRunChoice && !choice))

  /*
    Open the confirmation on exactly the identities displayed. A batch is only
    ever sent from here, so the reviewed head, base and node are always the
    ones the person just read.
  */
  function authorize(target: FactoryPRLink[], completeAfter: boolean, origin: 'selected' | 'remaining', useStrategy: FactoryMergeStrategy) {
    if (busy || !target.length || linksStale(target)) return
    if (needsRunChoice && !choice) return
    confirming = {
      origin,
      completeAfter,
      strategy: useStrategy,
      runChoice: choice,
      links: [...target],
      prs: target.map(reviewedPR),
    }
  }

  function cancel() {
    confirming = null
  }

  function onConfirmKey(event: KeyboardEvent) {
    if (event.key !== 'Escape') return
    event.preventDefault()
    cancel()
  }

  function confirm() {
    const pending = confirming
    if (!pending) return
    confirming = null
    const completionCommand: FactoryCompletionCommand = {
      prs: pending.prs,
      strategy: pending.strategy,
      completeAfter: pending.completeAfter,
      ...(pending.runChoice ? { runChoice: pending.runChoice } : {}),
    }
    void run(pending.completeAfter ? 'merge_and_complete' : 'merge_pr', { completion: completionCommand })
  }

  function assessmentLabel(state: FactoryAssessment): string {
    switch (state.status) {
      case 'pending': return 'Assessment pending'
      case 'available': return state.fresh ? 'Assessment available' : 'Assessment available — stale'
      case 'failed': return 'Assessment failed'
      case 'unavailable': return 'Assessment unavailable'
      default: return state.status || 'unknown'
    }
  }

  function assessmentTone(state: FactoryAssessment): Tone {
    if (state.status === 'available') return state.fresh ? 'ok' : 'warn'
    if (state.status === 'failed') return 'err'
    if (state.status === 'pending') return 'info'
    return 'neutral'
  }

  /** What the batch's own receipt says about one entry. Only `confirmed` merged. */
  function stepLabel(state: string): string {
    switch (state) {
      case 'confirmed': return 'confirmed'
      case 'failed': return 'failed'
      case 'uncertain': return 'uncertain'
      case 'dispatched': return 'dispatched — not confirmed'
      case 'prepared': return 'prepared — not confirmed'
      case 'unattempted': return 'not attempted'
      default: return state || 'unknown'
    }
  }

  function stepTone(state: string): Tone {
    switch (state) {
      case 'confirmed': return 'ok'
      case 'failed': return 'err'
      case 'uncertain': return 'warn'
      case 'dispatched':
      case 'prepared': return 'info'
      default: return 'neutral'
    }
  }

  function batchTone(state: string): Tone {
    if (state === 'complete') return 'ok'
    if (state === 'stopped') return 'warn'
    return 'info'
  }

  /** The strategy a batch authorized, which a retry of its remainder keeps. */
  const batchStrategy = $derived<FactoryMergeStrategy>(batch?.steps[0]?.strategy ?? 'merge')
  /** What a stopped or partly failed batch has left, in the order it authorized. */
  const remaining = $derived(remainingMergeLinks(item))
  const remainingStale = $derived(linksStale(remaining))
  const retryBlocked = $derived(busy || !remaining.length || remainingStale || (needsRunChoice && !choice))
  /** True while the batch still has something a person could act on. */
  const recoverable = $derived(!!batch && (batch.state !== 'complete' || (batch.steps ?? []).some((step) => step.state !== 'confirmed')))
  const hasUncertain = $derived((batch?.steps ?? []).some((step) => step.state === 'uncertain'))

  /** Everything completing without merging would leave behind, said plainly. */
  const completionNotes = $derived.by(() => {
    const notes: string[] = []
    const open = selectable.length
    if (open) notes.push(`${open} linked pull request${open === 1 ? ' is' : 's are'} still open. Completing records your decision; it neither merges nor closes ${open === 1 ? 'it' : 'them'}.`)
    if (linkStale) notes.push('Some linked evidence is stale. Refresh it before merging; completing is still allowed.')
    if (!assessment) notes.push('No completion assessment has been recorded, so completing without one is allowed and recorded as such.')
    else if (!assessment.fresh) notes.push('The assessment was made against evidence that has since moved, so it is shown as stale.')
    return notes
  })

  const stepStrategy = (step: FactoryMergeStep) => step.strategy || batchStrategy
</script>

<Card class="grid min-w-0 gap-4" data-testid="completion-panel">
  <div class="flex flex-wrap items-baseline justify-between gap-2">
    <h2 class="font-semibold">Completion</h2>
    <div class="flex flex-wrap items-center gap-2 text-sm" data-testid="completion-status">
      <Tag tone={status === 'complete' ? 'ok' : 'neutral'}>{status === 'complete' ? 'Complete' : 'Open'}</Tag>
      <span class="text-muted">Run: <span class="break-all" data-testid="completion-execution">{item.state || 'unknown'}</span></span>
    </div>
  </div>
  <p class="text-sm text-muted">Done is your decision about the work, and it is recorded separately from what the run is doing.</p>

  {#if stopState}
    <!-- The stop is its own fact: it says what happened to the run, never
         whether the work item is complete. -->
    <p class="text-sm" data-testid="completion-stop-state" data-state={stopState}>
      Run stop: {stopState === 'requested'
        ? 'requested — the workers have not confirmed stopping yet'
        : stopState === 'stopped'
          ? 'stopped'
          : stopState === 'failed'
            ? 'failed — the run could not be stopped'
            : 'unknown — the bridge cannot tell whether the run stopped'}.
    </p>
  {/if}

  <dl class="grid min-w-0 gap-1 text-sm">
    <dt class="text-muted">Goal</dt>
    <dd class="min-w-0 break-words" data-testid="completion-goal">{goal}</dd>
    <dt class="text-muted">Criteria</dt>
    <dd class="min-w-0 whitespace-pre-wrap break-words" data-testid="completion-criteria">
      {criteria ?? 'Unknown — no approved revision records what this work must satisfy.'}
    </dd>
  </dl>

  <section class="grid min-w-0 gap-2" aria-label="Completion assessment" data-testid="completion-assessment">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h3 class="font-semibold">Assessment</h3>
      <Button variant="ghost" disabled={busy} onclick={() => void run('assess_completion')}>Assess completion</Button>
    </div>
    {#if assessment}
      <div class="flex flex-wrap items-center gap-2">
        <Tag tone={assessmentTone(assessment)}>{assessmentLabel(assessment)}</Tag>
        {#if assessment.source}<span class="break-all text-xs text-muted">{assessment.source}</span>{/if}
      </div>
      {#if assessment.summary}<p class="whitespace-pre-wrap break-words text-sm">{assessment.summary}</p>{/if}
      <p class="text-xs text-muted">
        Observed {assessment.observedAt || 'at an unknown time'}{assessment.fresh
          ? '. It judges the current inputs.'
          : '. It is stale: the inputs it judged have changed since, and it does not describe this work now.'}
      </p>
    {:else}
      <p class="text-sm text-muted">No assessment recorded. Asking for one is evidence about the work; it neither completes nor reopens it.</p>
    {/if}
  </section>

  <section class="grid min-w-0 gap-2" aria-label="Linked pull requests" data-testid="completion-links">
    <h3 class="font-semibold">Linked pull requests</h3>
    {#if !links.length}
      <p class="text-sm text-muted">No pull requests are linked to this work item yet.</p>
    {:else}
      <ul class="grid min-w-0 gap-2">
        {#each links as link (keyOf(link))}
          <li class="grid min-w-0 gap-1 rounded border border-border p-2 text-sm" data-testid="completion-link" data-state={link.pr.state} data-fresh={link.fresh ? 'true' : 'false'}>
            <div class="flex flex-wrap items-center gap-2">
              {#if link.pr.state === 'open'}
                <input
                  type="checkbox"
                  class="h-4 w-4"
                  aria-label={`Select PR #${link.pr.number} for merging`}
                  checked={chosen.includes(keyOf(link))}
                  disabled={busy}
                  onchange={(event) => toggle(link, event.currentTarget.checked)}
                />
              {/if}
              <span class="font-semibold">PR #{link.pr.number}</span>
              <Tag tone={link.pr.state === 'open' ? 'ok' : 'neutral'}>{link.pr.state || 'unknown'}</Tag>
              <span class="text-xs text-muted">
                {link.origin === 'factory' ? 'opened by the factory' : link.origin === 'user' ? 'linked by a person' : `origin ${link.origin || 'unknown'}`}
              </span>
              {#if link.fresh === false}<Tag tone="warn">stale</Tag>{/if}
            </div>
            <p class="break-all text-xs text-muted"><code>{link.pr.headRef || 'unknown'}</code> → <code>{link.pr.baseRef || 'unknown'}</code> · head <code>{revisionLabel(link.pr.headSha)}</code></p>
            <p class="text-xs text-muted" data-testid="completion-link-observation">
              Observed {link.observedAt || 'at an unknown time'}{link.fresh === false ? ' — that observation no longer describes this pull request.' : '.'}
            </p>
          </li>
        {/each}
      </ul>
    {/if}
    <div class="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-end">
      <label class="grid gap-1 text-sm">Pull request number or URL
        <input class="min-w-0 rounded border border-border bg-bg p-2" bind:value={reference} placeholder="12 or https://host/owner/repo/pull/12" />
      </label>
      <Button disabled={busy || !reference.trim()} onclick={linkPR}>Link PR</Button>
    </div>
    <div class="flex flex-wrap items-center gap-2">
      <Button variant="ghost" disabled={busy} onclick={() => void run('refresh_pr_evidence')}>Refresh evidence</Button>
      {#if linkStale}<span class="text-sm text-warn">Some linked evidence is stale.</span>{/if}
    </div>
  </section>

  {#if selectable.length}
    <section class="grid min-w-0 gap-2" aria-label="Merge order" data-testid="completion-order">
      <h3 class="font-semibold">Merge order</h3>
      {#if !orderedLinks.length}
        <p class="text-sm text-muted">Select the pull requests to merge, in the order they should merge. Only open pull requests can be merged.</p>
      {:else}
        <ol class="grid min-w-0 gap-2">
          {#each orderedLinks as link, index (keyOf(link))}
            {@const at = chosen.indexOf(keyOf(link))}
            <li class="flex min-w-0 flex-wrap items-center gap-2 rounded border border-border p-2 text-sm" data-testid="completion-order-item">
              <span class="font-semibold">{index + 1}. PR #{link.pr.number}</span>
              <span class="break-all text-xs text-muted">head <code>{revisionLabel(link.pr.headSha)}</code> → <code>{link.pr.baseRef || 'unknown'}</code></span>
              <Button variant="ghost" disabled={busy || at <= 0} aria-label={`Move PR #${link.pr.number} up`} onclick={() => moveKey(keyOf(link), -1)}>Move up</Button>
              <Button variant="ghost" disabled={busy || at >= chosen.length - 1} aria-label={`Move PR #${link.pr.number} down`} onclick={() => moveKey(keyOf(link), 1)}>Move down</Button>
            </li>
          {/each}
        </ol>
      {/if}

      <label class="grid gap-1 text-sm sm:max-w-xs">Strategy
        <select class="rounded border border-border bg-bg p-2" bind:value={strategy} disabled={busy}>
          <option value="merge">merge</option>
          <option value="squash">squash</option>
          <option value="rebase">rebase</option>
        </select>
      </label>

      {#if needsRunChoice}
        <!-- The bridge refuses a batch that leaves the item's own run
             undecided, so the choice is made here and travels with the merge. -->
        <fieldset class="grid gap-1 rounded border border-attention p-2" data-testid="completion-run-choice">
          <legend class="text-sm font-semibold">This work item's run is still active</legend>
          <p class="text-sm text-muted">Say what happens to it. The merge record cannot leave the run undecided.</p>
          <label class="flex items-center gap-2 text-sm"><input type="radio" name="completion-run-choice" value="continue" bind:group={choice} disabled={busy} /> Continue the run</label>
          <label class="flex items-center gap-2 text-sm"><input type="radio" name="completion-run-choice" value="request_stop" bind:group={choice} disabled={busy} /> Request stop</label>
        </fieldset>
      {/if}

      {#if staleSelection}
        <p class="text-sm text-warn" data-testid="completion-stale-block">Evidence for a selected pull request is stale. Refresh evidence before merging; the bridge would refuse the review as it stands.</p>
      {/if}

      <div class="flex flex-wrap gap-2">
        <Button disabled={mergeBlocked} onclick={() => authorize(orderedLinks, false, 'selected', strategy)}>Merge selected</Button>
        <Button disabled={mergeBlocked} onclick={() => authorize(orderedLinks, true, 'selected', strategy)}>Merge selected and complete</Button>
      </div>
    </section>
  {/if}

  {#if confirming}
    <!-- Focus moves here and Escape leaves; nothing is sent until the person
         confirms the exact order, revisions and strategy they can read. -->
    <div
      class="grid min-w-0 gap-2 rounded border border-attention bg-raise p-3"
      role="dialog"
      aria-labelledby="completion-confirm-title"
      tabindex="-1"
      data-testid="completion-confirm"
      bind:this={confirmEl}
      onkeydown={onConfirmKey}
    >
      <h3 id="completion-confirm-title" class="font-semibold">
        {confirming.completeAfter ? 'Merge these pull requests and complete' : 'Merge these pull requests'}
      </h3>
      <p class="text-sm">
        {confirming.origin === 'remaining' ? 'The remaining pull requests of the authorized batch' : 'The selected pull requests'}
        merge in this order with the <strong>{confirming.strategy}</strong> strategy{confirming.completeAfter
          ? ', and the work item is completed once every merge confirms'
          : ', and the work item stays open'}.
      </p>
      <ol class="grid min-w-0 gap-1 text-sm">
        {#each confirming.links as link, index (keyOf(link))}
          <li class="break-all" data-testid="completion-confirm-pr">
            {index + 1}. PR #{link.pr.number} — head <code>{revisionLabel(link.pr.headSha)}</code> → base <code>{link.pr.baseRef || 'unknown'}</code>
          </li>
        {/each}
      </ol>
      {#if confirming.runChoice}
        <p class="text-sm">Run: {confirming.runChoice === 'request_stop' ? 'stop requested' : 'continues'}.</p>
      {/if}
      <p class="text-xs text-muted">These are the revisions you reviewed. A pull request whose head or base has moved is refused rather than merged under a revision nobody saw.</p>
      <div class="flex flex-wrap gap-2">
        <Button variant="ghost" onclick={cancel}>Back</Button>
        <Button disabled={busy} onclick={confirm}>{confirming.completeAfter ? 'Confirm merge and complete' : 'Confirm merge'}</Button>
      </div>
    </div>
  {/if}

  {#if batch}
    <section class="grid min-w-0 gap-2" aria-label="Merge batch" data-testid="completion-batch">
      <div class="flex flex-wrap items-center gap-2">
        <h3 class="font-semibold">Merge batch</h3>
        <Tag tone={batchTone(batch.state)}>{batch.state || 'unknown'}</Tag>
        <span class="text-xs text-muted">{batch.completeAfter
          ? 'Completes the work item once every merge confirms.'
          : 'Merges only; it completes nothing.'}</span>
      </div>
      <ol class="grid min-w-0 gap-1">
        {#each batch.steps as step (step.repoId + '#' + step.number)}
          <li class="grid min-w-0 gap-1 rounded border border-border p-2 text-sm" data-testid="completion-batch-step" data-state={step.state}>
            <div class="flex flex-wrap items-center gap-2">
              <span class="font-semibold">PR #{step.number}</span>
              <Tag tone={stepTone(step.state)}>{stepLabel(step.state)}</Tag>
            </div>
            <p class="break-all text-xs text-muted">head <code>{revisionLabel(step.headSha)}</code> → base <code>{step.baseRef || 'unknown'}</code> · {stepStrategy(step)}</p>
            {#if step.result}<p class="break-words text-xs">{step.result}</p>{/if}
          </li>
        {/each}
      </ol>
      {#if hasUncertain}
        <p class="text-sm text-warn" data-testid="completion-uncertain">An uncertain merge is read from the forge again before it is believed or retried. It is never sent a second time from here.</p>
      {/if}
      {#if recoverable}
        <div class="grid min-w-0 gap-2" data-testid="completion-batch-recovery">
          <p class="text-sm">A stopped batch resumes only on a new decision. Refresh evidence first, then retry what is left, or complete with the pull requests still open.</p>
          <div class="flex flex-wrap gap-2">
            <Button variant="ghost" disabled={busy} aria-label="Refresh pull request evidence for the remaining batch" onclick={() => void run('refresh_pr_evidence')}>Refresh evidence</Button>
            <Button
              disabled={retryBlocked}
              onclick={() => authorize(remaining, batch.completeAfter, 'remaining', batchStrategy)}
            >Retry remaining</Button>
            <Button variant="ghost" disabled={busy} onclick={() => void run('complete')}>Complete with open PRs</Button>
          </div>
          {#if remainingStale}<p class="text-sm text-warn">The remaining evidence is stale. Refresh it before retrying.</p>{/if}
          {#if !remaining.length}<p class="text-sm text-muted">No open pull request is left to retry.</p>{/if}
        </div>
      {/if}
    </section>
  {/if}

  <section class="grid min-w-0 gap-2" aria-label="Completion decision" data-testid="completion-decision">
    {#if status === 'complete'}
      <p class="text-sm">This work item is marked complete. Reopening records your decision; it starts no work, and the records that called it done stay as they are.</p>
      <div><Button variant="ghost" disabled={busy} onclick={() => void run('reopen')}>Reopen work item</Button></div>
    {:else}
      {#if completionNotes.length}
        <ul class="grid gap-1 text-sm text-muted">
          {#each completionNotes as note}
            <li data-testid="completion-note">{note}</li>
          {/each}
        </ul>
      {/if}
      <div><Button disabled={busy} onclick={() => void run('complete')}>Complete work item</Button></div>
    {/if}
  </section>
</Card>
