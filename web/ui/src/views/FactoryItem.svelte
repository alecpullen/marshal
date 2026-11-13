<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Tag from '../lib/ui/Tag.svelte'
  import { APIError, commandFactoryItem, errMessage, getFactoryItem } from '../lib/api'
  import { createFactoryPoller, type FactoryCommand, type FactoryCreate, type FactoryItem } from '../lib/factory'
  let { itemId, onNavigate }: { itemId: string; onNavigate: (hash: string) => void } = $props()
  let item = $state<FactoryItem | null>(null), error = $state(''), notice = $state(''), busy = $state(false)
  let answer = $state(''), reason = $state(''), additionalRounds = $state(1)
  let changePlan = $state(false), replacementPlan = $state(''), replacementSpec = $state(''), replacementInstructions = $state('')
  let replaceImplementerProfile = $state(''), replaceReviewerProfile = $state('')
  let attest = $state(false), workerName = $state(''), pending = $state<FactoryCommand | null>(null)
  async function refresh() { item = await getFactoryItem(itemId); error = '' }
  onMount(() => { const p = createFactoryPoller(refresh, () => !!item && !['draft', 'ready', 'stopped', 'closed', 'needs_input'].includes(item.state), (e) => { error = errMessage(e) }); p.start(); return p.stop })
  async function command(kind: FactoryCommand['kind']) {
    if (!item || busy) return
    busy = true; error = ''; notice = ''
    try {
      {
        const c: FactoryCommand = { requestId: crypto.randomUUID(), kind, expectedVersion: item.version, decisionId: item.decision?.id }
        if (kind === 'answer') c.answer = answer
        if (kind === 'retry') {
          c.reason = reason; c.additionalRounds = additionalRounds
          const latest = item.approvals?.at(-1)?.config ?? item.draft.config
          if (changePlan) {
            const draft: FactoryCreate = { ...item.draft, requestId: crypto.randomUUID(), plan: replacementPlan, spec: replacementSpec,
              config: { ...latest, reviewerInstructions: replacementInstructions,
                implementerRouting: replaceImplementerProfile.trim() ? { profile: replaceImplementerProfile.trim() } : latest.implementerRouting,
                reviewerRouting: replaceReviewerProfile.trim() ? { profile: replaceReviewerProfile.trim() } : latest.reviewerRouting } }
            c.replacement = draft
          } else {
            if (replaceImplementerProfile.trim()) c.implementerRouting = { profile: replaceImplementerProfile.trim() }
            if (replaceReviewerProfile.trim()) c.reviewerRouting = { profile: replaceReviewerProfile.trim() }
          }
        }
        if (kind === 'recover') { c.confirmPreviousWorkerStopped = attest; c.reason = `${workerName.trim()}: ${reason.trim()}` }
        pending = pending && JSON.stringify({ ...pending, requestId: '', replacement: pending.replacement && { ...pending.replacement, requestId: '' } }) === JSON.stringify({ ...c, requestId: '', replacement: c.replacement && { ...c.replacement, requestId: '' } }) ? pending : c
      }
      item = await commandFactoryItem(itemId, pending)
      pending = null; notice = kind === 'stop' ? 'Stop requested. Waiting for worker confirmation.' : `${kind} accepted`
      if (kind === 'answer') answer = ''
    } catch (e) {
      error = errMessage(e)
      if (e instanceof APIError && e.status === 409) {
        pending = null
        try { await refresh(); notice = 'Item changed. Review the refreshed version before trying again.' } catch (refreshError) { error += `; refresh failed: ${errMessage(refreshError)}` }
      }
    } finally { busy = false }
  }
  const round = $derived(item?.rounds?.at(-1)), ci = $derived(round?.ci)
  const failed = $derived((ci?.checks ?? []).filter((c) => c.conclusion && !['success', 'neutral', 'skipped'].includes(c.conclusion)))
  const passed = $derived((ci?.checks ?? []).filter((c) => c.conclusion === 'success'))
  const other = $derived((ci?.checks ?? []).filter((c) => !failed.includes(c) && !passed.includes(c)))
  const granted = $derived((item?.grants ?? []).reduce((sum, g) => sum + g.additionalRounds, 0))
  const lastWorkerId = $derived(item?.workers?.at(-1)?.agentId ?? '')
  const currentConfig = $derived(item?.approvals?.at(-1)?.config ?? item?.draft.config ?? null)
  const currentImplementerProfile = $derived((currentConfig?.implementerRouting as { profile?: string } | undefined)?.profile || 'none')
  const currentReviewerProfile = $derived((currentConfig?.reviewerRouting as { profile?: string } | undefined)?.profile || 'none')
  const narration = $derived(round?.implementation?.narration ?? null)
  const narrationSections = $derived.by(() => {
    const n = narration
    if (!n) return [] as { heading: string; paragraphs: string[] }[]
    const sections: { heading: string; paragraphs: string[] }[] = []
    const add = (heading: string, ...paragraphs: string[]) => {
      const kept = paragraphs.filter((paragraph) => paragraph)
      if (kept.length) sections.push({ heading, paragraphs: kept })
    }
    add('Issue', n.issue)
    add('Approach', n.approach)
    add('Existing behaviour', n.existingBehaviour)
    add('Alternatives and trade-offs', n.alternatives, n.tradeoffs)
    add('Implementation choice', n.choice)
    add('Verification', n.verification)
    add('Limitations', n.limitations)
    return sections
  })
  const excerpts = $derived(narration?.excerpts ?? [])
