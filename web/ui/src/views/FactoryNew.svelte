<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import { createFactoryItem, errMessage, getModels, getProjectSettings, listRepos, listWorkspaces, type ProjectSettings, type RepoRow, type WorkspaceListItem } from '../lib/api'
  import type { FactoryCheckKey, FactoryCreate, FactoryCreateConfig, FactoryItem } from '../lib/factory'
  import { formatFactoryRoute, formatProjectRoute } from '../lib/routes'

  let { projectRoot = '', onNavigate }: { projectRoot?: string; onNavigate: (hash: string) => void } = $props()

  // The handoff itself.
  let title = $state(''), plan = $state(''), spec = $state(''), repoId = $state('')
  // Local override selections. '' follows whatever the project resolves; the
  // effective value is never written back into these, so following stays
  // following when the project's settings change.
  let targetBranchOverride = $state('')
  let workspaceOverride = $state('')
  let implementerOverride = $state(''), reviewerOverride = $state('')
  let reviewerInstructions = $state('')
  let maxRoundsOverride = $state(''), ciPollOverride = $state(''), ciWaitOverride = $state('')
  // nil inherits the project's checks; [] is the deliberate "no checks" choice.
  let checksMode = $state<'inherit' | 'none' | 'custom'>('inherit')
  let expectedChecksText = $state('')
  // Handoff-only choices. Native is never inferred for the user once they set it.
  let native = $state(true), nativeChosen = $state(false)
  let implementerAuto = $state(false), noRequiredCiAcknowledged = $state(false)

  let repos = $state<RepoRow[]>([]), workspaces = $state<WorkspaceListItem[]>([]), profiles = $state<string[]>([])
  let settings = $state<ProjectSettings | null>(null)
  let error = $state(''), settingsError = $state(''), busy = $state(false)
  let draft = $state<FactoryItem | null>(null), pending = $state<FactoryCreate | null>(null)

  type EffectiveSource = 'handoff' | 'factory' | 'project' | 'repository' | 'builtin' | 'none'
  interface Effective { value: string; source: EffectiveSource }
  const SOURCE_TEXT: Record<EffectiveSource, string> = {
    handoff: 'This handoff',
    factory: 'Project factory default',
    project: 'Project agent default',
    repository: 'Repository branch',
    builtin: 'Built-in',
    none: 'Not configured',
  }
  const describe = (e: Effective, missing: string): string =>
    e.value ? `${SOURCE_TEXT[e.source]}: ${e.value}` : `${SOURCE_TEXT.none} — ${missing}`
  const pick = (handoff: string, factory: string, project: string): Effective => {
    if (handoff) return { value: handoff, source: 'handoff' }
    if (factory) return { value: factory, source: 'factory' }
    if (project) return { value: project, source: 'project' }
    return { value: '', source: 'none' }
  }
  const pickLimit = (handoff: string, factory: number | undefined, builtin: number): Effective => {
    if (handoff.trim()) return { value: handoff.trim(), source: 'handoff' }
    if (factory) return { value: String(factory), source: 'factory' }
    return { value: String(builtin), source: 'builtin' }
  }
  // A routing object only ever contributes its profile; every other member
  // belongs to the route it came from and is left untouched.
  const profileOf = (route: { profile?: unknown } | undefined): string => (typeof route?.profile === 'string' ? route.profile : '')

  const factoryDefaults = $derived(settings?.factoryDefaults ?? {})
  const projectWorkspace = $derived(settings?.workspace ?? '')
  const projectProfile = $derived(profileOf(settings?.routing))
  const factoryWorkspace = $derived(factoryDefaults.workspace ?? '')
  const factoryBranch = $derived(factoryDefaults.targetBranch ?? '')
  const repoBranch = $derived(repos.find((r) => r.id === repoId)?.branch || 'main')

  // Effective values, each with the source it came from.
  const workspace = $derived(pick(workspaceOverride, factoryWorkspace, projectWorkspace))
  const implementer = $derived(pick(implementerOverride, profileOf(factoryDefaults.implementerRouting), projectProfile))
  const reviewer = $derived(pick(reviewerOverride, profileOf(factoryDefaults.reviewerRouting), projectProfile))
  const branch = $derived<Effective>(targetBranchOverride.trim()
    ? { value: targetBranchOverride.trim(), source: 'handoff' }
    : factoryBranch ? { value: factoryBranch, source: 'factory' } : { value: repoBranch, source: 'repository' })
  const maxRounds = $derived(pickLimit(maxRoundsOverride, factoryDefaults.maxRounds, 5))
  const ciPoll = $derived(pickLimit(ciPollOverride, factoryDefaults.ciPollSeconds, 15))
  const ciWait = $derived(pickLimit(ciWaitOverride, factoryDefaults.ciWaitSeconds, 1800))
  const reviewerInstructionsEffective = $derived(pick('', factoryDefaults.reviewerInstructions ?? '', ''))

  // A native handoff suppresses the *project agent* workspace fallback; only a
  // separately configured factory workspace remains an explicit conflict.
  const workspaceConflict = $derived(native && factoryWorkspace !== '')

  function formatCheck(c: FactoryCheckKey): string {
    return c.kind === 'commit_status' ? `${c.kind}:${c.name}` : `${c.kind}:${c.name}:${c.appId}`
  }
  const formatChecks = (list: FactoryCheckKey[]): string => (list.length ? list.map(formatCheck).join(', ') : 'none')
  function parseChecks(text: string): FactoryCheckKey[] {
    return text.split('\n').map((line) => line.trim()).filter(Boolean).map((line) => {
      const [kind, name, app] = line.split(':')
      if (kind === 'check_run' && name && Number(app) > 0) return { kind, name, appId: Number(app) } as FactoryCheckKey
      if (kind === 'commit_status' && name && !app) return { kind, name, appId: 0 } as FactoryCheckKey
      throw new Error('Each check must be check_run:name:appId or commit_status:context')
    })
  }
  const checkPlan = $derived.by((): { checks: FactoryCheckKey[]; error: string } => {
    if (checksMode === 'none') return { checks: [], error: '' }
    if (checksMode === 'inherit') return { checks: factoryDefaults.expectedChecks ?? [], error: '' }
    try { return { checks: parseChecks(expectedChecksText), error: '' } }
    catch (e) { return { checks: [], error: errMessage(e) } }
  })
  const checksEffective = $derived(
    checksMode === 'inherit'
      ? (factoryDefaults.expectedChecks?.length
        ? `Project factory default: ${formatChecks(factoryDefaults.expectedChecks)}`
        : 'Not configured — no required checks are inherited')
      : checksMode === 'none'
        ? 'This handoff: no required checks'
        : (checkPlan.error ? checkPlan.error : `This handoff: ${formatChecks(checkPlan.checks)}`),
  )
  // Kept visible when a saved value is no longer in a picker, so the choice is
  // never silently discarded.
  const keepUnavailable = (options: { value: string; label: string }[], value: string) =>
    value && !options.some((o) => o.value === value) ? [...options, { value, label: `${value} (unavailable)` }] : options
  const wsOptions = $derived(workspaces.map((w) => ({ value: w.source === 'repo' ? `repo:${w.name}` : w.name, label: w.source === 'repo' ? `${w.name} (repo)` : w.name })))
  const workspaceChoices = $derived(keepUnavailable(wsOptions, workspaceOverride))
  const profileChoices = $derived(keepUnavailable(profiles.map((p) => ({ value: p, label: p })), implementerOverride || reviewerOverride))

  onMount(() => { void load() })
  async function load() {
    try {
      const [r, w, m] = await Promise.all([listRepos(), listWorkspaces(), getModels()])
      repos = r; workspaces = w; profiles = Object.keys(m.profiles).sort()
    } catch (e) { error = errMessage(e) }
    if (projectRoot) {
      try {
        const next = await getProjectSettings(projectRoot)
        settings = next
        // Follow whatever workspace the project configures; native is only
        // the default when nothing else can supply an environment.
        if (!nativeChosen) native = !(next.factoryDefaults?.workspace || next.workspace)
        if (!repoId && next.intake.repoId) repoId = next.intake.repoId
      } catch (e) { settingsError = errMessage(e) }
    }
    if (!repoId && repos.length === 1) repoId = repos[0].id
  }

  function positive(text: string, what: string): number {
    const n = Number(text)
    if (!Number.isFinite(n) || n <= 0) throw new Error(`${what} must be a positive number`)
    return Math.trunc(n)
  }

  function buildConfig(): FactoryCreateConfig {
    if (workspaceConflict) throw new Error(`The project's factory workspace "${factoryWorkspace}" cannot be combined with a native run. Choose a non-native environment, or change the project's factory settings.`)
    if (!native && !workspace.value) throw new Error('This project has no workspace. Choose one, or run natively.')
    if (!implementer.value || !reviewer.value) throw new Error('Choose both model profiles, or set a project model profile.')
    if (!branch.value) throw new Error('Choose a target branch')
    if (checkPlan.error) throw new Error(checkPlan.error)
    if (checksMode === 'custom' && !checkPlan.checks.length) throw new Error('Enter at least one expected check, or choose no required checks')
    if (!checkPlan.checks.length && !noRequiredCiAcknowledged) throw new Error('Confirm that this repository has no required CI, or enter expected checks')

    const config: FactoryCreateConfig = { repoId, projectRoot, native, implementerAuto, noRequiredCiAcknowledged }
    // Every field the backend can resolve is omitted while it follows: an
    // empty string or zero would be a different, explicit choice.
    if (!native && workspaceOverride) config.workspace = workspaceOverride
    // A target branch is required to create: inherit the project's factory
    // default when it has one, otherwise submit the repository's branch.
    if (targetBranchOverride.trim()) config.targetBranch = targetBranchOverride.trim()
    else if (!factoryBranch) config.targetBranch = repoBranch
    if (implementerOverride) config.implementerRouting = { profile: implementerOverride }
    if (reviewerOverride) config.reviewerRouting = { profile: reviewerOverride }
    if (reviewerInstructions.trim()) config.reviewerInstructions = reviewerInstructions.trim()
    if (checksMode !== 'inherit') config.expectedChecks = checkPlan.checks
    if (maxRoundsOverride.trim()) config.maxRounds = positive(maxRoundsOverride, 'Total rounds')
    if (ciPollOverride.trim()) config.ciPollSeconds = positive(ciPollOverride, 'CI poll seconds')
    if (ciWaitOverride.trim()) config.ciWaitSeconds = positive(ciWaitOverride, 'CI wait seconds')
    return config
  }

  function payload(): FactoryCreate {
    if (!title.trim() || !plan.trim()) throw new Error('Title and plan are required')
    if (!repoId) throw new Error('Choose a registered repository')
    return { requestId: crypto.randomUUID(), title: title.trim(), plan, spec, config: buildConfig() }
  }

  /** Identity of a request, so a byte-identical retry keeps the same ID. */
  const signature = (r: FactoryCreate): string => JSON.stringify({ ...r, requestId: '' })

  async function save() {
    busy = true; error = ''
    try {
      const next = payload()
      const retry = pending && signature(pending) === signature(next) ? pending : next
      pending = retry
      draft = await createFactoryItem(retry)
      pending = null
    } catch (e) { error = errMessage(e) }
    finally { busy = false }
  }

  // An in-progress handoff is only lost on purpose: the browser covers reload
  // and close; nothing here claims the draft outlives the tab.
  function hasInput(): boolean {
    return !!(title.trim() || plan.trim() || spec.trim())
  }
  $effect(() => {
    const warn = (e: BeforeUnloadEvent) => {
      if (!hasInput()) return
      e.preventDefault()
      e.returnValue = ''
    }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  })
