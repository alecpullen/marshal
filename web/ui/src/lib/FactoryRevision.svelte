<script lang="ts">
  import Card from './ui/Card.svelte'
  import Button from './ui/Button.svelte'
  import Tag from './ui/Tag.svelte'
  import {
    DELIVERY_STRATEGIES,
    assessmentPending,
    assessmentRequestable,
    assessmentRetryable,
    assessmentStateLabel,
    assessmentUnavailable,
    classificationLabel,
    deliveryReceipt,
    factoryPlanHash,
    latestProposal,
    matchingAssessment,
    proposalIsLive,
    proposalStatusLabel,
    recommendationLabel,
    strategyLabel,
    unresolvedProposal,
  } from './factory'
  import type {
    FactoryCreate,
    FactoryItem,
    FactoryRevisionCommand,
    FactoryRevisionCommandKind,
    FactoryRevisionDelivery,
    FactoryRevisionProposal,
    FactoryRevisionRun,
  } from './factory'

  let {
    item,
    busy = false,
    autoAssess = true,
    autoAssessKnown = true,
    run,
  }: {
    item: FactoryItem
    busy?: boolean
    /**
     * The project's effective automatic-assessment value, as project settings
     * resolve it: absent means on. Shown as the effective value, so a project
     * that turned assessment off is not described as assessing.
     */
    autoAssess?: boolean
    /**
     * False when this project's settings could not be read. The screen then
     * says it could not read the setting instead of claiming the effective
     * value, because "on" is a claim about a file nobody managed to open.
     */
    autoAssessKnown?: boolean
    run: FactoryRevisionRun
  } = $props()

  const approved = $derived(item.approvals?.at(-1) ?? null)
  const proposal = $derived(latestProposal(item))
  /** What the assessor recorded, shown as history whether or not it still applies. */
  const assessment = $derived(proposal?.assessment ?? null)
  /** The advice that may seed a strategy: only an assessment of these exact bytes. */
  const advice = $derived(matchingAssessment(proposal))
  const receipt = $derived(proposal?.delivery ? deliveryReceipt(proposal.delivery) : null)
  /** A proposal still accepting a decision, which is what the actions apply to. */
  const live = $derived(!!proposal && proposalIsLive(proposal))
  /**
   * An authorization is only accepted while the proposal is undecided, and the
   * bridge refuses one from anywhere else, so the control says so up front.
   */
  const canAuthorize = $derived(!!proposal && (proposal.status === 'proposed' || proposal.status === 'awaiting_authorization'))
  /** The bridge accepts a first request, or an explicit retry, from these states and no others. */
  const canRequestAssessment = $derived(!!proposal && assessmentRequestable(proposal))
  const canRetryAssessment = $derived(!!proposal && assessmentRetryable(proposal))

  /*
    The proposal's documents as the bridge last reported them. They are what
    "saved" means: an edit that matches them is already on the bridge, and an
    edit that does not is what a save would send.
  */
  const storedPlan = $derived(proposal ? proposal.plan : (item.draft?.plan ?? ''))
  const storedSpec = $derived(proposal ? proposal.spec : (item.draft?.spec ?? ''))

  let plan = $state('')
  let spec = $state('')
  /*
    The values last adopted from the item, so a poll can move an untouched
    draft forward while a draft someone is typing in is never overwritten.
  */
  let adopted = $state({ plan: '', spec: '' })
  let adoptedKey = $state<string | null>(null)
  $effect(() => {
    const key = proposal ? proposal.id : ''
    if (key === adoptedKey) return
    const untouched = plan === adopted.plan && spec === adopted.spec
    adoptedKey = key
    adopted = { plan: storedPlan, spec: storedSpec }
    if (!untouched) return
    plan = storedPlan
    spec = storedSpec
  })

  const dirty = $derived(plan !== storedPlan || spec !== storedSpec)

  /**
   * The delivery strategy someone picked. It starts empty when nothing judged
   * this exact candidate: the bridge refuses an authorization that names no
   * strategy, so the person makes the choice instead of a default deciding for
   * them.
   */
  let strategy = $state<FactoryRevisionDelivery['strategy'] | ''>('')
  let strategyKey = $state<string | null>(null)
  let followUpConfirmed = $state(false)
  let failed = $state(false)
  /** True while this component's own multi-step path is in flight. */
  let working = $state(false)
  const blocked = $derived(busy || working)

  /*
    Seeded once per proposal id, from what the bridge has recorded: the choice
    already made, else the assessment of these exact bytes. A poll must never
    revert a choice someone has made and not yet sent, and moving to another
    proposal must not carry a confirmation across.
  */
  $effect(() => {
    const key = proposal ? proposal.id : ''
    if (key === strategyKey) return
    strategyKey = key
    strategy = proposal?.delivery?.strategy ?? advice?.strategy ?? ''
    followUpConfirmed = false
  })

  /** One command's trip to the bridge: the item it answered with, or null when it was refused. */
  async function send(kind: FactoryRevisionCommandKind, revision: FactoryRevisionCommand): Promise<FactoryItem | null> {
    failed = false
    let next: FactoryItem | null = null
    try {
      next = await run(kind, revision)
    } catch {
      next = null
    }
    if (!next) {
      failed = true
      return null
    }
    // A confirmation is spent by the command it authorized, and by nothing else.
    if (kind === 'revision_follow_up_confirm') followUpConfirmed = false
    return next
  }

  /** One deliberate action at a time, so a double click cannot send two of anything. */
  async function act(action: () => Promise<void>) {
    if (blocked) return
    working = true
    try {
      await action()
    } finally {
      working = false
    }
  }

  /** The candidate a proposal command names, and the version it was reviewed at. */
  function proposalBody(p: FactoryRevisionProposal): FactoryRevisionCommand {
    const body: FactoryRevisionCommand = { proposalId: p.id, proposalVersion: p.version }
    if (p.candidateHash) body.candidateHash = p.candidateHash
    return body
  }

  /** The base a new proposal is written against: the newest approval the bridge returned. */
  function baseOf(source: FactoryItem): Pick<FactoryRevisionCommand, 'baseRevision' | 'baseHash'> {
    const active = source.approvals?.at(-1) ?? null
    return active ? { baseRevision: active.revision, baseHash: active.planHash } : { baseRevision: 0 }
  }

  /**
   * Saving a proposal records a candidate. It approves nothing and starts
   * nothing: those are separate commands, taken by separate controls.
   *
   * The bridge holds only one unresolved proposal per item and refuses a second
   * with "supersede it explicitly", so a live one is retired first and the new
   * proposal is written against the item that command returned. The candidate
   * hash binds the exact bytes, which is what the bridge checks before it
   * records anything.
   */
  function saveProposal() {
    void act(async () => {
      const candidateHash = await factoryPlanHash(plan, spec)
      let source = item
      const unresolved = unresolvedProposal(source)
      if (unresolved) {
        const superseded = await send('revision_supersede', proposalBody(unresolved))
        if (!superseded) return
        source = superseded
      }
      const body: FactoryRevisionCommand = { plan, spec, candidateHash, ...baseOf(source) }
      await send('revision_propose', body)
    })
  }

  /**
   * A first request names no attempt: the bridge derives the intent from the
   * proposal, and an id of our own would be a second intent it refuses. A lost
   * or uncertain attempt is retired by naming the attempt the bridge recorded,
   * which is the only retry it accepts.
   */
  function requestAssessment() {
    if (proposal == null) return
    const target: FactoryRevisionProposal = proposal
    void act(async () => {
      if (canRetryAssessment) {
        if (!target.assessmentAttemptId) return
        await send('revision_assess_retry', { ...proposalBody(target), attemptId: target.assessmentAttemptId })
        return
      }
      await send('revision_assess_request', proposalBody(target))
    })
  }

  /**
   * The explicit human authorization of these exact bytes, as an amendment or
   * as a follow-up. It names the candidate, the base it was written against,
   * the routing and the delivery strategy: the bridge checks all four, and it
   * takes the strategy from the assessment only when that assessment judged
   * this candidate at this base.
   */
  function authorizationBody(p: FactoryRevisionProposal, routing: 'amend' | 'follow_up'): FactoryRevisionCommand {
    const body: FactoryRevisionCommand = {
      ...proposalBody(p),
      baseRevision: p.baseRevision,
      baseHash: p.baseHash,
      routing,
    }
    if (strategy) body.strategy = strategy
    return body
  }

  /** The explicit human approval of this candidate, delivered as an amendment. */
  function approveRevision() {
    if (proposal == null) return
    const target: FactoryRevisionProposal = proposal
    if (!canAuthorize || !strategy) return
    void act(async () => { await send('revision_authorize', authorizationBody(target, 'amend')) })
  }

  /** Retiring a proposal changes no approval and starts nothing. */
  function retireProposal(kind: 'revision_reject' | 'revision_supersede') {
    if (proposal == null) return
    const target: FactoryRevisionProposal = proposal
    void act(async () => { await send(kind, proposalBody(target)) })
  }

  function chooseDelivery() {
    if (proposal == null) return
    const target: FactoryRevisionProposal = proposal
    if (!strategy) return
    void act(async () => {
      await send('revision_delivery_choice', { ...proposalBody(target), strategy })
    })
  }

  /** A follow-up is separate work, so it carries its own create request. */
  function followUpRequest(): FactoryCreate {
    return {
      requestId: crypto.randomUUID(),
      title: `${item.title} (follow-up)`,
      plan,
      spec,
      config: item.draft.config,
    }
  }

  /**
   * A follow-up is two commands, in this order: the authorization routing the
   * proposal to a follow-up (which is what records the intent), and then the
   * confirmation of the new work item, named against the version the
   * authorization returned. Without the first, the bridge refuses the second
   * for carrying no intent.
   */
  function confirmFollowUp() {
    if (proposal == null) return
    const target: FactoryRevisionProposal = proposal
    if (!canAuthorize || !strategy || !followUpConfirmed) return
    void act(async () => {
      const routed = await send('revision_authorize', authorizationBody(target, 'follow_up'))
      if (!routed) return
      const confirmed = latestProposal(routed)
      if (!confirmed || confirmed.id !== target.id) return
      await send('revision_follow_up_confirm', { ...proposalBody(confirmed), followUp: followUpRequest() })
    })
  }