</script>
<div class="mx-auto flex max-w-6xl flex-col gap-4 p-4 sm:p-6">
  <a class="text-sm text-accent underline" href="#factory" onclick={(e) => { e.preventDefault(); onNavigate('#factory') }}>Factory</a>
  {#if error}<Card role="alert" class="border-attention text-sm">{error}</Card>{/if}
  {#if notice}<Card role="status" class="text-sm">{notice}</Card>{/if}
  {#if item}
    <header class="grid gap-2"><h1 class="break-words text-xl font-semibold">{item.title}</h1><p class="text-sm text-muted">{item.repoId} · {item.branch || item.draft.config.targetBranch}</p><div class="flex gap-2"><Tag tone={item.decision ? 'warn' : 'neutral'}>{item.state}</Tag><span class="text-sm">{item.stage}</span></div></header>
    {#if item.state === 'draft'}
      <Card class="grid gap-3" data-testid="draft-approval">
        <h2 class="font-semibold">Review draft before starting</h2>
        <p class="text-sm">Repository: {item.draft.config.repoId} · Branch: {item.draft.config.targetBranch} · {item.draft.config.maxRounds} rounds</p>
        <p class="text-sm">Implementer: {JSON.stringify(item.draft.config.implementerRouting)} · Reviewer: {JSON.stringify(item.draft.config.reviewerRouting)}</p>
        <p class="text-sm">CI: {item.draft.config.expectedChecks?.length ? item.draft.config.expectedChecks.map((c) => c.name).join(', ') : 'No required CI acknowledged'}</p>
        <p class="text-sm">Environment: {item.draft.config.native ? 'Native' : item.draft.config.workspace || 'Unspecified workspace'} · Implementer auto permission: {item.draft.config.implementerAuto ? 'on' : 'off'}</p>
        <p class="whitespace-pre-wrap break-words text-sm">Reviewer instructions: {item.draft.config.reviewerInstructions || 'None'}</p>
        <pre class="max-h-72 overflow-auto whitespace-pre-wrap break-words rounded border border-border p-3 text-sm">{item.draft.plan}</pre>
        {#if item.draft.spec}<details><summary>Specification</summary><pre class="max-h-72 overflow-auto whitespace-pre-wrap break-words text-sm">{item.draft.spec}</pre></details>{/if}
        <Button onclick={() => void command('approve')} disabled={busy}>Approve &amp; start</Button>
      </Card>
    {/if}
    {#if item.decision}
      <Card class="grid gap-3 border-attention" data-testid="factory-decision">
        <h2 class="font-semibold">Action needed: {item.decision.kind}</h2>
        {#if item.decision.question}<p class="whitespace-pre-wrap break-words">{item.decision.question}</p>{/if}
        {#if item.decision.reason}<p class="whitespace-pre-wrap break-words text-sm text-muted">{item.decision.reason}</p>{/if}
        {#if item.decision.kind === 'question'}
          <label class="grid gap-1 text-sm">Answer <textarea class="min-h-24 rounded border border-border bg-bg p-2" bind:value={answer}></textarea></label>
          <Button disabled={busy || !answer.trim()} onclick={() => void command('answer')}>Send answer</Button>
        {/if}
        {#if ['exhausted', 'no_progress', 'worker_failed', 'invalid_result', 'ci_unavailable', 'ci_wait', 'conflict'].includes(item.decision.kind)}
          <p class="text-sm">{(item.rounds ?? []).length} rounds used · {granted} granted total. An additional allowance is required to resume work.</p>
          <label class="grid gap-1 text-sm">Reason <textarea class="rounded border border-border bg-bg p-2" bind:value={reason}></textarea></label>
          <label class="grid gap-1 text-sm">Additional rounds <input class="rounded border border-border bg-bg p-2" type="number" min="1" bind:value={additionalRounds} /></label>
          <p class="text-sm">Routes in force: implementer {currentImplementerProfile} · reviewer {currentReviewerProfile}</p>
          <label class="grid gap-1 text-sm">New implementer profile (optional) <input class="rounded border border-border bg-bg p-2" bind:value={replaceImplementerProfile} /></label>
          <label class="grid gap-1 text-sm">New reviewer profile (optional) <input class="rounded border border-border bg-bg p-2" bind:value={replaceReviewerProfile} /></label>
          <p class="text-sm">A route-only retry starts a fresh round. The interrupted round's review is not reused, and one granted round is spent.</p>
          <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={changePlan} onchange={() => { if (changePlan && item) { const approval = item.approvals?.at(-1); replacementPlan = approval?.plan ?? item.draft.plan; replacementSpec = approval?.spec ?? item.draft.spec; replacementInstructions = approval?.config.reviewerInstructions ?? item.draft.config.reviewerInstructions } }} /> Change approved plan or reviewer instructions</label>
          {#if changePlan}<details open><summary>Replacement plan and reviewer instructions</summary><div class="grid gap-2 pt-2">
            <p class="text-sm">Review these changes before submitting. Retry records a new approval revision with this plan, the selected routes and reviewer instructions before work resumes.</p>
            <label class="grid gap-1 text-sm">Replacement plan <textarea class="min-h-40 rounded border border-border bg-bg p-2" bind:value={replacementPlan}></textarea></label>
            <label class="grid gap-1 text-sm">Replacement specification <textarea class="min-h-24 rounded border border-border bg-bg p-2" bind:value={replacementSpec}></textarea></label>
            <label class="grid gap-1 text-sm">Reviewer instructions <textarea class="min-h-24 rounded border border-border bg-bg p-2" bind:value={replacementInstructions}></textarea></label>
          </div></details>{/if}
          <Button disabled={busy || !reason.trim() || additionalRounds < 1 || (changePlan && !replacementPlan.trim())} onclick={() => void command('retry')}>Grant rounds and retry</Button>
        {/if}
        {#if item.decision.kind === 'recovery'}
          <p class="text-sm">Refresh evidence first. Attest only after independently confirming the previous named worker has stopped. Attestation cannot prove an unreachable worker stopped.</p>
          <Button disabled={busy} onclick={() => void command('reconcile')}>Refresh evidence</Button>
          <p class="break-all font-mono text-sm">Last worker agent ID: {lastWorkerId || "Unavailable"}</p>
          <label class="grid gap-1 text-sm">Type the exact last worker agent ID <input class="rounded border border-border bg-bg p-2" bind:value={workerName} /></label>
          <label class="grid gap-1 text-sm">How you confirmed it <textarea class="rounded border border-border bg-bg p-2" bind:value={reason}></textarea></label>
          <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={attest} /> I confirm this named previous worker has stopped</label>
          <Button disabled={busy || !attest || !lastWorkerId || workerName.trim() !== lastWorkerId || !reason.trim()} onclick={() => void command('recover')}>Record named-worker attestation</Button>
        {/if}
      </Card>
    {/if}
    {#if !['closed', 'stopped', 'stopping', 'draft'].includes(item.state)}<Button disabled={busy} onclick={() => void command('stop')}>Request stop</Button>{/if}
    {#if item.state === 'stopping'}<Card role="status">Stop requested. Worker shutdown is not yet confirmed.</Card>{/if}
    <div class="grid min-w-0 gap-4 lg:grid-cols-2"><div class="grid min-w-0 content-start gap-4">
      <Card class="grid min-w-0 gap-3"><h2 class="font-semibold">CI evidence</h2>
        {#if !ci}<p class="text-sm text-muted">No CI evidence observed yet.</p>{:else if !ci.complete}<p class="text-sm text-attention">CI evidence unavailable or incomplete. Readiness is not established.</p>{:else if !(ci.checks ?? []).length}<p class="text-sm">Observation complete; no checks reported.</p>{/if}
        {#each failed as check}<div class="min-w-0 rounded border border-attention p-2"><strong>{check.key.name}: {check.conclusion}</strong>{#if check.url}<a class="ml-2 text-accent underline" href={check.url} target="_blank" rel="noopener noreferrer">Open check</a>{/if}{#if check.logTail}<pre class="max-h-60 max-w-full overflow-auto whitespace-pre-wrap break-words text-xs">{check.logTail}</pre>{/if}</div>{/each}
        {#if passed.length}<details><summary>{passed.length} passing checks</summary><ul class="text-sm">{#each passed as check}<li>{check.key.name}</li>{/each}</ul></details>{/if}
        {#each other as check}<p class="text-sm">{check.key.name}: {check.conclusion || check.status || 'pending'}</p>{/each}
      </Card>
      <Card class="grid min-w-0 gap-2"><h2 class="font-semibold">Engineering report</h2>
        {#if round?.implementation}{#if narration}
          <div class="grid gap-2" data-testid="factory-narration">
            {#each narrationSections as section}<div class="grid gap-1"><h3 class="text-sm font-semibold">{section.heading}</h3>{#each section.paragraphs as paragraph}<p class="whitespace-pre-wrap break-words text-sm">{paragraph}</p>{/each}</div>{/each}
            {#if excerpts.length}<div class="grid gap-1"><h3 class="text-sm font-semibold">Selected code</h3>{#each excerpts as excerpt}<div class="min-w-0 rounded border border-border p-2"><div class="flex flex-wrap items-baseline gap-2"><p class="break-all font-mono text-xs">{excerpt.path}</p>{#if excerpt.lang}<span class="text-xs text-muted">{excerpt.lang}</span>{/if}</div><pre class="max-h-60 overflow-auto whitespace-pre-wrap break-words text-xs">{excerpt.code}</pre></div>{/each}</div>{/if}
          </div>
        {:else}<p class="whitespace-pre-wrap break-words text-sm">{round.implementation.summary}</p>{/if}{#if round.implementation.question}<p class="text-sm">Implementer question: {round.implementation.question}</p>{/if}{#if round.implementation.reason}<p class="text-sm">Implementer reason: {round.implementation.reason}</p>{/if}{/if}
        {#if round?.review}<p class="whitespace-pre-wrap break-words text-sm">Review: {round.review.summary}</p>{#if round.review.question}<p class="text-sm">Reviewer question: {round.review.question}</p>{/if}{#if round.review.reason}<p class="text-sm">Reviewer reason: {round.review.reason}</p>{/if}
          {#if round.review.findings?.length}<h3 class="text-sm font-semibold">Review findings</h3>{#each round.review.findings as finding}<div class="min-w-0 rounded border border-border p-2 text-sm"><p class="font-semibold">{finding.severity}: {finding.title}</p>{#if finding.path}<p class="break-all font-mono text-xs">{finding.path}{#if finding.line}:{finding.line}{/if}</p>{/if}{#if finding.body}<p class="whitespace-pre-wrap break-words">{finding.body}</p>{/if}</div>{/each}{/if}
        {/if}
        {#if !round?.implementation && !round?.review}<p class="text-sm text-muted">No report yet.</p>{/if}</Card>
      {#if item.prs?.length}<Card><h2 class="font-semibold">Pull requests</h2>{#each item.prs as pr}<a class="block text-sm text-accent underline" href={pr.url} target="_blank" rel="noopener noreferrer">PR #{pr.number} ({pr.draft ? 'draft' : pr.state})</a>{/each}</Card>{/if}
      {#if item.workers?.length}<Card><h2 class="font-semibold">Agents</h2>{#each item.workers as worker}<a class="block text-sm text-accent underline" href={`#chat/${encodeURIComponent(worker.agentId)}`} onclick={(e) => { e.preventDefault(); onNavigate(`#chat/${encodeURIComponent(worker.agentId)}`) }}>{worker.role}: {worker.agentId}</a>{/each}</Card>{/if}
    </div><Card class="min-w-0"><h2 class="mb-2 font-semibold">Activity</h2>{#if !item.history?.length}<p class="text-sm text-muted">No activity yet.</p>{:else}<ol class="grid gap-3 text-sm">{#each [...item.history].reverse() as event}<li class="min-w-0 border-b border-border pb-2"><time class="text-xs text-muted">{event.at}</time><p class="whitespace-pre-wrap break-words">{event.message}</p></li>{/each}</ol>{/if}</Card></div>
  {:else}<p role="status">Loading work item…</p>{/if}
</div>
