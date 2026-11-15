<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import DraftApproval from '../lib/factory/DraftApproval.svelte'
  import WorkItemHeader from '../lib/factory/WorkItemHeader.svelte'
  import ActivityTimeline from '../lib/factory/ActivityTimeline.svelte'
  import DecisionPanel from '../lib/factory/DecisionPanel.svelte'
  import RecoveryPanel from '../lib/factory/RecoveryPanel.svelte'
  import DocumentsView from '../lib/factory/DocumentsView.svelte'
  import PullRequestsView from '../lib/factory/PullRequestsView.svelte'
  import { APIError, commandFactoryItem, errMessage, getPreviewEnvironments } from '../lib/api'
  import { formatFactoryRoute, parseFactoryRoute } from '../lib/routes'
  import { defaultFactoryItemView } from '../lib/studio/model'
  import type { FactoryCheck, FactoryCommand, FactoryCreate, FactoryItem, FactoryItemView, FactoryRoute } from '../lib/factory'

  import type { FactoryState } from '../lib/factoryStore'
  import type { PreviewDeployment, PreviewEnvironment, PreviewReport } from '../lib/preview'

  /** C5: what a decision or recovery panel hands back with a command kind. */
  interface FactoryCommandInput {
    answer?: string
    reason?: string
    additionalRounds?: number
    replacement?: FactoryCreate
    implementerRouting?: Record<string, unknown>
    reviewerRouting?: Record<string, unknown>
    confirmPreviousWorkerStopped?: boolean
  }
  /** The decision kinds the recovery panel presents; everything else is a question. */
  const RECOVERY_KINDS = ['exhausted', 'no_progress', 'worker_failed', 'invalid_result', 'ci_unavailable', 'ci_wait', 'conflict', 'recovery']
  /*
    One item, read through the shell's store instead of a poller of its own:
    the store answers the first read, the shell's list poll keeps it current,
    and a command's own result is published back so every view agrees. The
    callbacks are the shell's, so the poller and the error handling stay in
    one place.
  */
  let {
    itemId,
    route,
    factory,
    getItem,
    upsert,
    onRefresh,
    onNavigate,
  }: {
    itemId: string
    route: FactoryRoute
    factory: FactoryState
    getItem: (id: string) => Promise<FactoryItem>
    upsert: (item: FactoryItem) => void
    onRefresh: () => Promise<void>
    onNavigate: (hash: string) => void
  } = $props()
  let error = $state(''), notice = $state(''), busy = $state(false)
  /** The first read, until the shell's store carries this item. */
  let fetched = $state<FactoryItem | null>(null)
  /*
    Once the store carries the item it is the source of truth: the shell's list
    poll, another view's command and this view's own upsert all land there.
    Deriving rather than copying keeps one value, so a store update redraws the
    item in place and nothing here re-reads the preview.
  */
  const stored = $derived(factory.items.find((candidate) => candidate.id === itemId) ?? null)
  const item = $derived(stored ?? fetched)
  /*
    The selected view is the reader's, not the item's. It is seeded once from
    the state the item was in when this page opened — or from an explicit
    `view` the route named — and after that only an explicit route change
    moves it: a poll that reports a new state must never take the tab away
    from the person reading it.
  */
  let view = $state<FactoryItemView | null>(null)
  /** The route request last applied, so a repeated one cannot move the tab. */
  let appliedView: FactoryItemView | undefined
  $effect(() => {
    const requested = route.view
    if (!item) return
    if (view === null) {
      view = defaultFactoryItemView(item, requested)
      appliedView = requested
      return
    }
    if (requested === appliedView) return
    appliedView = requested
    view = defaultFactoryItemView(item, requested)
  })
  /*
    Polling never moves the tab, so an approval that was open when the draft was
    approved stays selected and says so rather than silently becoming another
    view. The tab strip keeps it visible for exactly that reason.
  */
  const selectedView = $derived<FactoryItemView>(view ?? 'activity')
  /*
    The decisions' answers, reasons, allowances, replacement documents and
    attestations belong to the panels that collect them, so a refresh of the
    same decision cannot take them away. Only the in-flight command's identity
    is kept here, because request identity is the one thing every panel's
    submission shares.
  */
  let pending = $state<FactoryCommand | null>(null)
  let preview = $state<PreviewReport | null>(null), previewLoading = $state(false), previewError = $state('')
  let previewStarted = $state(false), previewRequest = 0, alive = true
  type PreviewSegment = { kind: 'code'; text: string } | { kind: 'text'; text: string } | { kind: 'link'; url: string; logs: string } | { kind: 'badge'; label: string; sha: string }
  const textEncoder = new TextEncoder(), unsafeUrlPattern = /[\s`<>\u0000-\u001f\u007f-\u009f]/
  /** Spec D10: clickable only as a literal absolute http(s) URL with a nonempty host, no whitespace/control (C0+C1)/angle/backtick characters, at most 2048 bytes. Matches the bridge validator factoryURLClickable (web/bridge/factory_prbody.go). No repair. */
  function isHttpUrl(value: string) {
    if (!value || !/^https?:\/\//i.test(value) || value.length > 2048 || textEncoder.encode(value).length > 2048 || unsafeUrlPattern.test(value)) return false
    try {
      const parsed = new URL(value)
      return (parsed.protocol === 'http:' || parsed.protocol === 'https:') && !!parsed.hostname
    } catch { return false }
  }
  const displayText = (value: string) => { const chars = [...value]; return chars.length > 512 ? chars.slice(0, 512).join('') + '…' : value }
  function previewBadge(sha: string, head: string) { return sha.toLowerCase() === head.toLowerCase() ? 'matches the last factory-recorded PR head' : 'differs from the last factory-recorded PR head' }
  function deploymentSegments(d: PreviewDeployment, head: string): PreviewSegment[] {
    const segments: PreviewSegment[] = [{ kind: 'code', text: d.environment }, d.state ? { kind: 'code', text: d.state } : { kind: 'text', text: '(no status reported by the provider)' }]
    if (d.sha) {
      segments.push({ kind: 'code', text: d.sha.slice(0, 7) })
      if (head) segments.push({ kind: 'badge', label: previewBadge(d.sha, head), sha: head.slice(0, 7) })
    } else segments.push({ kind: 'text', text: 'revision not reported by the provider' })
    if (d.environmentUrl) segments.push({ kind: 'link', url: d.environmentUrl, logs: d.logUrl })
    if (d.description) segments.push({ kind: 'code', text: d.description })
    return segments
  }
  function environmentSegments(e: PreviewEnvironment): PreviewSegment[] {
    return e.htmlUrl ? [{ kind: 'code', text: e.name }, { kind: 'link', url: e.htmlUrl, logs: '' }] : [{ kind: 'code', text: e.name }]
  }
  function checkState(c: FactoryCheck) { return c.conclusion || c.status || 'pending' }
  async function loadPreview() {
    if (!item?.repoId) return
    const seq = ++previewRequest
    previewLoading = true; previewError = ''
    try {
      const report = (await getPreviewEnvironments(item.repoId, item.branch)) ?? null
      if (!alive || seq !== previewRequest) return
      preview = report
    } catch (e) {
      if (!alive || seq !== previewRequest) return
      previewError = errMessage(e)
    }
    if (alive && seq === previewRequest) previewLoading = false
  }
  /*
    The first read goes through the shell store's getItem, so an item the last
    list snapshot already carries costs no request.
  */
  onMount(() => {
    void (async () => {
      try {
        const next = await getItem(itemId)
        if (!alive) return
        fetched = next
        error = ''
      } catch (e) {
        if (alive) error = errMessage(e)
      }
    })()
    return () => { alive = false }
  })
  // Spec §6: the initial preview load fires once from the mount path, never from a store update.
  $effect(() => {
    if (!item?.repoId || previewStarted) return
    previewStarted = true
    void loadPreview()
  })
  /*
    One command path for every control (design C5). The panels own what a
    person typed; the identity a request must carry — its request ID, the
    version it was reviewed against, and the decision it answers — is built
    here from the latest item, so a panel can never submit a stale decision
    binding of its own.
  */
  async function command(kind: FactoryCommand['kind'], input: FactoryCommandInput = {}) {
    if (!item || busy) return
    busy = true; error = ''; notice = ''
    try {
      {
        const c: FactoryCommand = { requestId: crypto.randomUUID(), kind, expectedVersion: item.version, decisionId: item.decision?.id, ...input }
        pending = pending && JSON.stringify({ ...pending, requestId: '', replacement: pending.replacement && { ...pending.replacement, requestId: '' } }) === JSON.stringify({ ...c, requestId: '', replacement: c.replacement && { ...c.replacement, requestId: '' } }) ? pending : c
      }
      const applied = await commandFactoryItem(itemId, pending)
      // The command's own result is the newest truth: publish it before the
      // list can render again, then show it here.
      upsert(applied)
      fetched = applied
      pending = null; notice = kind === 'stop' ? 'Stop requested. Waiting for worker confirmation.' : `${kind} accepted`
    } catch (e) {
      error = errMessage(e)
      if (e instanceof APIError && e.status === 409) {
        // Only the obsolete command is dropped: the panel that submitted it
        // stays mounted, so the person keeps their input and retries it
        // against the version the refresh just showed them.
        pending = null
        // Awaited, and its own failure reported: a stale command must say so
        // rather than leave the person on a version that cannot succeed.
        try { await onRefresh(); notice = 'Item changed. Review the refreshed version before trying again.' } catch (refreshError) { error += `; refresh failed: ${errMessage(refreshError)}` }
      }
    } finally { busy = false }
  }
  /*
    A view tab moves within this item, so the worker drill-in the route names
    stays selected: the tab hash keeps only the view change, and `agent` is
    carried over from the route rather than dropped by the tab.
  */
  function navigateView(hash: string) {
    const next = parseFactoryRoute(hash)
    if (!next?.itemId) { onNavigate(hash); return }
    onNavigate(formatFactoryRoute({ itemId: next.itemId, view: next.view, agent: route.agent }))
  }
  /** The activity view, which is also where a decision is answered. */
  const activityRoute = $derived(formatFactoryRoute({ itemId, view: 'activity' }))
  const round = $derived(item?.rounds?.at(-1)), ci = $derived(round?.ci)
  const failed = $derived((ci?.checks ?? []).filter((c) => c.conclusion && !['success', 'neutral', 'skipped'].includes(c.conclusion)))
  const passed = $derived((ci?.checks ?? []).filter((c) => c.conclusion === 'success'))
  const other = $derived((ci?.checks ?? []).filter((c) => !failed.includes(c) && !passed.includes(c)))
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
  const prHead = $derived(item?.prs?.at(-1)?.headSha ?? '')
  const previewDeployments = $derived((preview?.deployments ?? []).map((d) => deploymentSegments(d, prHead)))
  const previewEnvironments = $derived((preview?.environments ?? []).map(environmentSegments))
  const previewGaps = $derived(preview?.gaps ?? [])
  const statusLinks = $derived.by(() => {
    const last = item?.rounds?.at(-1), ci = last?.ci
    if (!last || !ci || ci.headSha !== last.headSha) return [] as FactoryCheck[]
    return [...(ci.checks ?? [])].filter((c) => c.key.kind === 'commit_status' && c.url)
      .sort((a, b) => (a.key.name < b.key.name ? -1 : a.key.name > b.key.name ? 1 : a.id < b.id ? -1 : a.id > b.id ? 1 : 0))
  })
  const previewEmpty = $derived(!previewDeployments.length && !previewEnvironments.length && !previewGaps.length)
</script>
<div class="mx-auto flex max-w-6xl flex-col gap-4 p-4 sm:p-6">
  {#if error}<Card role="alert" class="border-attention text-sm">{error}</Card>{/if}
  {#if notice}<Card role="status" class="text-sm">{notice}</Card>{/if}
  {#if item}
    <WorkItemHeader item={item} view={selectedView} onNavigate={navigateView} onStop={() => void command('stop')} stopping={busy} />
    {#if item.decision && selectedView !== 'activity'}
      <Card class="grid gap-2 border-attention" data-testid="factory-decision-banner">
        <h2 class="font-semibold">Action needed: {item.decision.kind}</h2>
        {#if item.decision.question}<p class="whitespace-pre-wrap break-words text-sm">{item.decision.question}</p>{/if}
        <a class="text-sm text-accent underline" href={activityRoute} onclick={(e) => { e.preventDefault(); onNavigate(activityRoute) }}>Answer it in activity</a>
      </Card>
    {/if}
    {#if selectedView === 'approval'}
      {#if item.state === 'draft'}
        <div data-testid="draft-approval">
          <DraftApproval item={item} busy={busy} onApprove={() => void command('approve')} />
        </div>
      {:else}
        <Card class="text-sm">This item is no longer a draft, so there is nothing to approve. <a class="text-accent underline" href={activityRoute} onclick={(e) => { e.preventDefault(); onNavigate(activityRoute) }}>Go to activity</a></Card>
      {/if}
    {:else if selectedView === 'review'}
      <div class="grid min-w-0 content-start gap-4">
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
        {#if item.repoId}<Card class="grid min-w-0 gap-2" data-testid="factory-preview">
          <div class="flex flex-wrap items-center justify-between gap-2"><h2 class="font-semibold">Preview and test environments</h2><Button disabled={previewLoading} onclick={() => void loadPreview()}>Refresh</Button></div>
          {#if previewLoading}<p class="text-sm text-muted">Reading preview environments…</p>
          {:else if previewError}<p class="text-sm">Preview and test environments could not be read.</p><p class="break-words text-sm text-muted">{previewError}</p>
          {:else if preview?.status === 'unavailable'}<p class="text-sm">Preview and test environments could not be read.</p>{#if preview.error}<p class="break-words text-sm text-muted">{preview.error}</p>{/if}
          {:else if preview}
            {#each previewGaps as gap}<p class="break-words text-sm"><code class="text-xs">{gap.key}</code> — {gap.state} — {gap.detail}</p>{/each}
            {#if previewEmpty}<p class="text-sm text-muted">No preview deployments or test environments are configured for this revision.</p>{/if}
            {#if previewDeployments.length}<ul class="grid gap-1">{#each previewDeployments as segments}<li class="break-words text-sm">{#each segments as segment, i}{#if i}<span class="text-muted"> — </span>{/if}{#if segment.kind === 'code'}<code class="text-xs">{segment.text}</code>{:else if segment.kind === 'badge'}{segment.label} <code class="text-xs">{segment.sha}</code>{:else if segment.kind === 'link'}{#if isHttpUrl(segment.url)}<a class="break-all text-accent underline" href={segment.url} target="_blank" rel="noopener noreferrer">{segment.url}</a>{:else}<span class="break-all">{displayText(segment.url)}</span>{/if}{#if segment.logs} (logs: {#if isHttpUrl(segment.logs)}<a class="break-all text-accent underline" href={segment.logs} target="_blank" rel="noopener noreferrer">{segment.logs}</a>{:else}<span class="break-all">{displayText(segment.logs)}</span>{/if}){/if}{:else if segment.kind === 'text'}{segment.text}{/if}{/each}</li>{/each}</ul>{/if}
            {#if previewEnvironments.length}<p class="text-sm">Configured environments:</p><ul class="grid gap-1">{#each previewEnvironments as segments}<li class="break-words text-sm">{#each segments as segment, i}{#if i}<span class="text-muted"> — </span>{/if}{#if segment.kind === 'code'}<code class="text-xs">{segment.text}</code>{:else if segment.kind === 'link'}{#if isHttpUrl(segment.url)}<a class="break-all text-accent underline" href={segment.url} target="_blank" rel="noopener noreferrer">{segment.url}</a>{:else}<span class="break-all">{displayText(segment.url)}</span>{/if}{:else if segment.kind === 'text'}{segment.text}{/if}{/each}</li>{/each}</ul>{/if}
            {#if statusLinks.length}<p class="text-sm">Links reported by commit statuses:</p><ul class="grid gap-1">{#each statusLinks as check}<li class="break-words text-sm"><code class="text-xs">{check.key.name}</code> (<code class="text-xs">{checkState(check)}</code>): {#if isHttpUrl(check.url)}<a class="break-all text-accent underline" href={check.url} target="_blank" rel="noopener noreferrer">{check.url}</a>{:else}<span class="break-all">{displayText(check.url)}</span>{/if}</li>{/each}</ul>{/if}
          {/if}
        </Card>{/if}
      </div>
    {:else if selectedView === 'prs'}
      <PullRequestsView {item} />
    {:else if selectedView === 'documents'}
      <DocumentsView {item} />
    {:else}
      <div class="grid min-w-0 gap-4">
        {#if item.decision}
          <!--
            The panel is chosen by the decision kind, never keyed by the item's
            version: the same decision refreshed in place keeps the panel — and
            what the person typed into it — mounted. A different decision kind
            is a different decision, so it legitimately starts fresh.
          -->
          {#if RECOVERY_KINDS.includes(item.decision.kind)}
            <RecoveryPanel {item} {busy} run={command} />
          {:else}
            <DecisionPanel {item} {busy} run={command} />
          {/if}
        {/if}
        {#if item.workers?.length}<Card><h2 class="font-semibold">Agents</h2>{#each item.workers as worker}<a class="block text-sm text-accent underline" href={`#chat/${encodeURIComponent(worker.agentId)}`} onclick={(e) => { e.preventDefault(); onNavigate(`#chat/${encodeURIComponent(worker.agentId)}`) }}>{worker.role}: {worker.agentId}</a>{/each}</Card>{/if}
        <ActivityTimeline {item} />
      </div>
    {/if}
  {:else}<p role="status">Loading work item…</p>{/if}
</div>
