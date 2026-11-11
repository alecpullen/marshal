<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import { APIError, createFactoryItem, errMessage, getModels, getProjectSettings, listRepos, listWorkspaces, type RepoRow, type WorkspaceListItem } from '../lib/api'
  import type { FactoryCheckKey, FactoryConfig, FactoryCreate, FactoryItem } from '../lib/factory'
  import { formatFactoryRoute } from '../lib/routes'
  let { projectRoot = '', onNavigate }: { projectRoot?: string; onNavigate: (hash: string) => void } = $props()
  let title = $state(''), plan = $state(''), spec = $state(''), repoId = $state(''), targetBranch = $state('main')
  let workspace = $state(''), native = $state(true), implementerProfile = $state(''), reviewerProfile = $state('')
  let reviewerInstructions = $state(''), implementerAuto = $state(false), maxRounds = $state(5)
  let expectedChecksText = $state(''), noRequiredCiAcknowledged = $state(false)
  let repos = $state<RepoRow[]>([]), workspaces = $state<WorkspaceListItem[]>([]), profiles = $state<string[]>([])
  let error = $state(''), busy = $state(false), draft = $state<FactoryItem | null>(null), pending = $state<FactoryCreate | null>(null)
  onMount(() => { void (async () => {
    try {
      const [r, w, m] = await Promise.all([listRepos(), listWorkspaces(), getModels()])
      repos = r; workspaces = w; profiles = Object.keys(m.profiles).sort()
      if (projectRoot) {
        const settings = await getProjectSettings(projectRoot)
        repoId = settings.intake.repoId || ''; workspace = settings.workspace || ''; native = !workspace
        implementerProfile = settings.routing?.profile || ''; reviewerProfile = settings.routing?.profile || ''
      }
      if (!repoId && r.length === 1) repoId = r[0].id
      targetBranch = r.find((x) => x.id === repoId)?.branch || 'main'
    } catch (e) { error = errMessage(e) }
  })() })
  function checks(): FactoryCheckKey[] {
    return expectedChecksText.split('\n').map((line) => line.trim()).filter(Boolean).map((line) => {
      const [kind, name, app] = line.split(':')
      if (kind === 'check_run' && name && Number(app) > 0) return { kind, name, appId: Number(app) }
      if (kind === 'commit_status' && name && !app) return { kind, name, appId: 0 }
      throw new Error('Each check must be check_run:name:appId or commit_status:context')
    })
  }
  function payload(): FactoryCreate {
    if (!title.trim() || !plan.trim()) throw new Error('Title and plan are required')
    if (!repoId) throw new Error('Choose a registered repository')
    const expectedChecks = checks()
    if (!expectedChecks.length && !noRequiredCiAcknowledged) throw new Error('Confirm that this repository has no required CI, or enter expected checks')
    if (!implementerProfile || !reviewerProfile) throw new Error('Choose both model profiles')
    const config: FactoryConfig = { repoId, projectRoot, hostId: 'local', targetBranch, workspace: native ? '' : workspace, native,
      implementerRouting: { profile: implementerProfile }, reviewerRouting: { profile: reviewerProfile }, reviewerInstructions,
      implementerAuto, expectedChecks, noRequiredCiAcknowledged, maxRounds, ciPollSeconds: 15, ciWaitSeconds: 1800 }
    return { requestId: crypto.randomUUID(), title, plan, spec, config }
  }
  async function save() {
    busy = true; error = ''
    try { const next = payload(); pending = pending && JSON.stringify({ ...pending, requestId: '' }) === JSON.stringify({ ...next, requestId: '' }) ? pending : next; draft = await createFactoryItem(pending); pending = null }
    catch (e) { error = errMessage(e) }
    finally { busy = false }
  }
