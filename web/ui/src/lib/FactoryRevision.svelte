<script lang="ts">
  import Card from './ui/Card.svelte'
  import Button from './ui/Button.svelte'
  import Tag from './ui/Tag.svelte'
  import {
    DELIVERY_STRATEGIES,
    assessmentPending,
    assessmentStateLabel,
    assessmentUnavailable,
    classificationLabel,
    deliveryReceipt,
    latestProposal,
    proposalIsLive,
    proposalStatusLabel,
    recommendationLabel,
    strategyLabel,
  } from './factory'
  import type {
    FactoryCreate,
    FactoryItem,
    FactoryRevisionCommand,
    FactoryRevisionCommandKind,
    FactoryRevisionDelivery,
    FactoryRevisionRun,
  } from './factory'

  let {
    item,
    busy = false,
    autoAssess = true,
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
    run: FactoryRevisionRun
  } = $props()

  const approved = $derived(item.approvals?.at(-1) ?? null)
  const proposal = $derived(latestProposal(item))
  const assessment = $derived(proposal?.assessment ?? null)
  const receipt = $derived(proposal?.delivery ? deliveryReceipt(proposal.delivery) : null)
  /** A proposal still accepting a decision, which is what the actions apply to. */
  const live = $derived(!!proposal && proposalIsLive(proposal))
  /** True while nobody has assessed the candidate and nobody is assessing it. */
  const needsAssessment = $derived(!!proposal && !assessmentPending(proposal) && proposal.assessmentState !== 'assessed')

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

  /** The delivery strategy the person picked, seeded from the advice in force. */
  let strategy = $state<FactoryRevisionDelivery['strategy']>('wait_safe')
  let followUpConfirmed = $state(false)
  let failed = $state(false)
  $effect(() => {
    const next = proposal?.delivery?.strategy ?? assessment?.strategy
    if (next) strategy = next
  })

  async function send(kind: FactoryRevisionCommandKind, revision: FactoryRevisionCommand): Promise<boolean> {
    if (busy) return false
    failed = false
    let ok = false
    try {
      ok = await run(kind, revision)
    } catch {
      ok = false
    }
    failed = !ok
    // A confirmation is spent by the command it authorized, and by nothing else.
    if (ok && kind === 'revision_follow_up_confirm') followUpConfirmed = false
    return ok
  }

  /** The candidate a proposal command names: a live proposal's, or the approved revision it builds on. */
  function proposalBody(): FactoryRevisionCommand {
    const body: FactoryRevisionCommand = {
      plan,
      spec,
      baseRevision: proposal?.baseRevision ?? approved?.revision ?? 0,
    }
    const baseHash = proposal?.baseHash ?? approved?.planHash
    if (baseHash) body.baseHash = baseHash
    if (proposal) {
      body.proposalId = proposal.id
      body.proposalVersion = proposal.version
      if (proposal.candidateHash) body.candidateHash = proposal.candidateHash
    }
    return body
  }

  /**
   * Saving a proposal records a candidate. It approves nothing and starts
   * nothing: those are separate commands, taken by separate controls.
   */
  function saveProposal() {
    void send('revision_propose', proposalBody())
  }

  function requestAssessment() {
    if (!proposal) return
    const body: FactoryRevisionCommand = {
      proposalId: proposal.id,
      proposalVersion: proposal.version,
      attemptId: crypto.randomUUID(),
    }
    if (proposal.candidateHash) body.candidateHash = proposal.candidateHash
    void send('revision_assess_request', body)
  }

  /** The explicit human approval of this candidate, delivered as an amendment. */
  function approveRevision() {
    if (!proposal) return
    const body: FactoryRevisionCommand = {
      proposalId: proposal.id,
      proposalVersion: proposal.version,
      routing: 'amend',
    }
    if (proposal.candidateHash) body.candidateHash = proposal.candidateHash
    void send('revision_authorize', body)
  }

  function chooseDelivery() {
    if (!proposal) return
    const body: FactoryRevisionCommand = {
      proposalId: proposal.id,
      proposalVersion: proposal.version,
      strategy,
    }
    if (proposal.candidateHash) body.candidateHash = proposal.candidateHash
    void send('revision_delivery_choice', body)
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

  function confirmFollowUp() {
    if (!proposal || !followUpConfirmed) return
    const body: FactoryRevisionCommand = {
      proposalId: proposal.id,
      proposalVersion: proposal.version,
      routing: 'follow_up',
      followUp: followUpRequest(),
    }
    if (proposal.candidateHash) body.candidateHash = proposal.candidateHash
    void send('revision_follow_up_confirm', body)
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
      That was not saved. Your text is kept, so you can review the refreshed item and try again.
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
         means on, and only a deliberate false turns it off. -->
    <p class="text-xs text-muted" data-testid="revision-auto-assess">
      {#if autoAssess}
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
    {#if proposal && needsAssessment}
      <div>
        <Button variant="ghost" onclick={requestAssessment} disabled={busy || !live}>Request assessment</Button>
      </div>
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
    <Button onclick={saveProposal} disabled={busy || !dirty}>Save proposal</Button>
    <span class="text-xs text-muted" data-testid="revision-save-note">Saving records a candidate. It approves nothing and starts nothing.</span>
  </div>

  {#if proposal && live}
    <div class="flex flex-wrap items-center gap-3 border-t border-border pt-3">
      <Button variant="ghost" onclick={approveRevision} disabled={busy || dirty}>Approve this revision</Button>
      {#if dirty}<span class="text-xs text-muted">Save your changes before approving them.</span>{/if}
    </div>

    <fieldset class="grid min-w-0 gap-2" data-testid="revision-delivery-choices">
      <legend class="text-sm font-semibold">How to deliver it</legend>
      <p class="text-xs text-muted">
        {#if assessmentUnavailable(proposal) || !assessment}
          Nothing has been assessed, so choose how this reaches the running work. Waiting for a safe boundary is the least disruptive; interrupting stops the current step.
        {:else}
          The assessor recommends {strategyLabel(assessment.strategy)}. You can still choose another strategy.
        {/if}
      </p>
      {#each DELIVERY_STRATEGIES as option (option)}
        <label class="flex items-center gap-2 text-sm">
          <input type="radio" name="revision-strategy" value={option} checked={strategy === option} onchange={() => (strategy = option)} disabled={busy} />
          {strategyLabel(option)}
        </label>
      {/each}
      <div>
        <Button variant="ghost" onclick={chooseDelivery} disabled={busy}>Record delivery choice</Button>
      </div>
    </fieldset>

    <div class="grid min-w-0 gap-2 border-t border-border pt-3" data-testid="revision-follow-up">
      <h3 class="text-sm font-semibold">Or make it separate work</h3>
      <p class="text-xs text-muted">
        A follow-up leaves this item's plan alone and starts a new work item with what is written above. Nothing from this
        candidate is applied here, and the approved revision keeps running unchanged.
      </p>
      <label class="flex items-start gap-2 text-sm">
        <input type="checkbox" checked={followUpConfirmed} onchange={(e) => (followUpConfirmed = e.currentTarget.checked)} disabled={busy} />
        I confirm this starts a separate work item instead of changing this one
      </label>
      <div>
        <Button variant="ghost" onclick={confirmFollowUp} disabled={busy || !followUpConfirmed}>Start a follow-up item</Button>
      </div>
    </div>
  {/if}
</Card>
