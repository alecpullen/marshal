<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import Button from '../ui/Button.svelte'
  import Card from '../ui/Card.svelte'
  import ProfileSelect from './ProfileSelect.svelte'
  import { profileOf } from './config'
  import { getModels } from '../api'
  import type { FactoryCommand, FactoryCreate, FactoryItem } from '../factory'

  /** C5: the fields a decision panel supplies; the caller owns request identity. */
  export interface FactoryCommandInput {
    answer?: string
    reason?: string
    additionalRounds?: number
    replacement?: FactoryCreate
    implementerRouting?: Record<string, unknown>
    reviewerRouting?: Record<string, unknown>
    confirmPreviousWorkerStopped?: boolean
  }

  export type RunFactoryCommand = (kind: FactoryCommand['kind'], input?: FactoryCommandInput) => Promise<void>

  export interface RecoveryPanelProps {
    item: FactoryItem
    busy: boolean
    run: RunFactoryCommand
  }

  /**
   * The factory's recovery controls (design C5): an additional-round grant for
   * a stopped round, and the named-worker attestation for a recovery.
   *
   * Everything someone types lives here, not in the caller, and nothing is
   * keyed to `item.version`: a poll or a stale-version refresh redraws the same
   * decision in place, so the reason, the allowance and the replacement they
   * were editing survive it. The route pickers are the real profile list from
   * the models API, seeded with the routes in force and keeping a routed
   * profile the list no longer offers visible rather than silently replacing
   * it. Only a retry reads that list; attestation never needs it.
   *
   * There is no option the bridge cannot execute: a route-only retry is the
   * fresh round the copy describes, never a continuation of the interrupted
   * conversation.
   */
  let { item, busy, run }: RecoveryPanelProps = $props()

  /** The decision kinds an additional-round grant can resume. */
  const RETRY_KINDS = ['exhausted', 'no_progress', 'worker_failed', 'invalid_result', 'ci_unavailable', 'ci_wait', 'conflict']

  const decision = $derived(item.decision)
  const kind = $derived(decision?.kind ?? '')
  const retryable = $derived(RETRY_KINDS.includes(kind))
  const recovering = $derived(kind === 'recovery')
  /** The configuration governing a retry: the latest recorded approval. */
  const latest = $derived(item.approvals?.at(-1)?.config ?? item.draft?.config ?? null)
  const currentImplementer = $derived(profileOf(latest?.implementerRouting ?? undefined))
  const currentReviewer = $derived(profileOf(latest?.reviewerRouting ?? undefined))
  const granted = $derived((item.grants ?? []).reduce((sum, grant) => sum + grant.additionalRounds, 0))
  const roundsUsed = $derived((item.rounds ?? []).length)
  const lastWorkerId = $derived(item.workers?.at(-1)?.agentId ?? '')

  /*
    The route pickers start at the routes in force, so the choice they show is
    the one that actually runs. A new decision remounts this panel, which is
    what makes that seed fresh; a refresh of the same decision does not.
  */
  const seed = untrack(() => item.approvals?.at(-1)?.config ?? item.draft?.config ?? null)
  let implementerProfile = $state(profileOf(seed?.implementerRouting ?? undefined))
  let reviewerProfile = $state(profileOf(seed?.reviewerRouting ?? undefined))
  let reason = $state(''), additionalRounds = $state(1)
  let changePlan = $state(false), replacementPlan = $state(''), replacementSpec = $state(''), replacementInstructions = $state('')
  let attest = $state(false), workerName = $state('')

  let profiles = $state<string[]>([])
  /** Loading the real profile list is a once-only, retry-only read. */
  let modelsStarted = false, alive = true
  onMount(() => () => { alive = false })
  $effect(() => {
    if (!retryable || modelsStarted) return
    modelsStarted = true
    void (async () => {
      try {
        const models = await getModels()
        if (alive) profiles = Object.keys(models.profiles ?? {}).sort()
      } catch {
        // The routes in force stay visible and usable; a failed list only
        // means no other profile is offered.
        if (alive) profiles = []
      }
    })()
  })

  function toggleChangePlan() {
    if (!changePlan) return
    const approval = item.approvals?.at(-1)
    replacementPlan = approval?.plan ?? item.draft.plan
    replacementSpec = approval?.spec ?? item.draft.spec
    replacementInstructions = approval?.config.reviewerInstructions ?? item.draft.config.reviewerInstructions
  }

  /** Whether a route is being changed, as opposed to left at the one in force. */
  const routeChange = (profile: string, current: string) => !!profile && profile !== current

  function retry() {
    const input: FactoryCommandInput = { reason, additionalRounds }
    if (changePlan) {
      const draft: FactoryCreate = {
        ...item.draft,
        requestId: crypto.randomUUID(),
        plan: replacementPlan,
        spec: replacementSpec,
        config: {
          ...latest,
          reviewerInstructions: replacementInstructions,
          implementerRouting: routeChange(implementerProfile, currentImplementer) ? { profile: implementerProfile } : latest?.implementerRouting,
          reviewerRouting: routeChange(reviewerProfile, currentReviewer) ? { profile: reviewerProfile } : latest?.reviewerRouting,
        },
      }
      input.replacement = draft
    } else {
      if (routeChange(implementerProfile, currentImplementer)) input.implementerRouting = { profile: implementerProfile }
      if (routeChange(reviewerProfile, currentReviewer)) input.reviewerRouting = { profile: reviewerProfile }
    }
    void run('retry', input)
  }

  function recover() {
    void run('recover', { confirmPreviousWorkerStopped: attest, reason: `${workerName.trim()}: ${reason.trim()}` })
  }