</script>
<div class="mx-auto flex max-w-4xl flex-col gap-4 p-4 sm:p-6">
  <a class="text-sm text-accent underline" href="#factory" onclick={(e) => { e.preventDefault(); onNavigate('#factory') }}>Factory</a>
  <h1 class="text-xl font-semibold">New factory work item</h1>
  {#if error}<Card role="alert" class="border-attention text-sm">{error}{#if error.includes('network') || error.includes('fetch')} <span>Draft input is retained. Retry uses the same request ID.</span>{/if}</Card>{/if}
  {#if draft}
    <Card class="flex flex-col gap-3" data-testid="draft-preview">
      <h2 class="font-semibold">Draft saved — review before starting</h2>
      <p>{draft.title} · {draft.repoId} · {draft.state}</p>
      <p class="whitespace-pre-wrap break-words text-sm">{draft.draft.plan}</p>
      <p class="text-sm">{draft.draft.config.expectedChecks.length} expected CI checks · {draft.draft.config.maxRounds} total rounds</p>
      <a class="text-accent underline" href={formatFactoryRoute({ itemId: draft.id })} onclick={(e) => { e.preventDefault(); onNavigate(formatFactoryRoute({ itemId: draft!.id })) }}>Review and approve this draft</a>
    </Card>
  {:else}
    <form class="grid gap-4" onsubmit={(e) => { e.preventDefault(); void save() }}>
      <Card class="grid gap-3">
        <label class="grid gap-1 text-sm">Title <input class="rounded border border-border bg-bg p-2" bind:value={title} required /></label>
        <label class="grid gap-1 text-sm">Plan <textarea class="min-h-40 rounded border border-border bg-bg p-2" bind:value={plan} required></textarea></label>
        <label class="grid gap-1 text-sm">Specification (optional) <textarea class="min-h-24 rounded border border-border bg-bg p-2" bind:value={spec}></textarea></label>
      </Card>
      <Card class="grid gap-3 sm:grid-cols-2">
        <label class="grid gap-1 text-sm">Repository <select class="rounded border border-border bg-bg p-2" bind:value={repoId} onchange={() => targetBranch = repos.find((r) => r.id === repoId)?.branch || 'main'} required><option value="">Choose repository</option>{#each repos as repo}<option value={repo.id}>{repo.id}</option>{/each}</select></label>
        <label class="grid gap-1 text-sm">Target branch <input class="rounded border border-border bg-bg p-2" bind:value={targetBranch} required /></label>
        <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={native} /> Native environment</label>
        {#if !native}<label class="grid gap-1 text-sm">Workspace <select class="rounded border border-border bg-bg p-2" bind:value={workspace} required><option value="">Choose workspace</option>{#each workspaces as ws}<option value={ws.name}>{ws.name}</option>{/each}</select></label>{/if}
        <label class="grid gap-1 text-sm">Implementer model profile <select class="rounded border border-border bg-bg p-2" bind:value={implementerProfile} required><option value="">Choose profile</option>{#each profiles as p}<option value={p}>{p}</option>{/each}</select></label>
        <label class="grid gap-1 text-sm">Reviewer model profile <select class="rounded border border-border bg-bg p-2" bind:value={reviewerProfile} required><option value="">Choose profile</option>{#each profiles as p}<option value={p}>{p}</option>{/each}</select></label>
        <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={implementerAuto} /> Implementer auto permission setting</label>
        <label class="grid gap-1 text-sm">Total rounds <input class="rounded border border-border bg-bg p-2" type="number" min="1" bind:value={maxRounds} /></label>
      </Card>
      <Card class="grid gap-3">
        <label class="grid gap-1 text-sm">Reviewer instructions <textarea class="min-h-24 rounded border border-border bg-bg p-2" bind:value={reviewerInstructions}></textarea></label>
        <label class="grid gap-1 text-sm">Expected CI checks (one per line: check_run:name:appId or commit_status:context) <textarea class="min-h-24 rounded border border-border bg-bg p-2 font-mono" bind:value={expectedChecksText}></textarea></label>
        <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={noRequiredCiAcknowledged} /> This repository has no required CI</label>
      </Card>
      <Button type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save draft'}</Button>
    </form>
  {/if}
</div>