</script>
<div class="mx-auto flex max-w-4xl flex-col gap-4 p-4 sm:p-6">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <a class="text-sm text-accent underline" href="#factory" onclick={(e) => { e.preventDefault(); onNavigate('#factory') }}>Factory</a>
    {#if projectRoot}
      <a class="text-sm text-accent underline" href={formatProjectRoute(projectRoot)} target="_blank" rel="noopener noreferrer" aria-label="Open project settings in a new tab">Project settings</a>
    {/if}
  </div>
  <h1 class="text-xl font-semibold">New factory work item</h1>
  {#if settingsError}
    <Card class="border-attention text-sm" role="alert" data-testid="settings-warning">
      Project settings could not be read ({settingsError}). Inherited values are unknown here; set anything you need explicitly. Your input is kept.
    </Card>
  {/if}
  {#if error}
    <Card role="alert" class="border-attention text-sm">{error}{#if error.includes('network') || error.includes('fetch') || error.includes('offline')} <span>Your input is kept, and an unchanged retry reuses the same request ID.</span>{/if}</Card>
  {/if}
  {#if draft}
    <Card class="flex flex-col gap-3" data-testid="draft-preview">
      <h2 class="font-semibold">Draft saved — review before starting</h2>
      <p>{draft.title} · {draft.repoId} · {draft.state}</p>
      <p class="whitespace-pre-wrap break-words text-sm">{draft.draft.plan}</p>
      <dl class="grid gap-x-3 gap-y-1 text-sm sm:grid-cols-[max-content_minmax(0,1fr)]">
        <dt class="text-muted">Target branch</dt><dd data-testid="draft-target-branch">{draft.draft.config.targetBranch || 'Not set'}</dd>
        <dt class="text-muted">Environment</dt><dd data-testid="draft-environment">{draft.draft.config.native ? 'Native' : draft.draft.config.workspace || 'No workspace'}</dd>
        <dt class="text-muted">Host</dt><dd>{draft.draft.config.hostId || 'local'}</dd>
        <dt class="text-muted">Implementer</dt><dd data-testid="draft-implementer">{profileOf(draft.draft.config.implementerRouting) || 'Not set'}</dd>
        <dt class="text-muted">Reviewer</dt><dd data-testid="draft-reviewer">{profileOf(draft.draft.config.reviewerRouting) || 'Not set'}</dd>
        <dt class="text-muted">Expected CI checks</dt>
        <dd data-testid="draft-checks">{#if draft.draft.config.expectedChecks.length}{#each draft.draft.config.expectedChecks as c (formatCheck(c))}<span class="mr-2 font-mono">{formatCheck(c)}</span>{/each}{:else}None — no required CI acknowledged{/if}</dd>
        <dt class="text-muted">Rounds</dt><dd data-testid="draft-rounds">{draft.draft.config.maxRounds}</dd>
        <dt class="text-muted">CI poll / wait</dt><dd>{draft.draft.config.ciPollSeconds}s / {draft.draft.config.ciWaitSeconds}s</dd>
        <dt class="text-muted">Reviewer instructions</dt><dd class="whitespace-pre-wrap break-words">{draft.draft.config.reviewerInstructions || 'None'}</dd>
      </dl>
      <a class="text-accent underline" href={formatFactoryRoute({ itemId: draft.id })} onclick={(e) => { e.preventDefault(); onNavigate(formatFactoryRoute({ itemId: draft!.id })) }}>Review and approve this draft</a>
    </Card>
  {:else}
    <form class="grid gap-4" onsubmit={(e) => { e.preventDefault(); void save() }}>
      <Card class="grid gap-3">
        <label class="grid gap-1 text-sm">Title <input class="rounded border border-border bg-bg p-2" bind:value={title} required /></label>
        <label class="grid gap-1 text-sm">Plan <textarea class="min-h-40 rounded border border-border bg-bg p-2" bind:value={plan} required></textarea></label>
        <label class="grid gap-1 text-sm">Specification (optional) <textarea class="min-h-24 rounded border border-border bg-bg p-2" bind:value={spec}></textarea></label>
      </Card>
      <Card class="grid gap-3">
        <h2 class="text-sm font-semibold">Environment</h2>
        <label class="grid gap-1 text-sm">Repository <select class="rounded border border-border bg-bg p-2" bind:value={repoId} required><option value="">Choose repository</option>{#each repos as repo (repo.id)}<option value={repo.id}>{repo.id}</option>{/each}</select></label>
        <label class="grid gap-1 text-sm">Target branch
          <input class="rounded border border-border bg-bg p-2" bind:value={targetBranchOverride} placeholder={branch.value} />
        </label>
        <p class="text-xs text-muted" data-testid="target-branch-effective">{describe(branch, 'no branch to target')}</p>
        <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={native} onchange={() => (nativeChosen = true)} /> Native environment</label>
        <label class="grid gap-1 text-sm">Workspace
          <select class="rounded border border-border bg-bg p-2" bind:value={workspaceOverride} disabled={native} onchange={() => { native = false; nativeChosen = true }}>
            <option value="">Follow inherited setting</option>
            {#each workspaceChoices as ws (ws.value)}<option value={ws.value}>{ws.label}</option>{/each}
          </select>
        </label>
        <p class="text-xs text-muted" data-testid="workspace-effective">{native ? 'Native environment — no workspace is used' : describe(workspace, 'choose a workspace or run natively')}</p>
        {#if workspaceConflict}
          <p class="text-xs text-attention" data-testid="workspace-conflict">The project's factory workspace "{factoryWorkspace}" and a native run cannot both apply. Choose a non-native environment above, or change the project's factory settings.</p>
        {/if}
      </Card>
      <Card class="grid gap-3">
        <h2 class="text-sm font-semibold">Model profiles</h2>
        <label class="grid gap-1 text-sm">Implementer model profile
          <select class="rounded border border-border bg-bg p-2" bind:value={implementerOverride}>
            <option value="">Follow inherited setting</option>
            {#each profileChoices as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
          </select>
        </label>
        <p class="text-xs text-muted" data-testid="implementer-effective">{describe(implementer, 'set a project model profile or choose one here')}</p>
        <label class="grid gap-1 text-sm">Reviewer model profile
          <select class="rounded border border-border bg-bg p-2" bind:value={reviewerOverride}>
            <option value="">Follow inherited setting</option>
            {#each profileChoices as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
          </select>
        </label>
        <p class="text-xs text-muted" data-testid="reviewer-effective">{describe(reviewer, 'set a project model profile or choose one here')}</p>
        <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={implementerAuto} /> Implementer auto permission setting</label>
      </Card>
      <Card class="grid gap-3">
        <label class="grid gap-1 text-sm">Reviewer instructions <textarea class="min-h-24 rounded border border-border bg-bg p-2" bind:value={reviewerInstructions} placeholder={factoryDefaults.reviewerInstructions || ''}></textarea></label>
        <p class="text-xs text-muted" data-testid="reviewer-instructions-effective">{reviewerInstructions.trim() ? 'This handoff: its own instructions' : describe(reviewerInstructionsEffective, 'no instructions')}</p>
        <label class="grid gap-1 text-sm">Expected checks source
          <select class="rounded border border-border bg-bg p-2" bind:value={checksMode}>
            <option value="inherit">Inherit from the project's factory defaults</option>
            <option value="none">No required checks</option>
            <option value="custom">List this handoff's checks</option>
          </select>
        </label>
        {#if checksMode === 'custom'}
          <label class="grid gap-1 text-sm">Expected CI checks (one per line: check_run:name:appId or commit_status:context) <textarea class="min-h-24 rounded border border-border bg-bg p-2 font-mono" bind:value={expectedChecksText}></textarea></label>
        {/if}
        <p class="text-xs text-muted" data-testid="checks-effective">{checksEffective}</p>
        <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={noRequiredCiAcknowledged} /> This repository has no required CI</label>
        <label class="grid gap-1 text-sm">Total rounds
          <input class="rounded border border-border bg-bg p-2" type="number" min="1" value={maxRoundsOverride} oninput={(e) => (maxRoundsOverride = e.currentTarget.value)} placeholder={maxRounds.value} />
        </label>
        <p class="text-xs text-muted" data-testid="max-rounds-effective">{describe(maxRounds, 'no round limit')}</p>
        <details class="min-w-0">
          <summary class="cursor-pointer text-xs text-muted">Advanced</summary>
          <div class="mt-2 grid gap-3">
            <label class="grid gap-1 text-sm">CI poll seconds
              <input class="rounded border border-border bg-bg p-2" type="number" min="1" value={ciPollOverride} oninput={(e) => (ciPollOverride = e.currentTarget.value)} placeholder={ciPoll.value} />
            </label>
            <p class="text-xs text-muted" data-testid="ci-poll-effective">{describe(ciPoll, 'no polling interval')}</p>
            <label class="grid gap-1 text-sm">CI wait seconds
              <input class="rounded border border-border bg-bg p-2" type="number" min="1" value={ciWaitOverride} oninput={(e) => (ciWaitOverride = e.currentTarget.value)} placeholder={ciWait.value} />
            </label>
            <p class="text-xs text-muted" data-testid="ci-wait-effective">{describe(ciWait, 'no wait limit')}</p>
          </div>
        </details>
      </Card>
      <Button type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save draft'}</Button>
    </form>
  {/if}
</div>