</script>

{#if decision}
  <Card class="grid gap-3 border-attention" data-testid="factory-recovery">
    <h2 class="font-semibold">Action needed: {decision.kind}</h2>
    {#if decision.question}<p class="whitespace-pre-wrap break-words">{decision.question}</p>{/if}
    {#if decision.reason}<p class="whitespace-pre-wrap break-words text-sm text-muted">{decision.reason}</p>{/if}
    {#if retryable}
      <p class="text-sm">{roundsUsed} rounds used · {granted} granted total. An additional allowance is required to resume work.</p>
      <label class="grid gap-1 text-sm">Reason <textarea class="rounded border border-border bg-bg p-2" bind:value={reason}></textarea></label>
      <label class="grid gap-1 text-sm">Additional rounds <input class="rounded border border-border bg-bg p-2" type="number" min="1" bind:value={additionalRounds} /></label>
      <p class="text-sm">Routes in force: implementer {currentImplementer || 'none'} · reviewer {currentReviewer || 'none'}</p>
      <ProfileSelect label="Implementer profile" bind:value={implementerProfile} profiles={profiles} followLabel="Keep the approved route" disabled={busy} />
      <ProfileSelect label="Reviewer profile" bind:value={reviewerProfile} profiles={profiles} followLabel="Keep the approved route" disabled={busy} />
      <p class="text-sm">A route-only retry starts a fresh round. The interrupted round's review is not reused, and one granted round is spent.</p>
      <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={changePlan} onchange={toggleChangePlan} /> Change approved plan or reviewer instructions</label>
      {#if changePlan}
        <details open><summary>Replacement plan and reviewer instructions</summary><div class="grid gap-2 pt-2">
          <p class="text-sm">Review these changes before submitting. Retry records a new approval revision with this plan, the selected routes and reviewer instructions before work resumes.</p>
          <label class="grid gap-1 text-sm">Replacement plan <textarea class="min-h-40 rounded border border-border bg-bg p-2" bind:value={replacementPlan}></textarea></label>
          <label class="grid gap-1 text-sm">Replacement specification <textarea class="min-h-24 rounded border border-border bg-bg p-2" bind:value={replacementSpec}></textarea></label>
          <label class="grid gap-1 text-sm">Reviewer instructions <textarea class="min-h-24 rounded border border-border bg-bg p-2" bind:value={replacementInstructions}></textarea></label>
        </div></details>
      {/if}
      <div><Button disabled={busy || !reason.trim() || additionalRounds < 1 || (changePlan && !replacementPlan.trim())} onclick={retry}>Grant rounds and retry</Button></div>
    {/if}
    {#if recovering}
      <p class="text-sm">Refresh evidence first. Attest only after independently confirming the previous named worker has stopped. Attestation cannot prove an unreachable worker stopped.</p>
      <div><Button disabled={busy} onclick={() => void run('reconcile')}>Refresh evidence</Button></div>
      <p class="break-all font-mono text-sm">Last worker agent ID: {lastWorkerId || 'Unavailable'}</p>
      <label class="grid gap-1 text-sm">Type the exact last worker agent ID <input class="rounded border border-border bg-bg p-2" bind:value={workerName} /></label>
      <label class="grid gap-1 text-sm">How you confirmed it <textarea class="rounded border border-border bg-bg p-2" bind:value={reason}></textarea></label>
      <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={attest} /> I confirm this named previous worker has stopped</label>
      <div><Button disabled={busy || !attest || !lastWorkerId || workerName.trim() !== lastWorkerId || !reason.trim()} onclick={recover}>Record named-worker attestation</Button></div>
    {/if}
  </Card>
{/if}