</script>

<Card class="grid min-w-0 gap-4" data-testid="factory-revision">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <h2 class="font-semibold">Plan changes</h2>
    {#if proposal}
      <Tag tone={proposalIsLive(proposal) ? 'accent' : 'neutral'}>{proposalStatusLabel(proposal.status)}</Tag>
    {:else}
      <Tag tone="neutral">No proposal</Tag>
    {/if}
  </div>
  {#if failed}
    <p class="text-sm text-warn" role="status" data-testid="revision-failed">
      That was refused. Your text is kept, so you can review the refreshed item and try again.
    </p>
  {/if}

  <!--
    The comparison is one grid that stacks on a phone and sits side by side
    from a desktop width up. Each half is a labelled region, so a screen reader
    says which revision it is reading and never merges the two.
  -->
  <div class="grid min-w-0 gap-4 lg:grid-cols-2">
    <section
      class="grid min-w-0 content-start gap-2 rounded-md border border-border p-3"
      data-testid="revision-current"
      aria-label={approved ? `Approved revision ${approved.revision}` : 'No approved revision'}
    >
      <div class="flex flex-wrap items-center gap-2">
        <h3 class="text-sm font-semibold">{approved ? `Approved revision ${approved.revision}` : 'No approved revision yet'}</h3>
        {#if approved}<Tag tone="ok">approved</Tag>{/if}
      </div>
      {#if approved}
        <!-- Evidence is recorded against this revision's hash, and against it
             only: it is not coverage for a candidate nobody has run. -->
        <p class="text-xs text-muted" data-testid="revision-current-evidence">
          Any evidence, CI or review recorded for this revision covers revision {approved.revision} only, not a newer candidate.
        </p>
        <p class="whitespace-pre-wrap break-words text-sm" data-testid="revision-current-plan">{approved.plan || 'No plan recorded.'}</p>
        {#if approved.spec}
          <p class="whitespace-pre-wrap break-words text-sm text-muted" data-testid="revision-current-spec">{approved.spec}</p>
        {/if}
        <p class="font-mono text-xs text-muted">
          {approved.planHash}{#if approved.approvedAt} · approved {approved.approvedAt}{/if}
        </p>
      {:else}
        <p class="text-sm text-muted">This item has no approved revision, so there is no evidence to carry forward.</p>
      {/if}
    </section>

    <section
      class="grid min-w-0 content-start gap-2 rounded-md border border-border p-3"
      data-testid="revision-proposed"
      aria-label="Proposed revision"
    >
      <div class="flex flex-wrap items-center gap-2">
        <h3 class="text-sm font-semibold">{proposal ? 'Proposed revision' : 'Propose a revision'}</h3>
        {#if proposal}<Tag tone="neutral">candidate {proposal.candidateHash || 'unhashed'}</Tag>{/if}
      </div>
      <p class="text-xs text-muted" data-testid="revision-coverage">
        {#if approved}
          This candidate is not covered by revision {approved.revision}'s evidence: nothing has run against it yet.
        {:else}
          Nothing has run against this candidate yet.
        {/if}
      </p>
      <label class="grid min-w-0 gap-1 text-xs">Proposed plan
        <textarea
          aria-label="Proposed plan"
          class="min-h-32 w-full min-w-0 rounded border border-border bg-bg p-2 font-mono text-sm"
          bind:value={plan}
        ></textarea>
      </label>
      <label class="grid min-w-0 gap-1 text-xs">Proposed specification
        <textarea
          aria-label="Proposed specification"
          class="min-h-24 w-full min-w-0 rounded border border-border bg-bg p-2 font-mono text-sm"
          bind:value={spec}
        ></textarea>
      </label>
    </section>
  </div>

  <section class="grid min-w-0 gap-2" data-testid="revision-assessment" aria-label="Assessment">
    <div class="flex flex-wrap items-center gap-2">
      <h3 class="text-sm font-semibold">Assessment</h3>
      {#if proposal}<Tag tone={assessmentUnavailable(proposal) ? 'warn' : 'neutral'}>{assessmentStateLabel(proposal.assessmentState)}</Tag>{/if}
    </div>
    <!-- The effective project value, not a local default: an absent setting
         means on, and only a deliberate false turns it off. A setting that
         could not be read is said to be unread, never reported as on. -->
    <p class="text-xs text-muted" data-testid="revision-auto-assess">
      {#if !autoAssessKnown}
        Could not read this project's automatic-assessment setting. Nothing here claims it is on or off; the control below asks explicitly.
      {:else if autoAssess}
        Automatic assessment: on. Saving a proposal asks the planner's assessor for a classification and a routing recommendation.
      {:else}
        Automatic assessment: off for this project. Nothing is assessed unless you ask for an assessment below.
      {/if}
    </p>
    {#if assessment}
      <dl class="grid min-w-0 grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
        <dt class="text-muted">Classification</dt>
        <dd data-testid="revision-classification">{classificationLabel(assessment.classification)}</dd>
        <dt class="text-muted">Reason</dt>
        <dd class="whitespace-pre-wrap break-words" data-testid="revision-reason">{assessment.reason || 'No reason given.'}</dd>
        <dt class="text-muted">Recommendation</dt>
        <dd data-testid="revision-recommendation">{recommendationLabel(assessment.recommendation)}</dd>
        <dt class="text-muted">Delivery</dt>
        <dd data-testid="revision-strategy">{strategyLabel(assessment.strategy)}</dd>
        <dt class="text-muted">Assessor</dt>
        <dd class="font-mono" data-testid="revision-assessor">{assessment.assessorId || 'unreported'}</dd>
        {#if assessment.affectedWorkerId}
          <dt class="text-muted">Affected worker</dt>
          <dd class="font-mono">{assessment.affectedWorkerId}</dd>
        {/if}
      </dl>
      {#if advice === null}
        <p class="text-xs text-muted" data-testid="revision-assessment-stale">
          That assessment judged a different candidate, so it advises nothing here: choose how these bytes are delivered below.
        </p>
      {/if}
    {:else if proposal}
      <p class="text-sm text-muted" data-testid="revision-assessment-none">
        {#if assessmentPending(proposal)}
          No assessment has arrived yet. Routing the change yourself is fine; the recommendation is advice, not a gate.
        {:else if assessmentUnavailable(proposal)}
          The assessor could not answer, so this is your decision. The options below say how the change can reach the work that is running.
        {:else}
          No assessment has been requested for this candidate.
        {/if}
      </p>
    {/if}
    {#if proposal?.pauseState}
      <!-- Pause evidence is what the bridge has recorded, named as such: a
           request is not a confirmation that the work stopped. -->
      <p class="text-xs text-muted" data-testid="revision-pause">
        Interrupt evidence: the running work was asked to pause (state: {proposal.pauseState}).
        {#if proposal.pauseState !== 'paused'}It is not confirmed stopped.{/if}
      </p>
    {/if}
    {#if canRequestAssessment || canRetryAssessment}
      <div>
        <!-- Offered only in the states the bridge accepts: a first request from
             "not requested" or "planner unavailable", a retry only of an attempt
             it still holds. -->
        <Button
          variant="ghost"
          onclick={requestAssessment}
          disabled={blocked || !live || (canRetryAssessment && !proposal?.assessmentAttemptId)}
        >{canRetryAssessment ? 'Retry the assessment' : 'Request assessment'}</Button>
      </div>
    {:else if proposal?.assessmentState === 'failed'}
      <p class="text-xs text-muted" data-testid="revision-assessment-failed">
        The assessor failed on this candidate, and the bridge retires a lost attempt rather than a failed one. Save a new proposal to ask again.
      </p>
    {/if}
  </section>

  {#if proposal?.delivery && receipt}
    <!-- The real receipt, and what it means. Only `delivered` is a delivery;
         every earlier state is a receipt about the request. -->
    <section class="grid min-w-0 gap-1 rounded-md border border-border p-3" data-testid="revision-receipt" aria-label="Delivery receipt">
      <div class="flex flex-wrap items-center gap-2">
        <h3 class="text-sm font-semibold">Delivery</h3>
        <Tag tone={receipt.tone}>{receipt.headline}</Tag>
        <Tag tone="neutral">{strategyLabel(proposal.delivery.strategy)}</Tag>
      </div>
      <p class="text-sm" data-testid="revision-receipt-meaning">{receipt.meaning}</p>
    </section>
  {/if}

  {#if proposal && !live}
    <p class="text-sm text-muted">This proposal is no longer live, so it cannot be approved, amended or followed up. Save a new proposal to change the plan again.</p>
  {/if}

  <div class="flex flex-wrap items-center gap-3">
    <Button onclick={saveProposal} disabled={blocked || !dirty}>Save proposal</Button>
    <span class="text-xs text-muted" data-testid="revision-save-note">Saving records a candidate. It approves nothing and starts nothing.</span>
  </div>

  {#if proposal && live}
    <div class="flex flex-wrap items-center gap-3 border-t border-border pt-3">
      <Button variant="ghost" onclick={approveRevision} disabled={blocked || dirty || !canAuthorize || !strategy}>Approve this revision</Button>
      {#if dirty}<span class="text-xs text-muted">Save your changes before approving them.</span>{/if}
      {#if !dirty && canAuthorize && !strategy}
        <span class="text-xs text-muted" data-testid="revision-strategy-needed">Choose how to deliver it below before approving: nothing has assessed this candidate.</span>
      {/if}
    </div>

    <fieldset class="grid min-w-0 gap-2" data-testid="revision-delivery-choices">
      <legend class="text-sm font-semibold">How to deliver it</legend>
      <p class="text-xs text-muted">
        {#if advice}
          The assessor recommends {strategyLabel(advice.strategy)}. You can still choose another strategy.
        {:else}
          Nothing has been assessed for these exact bytes, so choose how this reaches the running work. Waiting for a safe boundary is the least disruptive; interrupting stops the current step.
        {/if}
      </p>
      {#each DELIVERY_STRATEGIES as option (option)}
        <label class="flex items-center gap-2 text-sm">
          <input type="radio" name="revision-strategy" value={option} checked={strategy === option} onchange={() => (strategy = option)} disabled={blocked} />
          {strategyLabel(option)}
        </label>
      {/each}
      <div>
        <Button variant="ghost" onclick={chooseDelivery} disabled={blocked || !strategy}>Record delivery choice</Button>
      </div>
    </fieldset>

    <div class="grid min-w-0 gap-2 border-t border-border pt-3" data-testid="revision-follow-up">
      <h3 class="text-sm font-semibold">Or make it separate work</h3>
      <p class="text-xs text-muted">
        A follow-up leaves this item's plan alone and starts a new work item with what is written above. Nothing from this
        candidate is applied here, and the approved revision keeps running unchanged.
      </p>
      <label class="flex items-start gap-2 text-sm">
        <input type="checkbox" checked={followUpConfirmed} onchange={(e) => (followUpConfirmed = e.currentTarget.checked)} disabled={blocked} />
        I confirm this starts a separate work item instead of changing this one
      </label>
      <div>
        <Button variant="ghost" onclick={confirmFollowUp} disabled={blocked || !followUpConfirmed || !canAuthorize || !strategy}>Start a follow-up item</Button>
      </div>
    </div>

    <div class="flex flex-wrap items-center gap-3 border-t border-border pt-3" data-testid="revision-retire">
      <Button variant="ghost" onclick={() => retireProposal('revision_reject')} disabled={blocked}>Reject this proposal</Button>
      <Button variant="ghost" onclick={() => retireProposal('revision_supersede')} disabled={blocked}>Supersede it with a new one</Button>
      <span class="text-xs text-muted">
        Rejecting closes the question. Superseding retires this candidate so a new proposal can replace it. Neither approves or starts anything.
      </span>
    </div>
  {/if}
</Card>
