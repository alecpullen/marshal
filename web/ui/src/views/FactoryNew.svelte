<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import { createFactoryItem, errMessage, getModels, getProjectSettings, listRepos, listWorkspaces, type ProjectSettings, type RepoRow, type WorkspaceListItem } from '../lib/api'
  import type { FactoryCheckKey, FactoryCreate, FactoryCreateConfig, FactoryItem } from '../lib/factory'
  import { formatFactoryRoute, formatProjectRoute, formatWorkingDraftsRoute } from '../lib/routes'
  import { emptyWorkingDraftContent, sameWorkingDraftContent, type WorkingDraftContent, type WorkingDraftController } from '../lib/working-drafts'

  /**
   * A saved working draft this view edits. With one, every change is handed to
   * the controller (which keeps it on this device and autosaves it) and the
   * manual "Save draft" path below is not offered. Without one, this is the
   * handoff form it always was.
   */
  interface WorkingDraftBinding { controller?: WorkingDraftController; draftId?: string }

  let { projectRoot = '', onNavigate, draft }: { projectRoot?: string; onNavigate: (hash: string) => void; draft?: WorkingDraftBinding } = $props()

  const controller = draft?.controller ?? null
  /** The content the draft already had when this form was opened, if any. */
  const restored: WorkingDraftContent | null = controller?.view().local ?? null

  /**
   * Every editable field, as the single record a working draft is stored and
   * sent in. `native` starts true: a handoff with nothing configured runs in
   * this environment. A saved draft brings its own values with it.
   */
  let content = $state<WorkingDraftContent>(restored ? { ...restored } : { ...emptyWorkingDraftContent(), native: true })

  /**
   * The content last handed to the controller, or adopted from it. It is not
   * reactive on purpose: it is what tells the autosave effect whether a change
   * is the user's keystroke or the server's answer.
   */
  let synced: WorkingDraftContent | null = restored ? { ...restored } : null

  // A working draft is autosaved. The controller stores the content on this
  // device before it sends anything, so this is the whole write path.
  $effect(() => {
    if (!controller) return
    const snapshot = { ...content }
    if (sameWorkingDraftContent(snapshot, synced)) return
    synced = snapshot
    void controller.edit(snapshot)
  })
  // Content the controller changed on its own — a conflict resolution, a copy
  // adopted from another editor, a discard — replaces what the form shows.
  $effect(() => {
    if (!controller) return
    return controller.subscribe((view) => {
      if (!view.local || sameWorkingDraftContent(view.local, synced)) return
      synced = { ...view.local }
      content = { ...view.local }
    })
  })

  let repos = $state<RepoRow[]>([]), workspaces = $state<WorkspaceListItem[]>([]), profiles = $state<string[]>([])
  let settings = $state<ProjectSettings | null>(null)
  let error = $state(''), settingsError = $state(''), busy = $state(false)
  let item = $state<FactoryItem | null>(null), pending = $state<FactoryCreate | null>(null)

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
  const repoBranch = $derived(repos.find((r) => r.id === content.repoId)?.branch || 'main')

  // Effective values, each with the source it came from.
  const workspace = $derived(pick(content.workspaceOverride, factoryWorkspace, projectWorkspace))
  const implementer = $derived(pick(content.implementerOverride, profileOf(factoryDefaults.implementerRouting), projectProfile))
  const reviewer = $derived(pick(content.reviewerOverride, profileOf(factoryDefaults.reviewerRouting), projectProfile))
  const branch = $derived<Effective>(content.targetBranchOverride.trim()
    ? { value: content.targetBranchOverride.trim(), source: 'handoff' }
    : factoryBranch ? { value: factoryBranch, source: 'factory' } : { value: repoBranch, source: 'repository' })
  const maxRounds = $derived(pickLimit(content.maxRoundsOverride, factoryDefaults.maxRounds, 5))
  const ciPoll = $derived(pickLimit(content.ciPollOverride, factoryDefaults.ciPollSeconds, 15))
  const ciWait = $derived(pickLimit(content.ciWaitOverride, factoryDefaults.ciWaitSeconds, 1800))
  const reviewerInstructionsEffective = $derived(pick('', factoryDefaults.reviewerInstructions ?? '', ''))

  // A native handoff suppresses the *project agent* workspace fallback; only a
  // separately configured factory workspace remains an explicit conflict.
  const workspaceConflict = $derived(content.native && factoryWorkspace !== '')

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
    if (content.checksMode === 'none') return { checks: [], error: '' }
    if (content.checksMode === 'inherit') return { checks: factoryDefaults.expectedChecks ?? [], error: '' }
    try { return { checks: parseChecks(content.expectedChecksText), error: '' } }
    catch (e) { return { checks: [], error: errMessage(e) } }
  })
  const checksEffective = $derived(
    content.checksMode === 'inherit'
      ? (factoryDefaults.expectedChecks?.length
        ? `Project factory default: ${formatChecks(factoryDefaults.expectedChecks)}`
        : 'Not configured — no required checks are inherited')
      : content.checksMode === 'none'
        ? 'This handoff: no required checks'
        : (checkPlan.error ? checkPlan.error : `This handoff: ${formatChecks(checkPlan.checks)}`),
  )
  // Kept visible when a saved value is no longer in a picker, so the choice is
  // never silently discarded.
  const keepUnavailable = (options: { value: string; label: string }[], value: string) =>
    value && !options.some((o) => o.value === value) ? [...options, { value, label: `${value} (unavailable)` }] : options
  const repoChoices = $derived(keepUnavailable(repos.map((r) => ({ value: r.id, label: r.id })), content.repoId))
  const wsOptions = $derived(workspaces.map((w) => ({ value: w.source === 'repo' ? `repo:${w.name}` : w.name, label: w.source === 'repo' ? `${w.name} (repo)` : w.name })))
  const workspaceChoices = $derived(keepUnavailable(wsOptions, content.workspaceOverride))
  // Each role keeps its own unavailable value. Sharing one list would leave the
  // second role's saved profile out of its picker, so the select would show a
  // blank choice and touching it would silently replace a value the user never
  // meant to change.
  const profileOptions = $derived(profiles.map((p) => ({ value: p, label: p })))
  const implementerChoices = $derived(keepUnavailable(profileOptions, content.implementerOverride))
  const reviewerChoices = $derived(keepUnavailable(profileOptions, content.reviewerOverride))

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
        // the default when nothing else can supply an environment. A saved
        // draft's own choice is not a default to be overwritten here.
        if (!content.nativeChosen && !restored) content.native = !(next.factoryDefaults?.workspace || next.workspace)
        if (!content.repoId && next.intake.repoId) content.repoId = next.intake.repoId
      } catch (e) { settingsError = errMessage(e) }
    }
    if (!content.repoId && repos.length === 1) content.repoId = repos[0].id
  }

  function positive(text: string, what: string): number {
    const n = Number(text)
    if (!Number.isFinite(n) || n <= 0) throw new Error(`${what} must be a positive number`)
    return Math.trunc(n)
  }

  function buildConfig(): FactoryCreateConfig {
    if (workspaceConflict) throw new Error(`The project's factory workspace "${factoryWorkspace}" cannot be combined with a native run. Choose a non-native environment, or change the project's factory settings.`)
    if (!content.native && !workspace.value) throw new Error('This project has no workspace. Choose one, or run natively.')
    if (!implementer.value || !reviewer.value) throw new Error('Choose both model profiles, or set a project model profile.')
    if (!branch.value) throw new Error('Choose a target branch')
    if (checkPlan.error) throw new Error(checkPlan.error)
    if (content.checksMode === 'custom' && !checkPlan.checks.length) throw new Error('Enter at least one expected check, or choose no required checks')
    if (!checkPlan.checks.length && !content.noRequiredCiAcknowledged) throw new Error('Confirm that this repository has no required CI, or enter expected checks')

    const config: FactoryCreateConfig = { repoId: content.repoId, projectRoot, native: content.native, implementerAuto: content.implementerAuto, noRequiredCiAcknowledged: content.noRequiredCiAcknowledged }
    // Every field the backend can resolve is omitted while it follows: an
    // empty string or zero would be a different, explicit choice.
    if (!content.native && content.workspaceOverride) config.workspace = content.workspaceOverride
    // A target branch is required to create: inherit the project's factory
    // default when it has one, otherwise submit the repository's branch.
    if (content.targetBranchOverride.trim()) config.targetBranch = content.targetBranchOverride.trim()
    else if (!factoryBranch) config.targetBranch = repoBranch
    if (content.implementerOverride) config.implementerRouting = { profile: content.implementerOverride }
    if (content.reviewerOverride) config.reviewerRouting = { profile: content.reviewerOverride }
    if (content.reviewerInstructions.trim()) config.reviewerInstructions = content.reviewerInstructions.trim()
    if (content.checksMode !== 'inherit') config.expectedChecks = checkPlan.checks
    if (content.maxRoundsOverride.trim()) config.maxRounds = positive(content.maxRoundsOverride, 'Total rounds')
    if (content.ciPollOverride.trim()) config.ciPollSeconds = positive(content.ciPollOverride, 'CI poll seconds')
    if (content.ciWaitOverride.trim()) config.ciWaitSeconds = positive(content.ciWaitOverride, 'CI wait seconds')
    return config
  }

  function payload(): FactoryCreate {
    if (!content.title.trim() || !content.plan.trim()) throw new Error('Title and plan are required')
    if (!content.repoId) throw new Error('Choose a registered repository')
    return { requestId: crypto.randomUUID(), title: content.title.trim(), plan: content.plan, spec: content.spec, config: buildConfig() }
  }

  /** Identity of a request, so a byte-identical retry keeps the same ID. */
  const signature = (r: FactoryCreate): string => JSON.stringify({ ...r, requestId: '' })

  async function save() {
    busy = true; error = ''
    try {
      const next = payload()
      const retry = pending && signature(pending) === signature(next) ? pending : next
      pending = retry
      item = await createFactoryItem(retry)
      pending = null
    } catch (e) { error = errMessage(e) }
    finally { busy = false }
  }

  // An in-progress handoff is only lost on purpose: the browser covers reload
  // and close; nothing here claims the draft outlives the tab. A working draft
  // is already kept on this device by its controller, so it needs no warning.
  function hasInput(): boolean {
    return !!(content.title.trim() || content.plan.trim() || content.spec.trim())
  }
  $effect(() => {
    if (controller) return
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
  {#if !controller}
    <div class="flex flex-wrap items-center justify-between gap-2">
      <div class="flex flex-wrap gap-3">
        <a class="text-sm text-accent underline" href="#factory" onclick={(e) => { e.preventDefault(); onNavigate('#factory') }}>Factory</a>
        <a class="text-sm text-accent underline" href={formatWorkingDraftsRoute({}, projectRoot)} onclick={(e) => { e.preventDefault(); onNavigate(formatWorkingDraftsRoute({}, projectRoot)) }}>Working drafts</a>
      </div>
      {#if projectRoot}
        <a class="text-sm text-accent underline" href={formatProjectRoute(projectRoot)} target="_blank" rel="noopener noreferrer" aria-label="Open project settings in a new tab">Project settings</a>
      {/if}
    </div>
  {/if}
  <h1 class="text-xl font-semibold">{controller ? 'Working draft' : 'New factory work item'}</h1>
  {#if settingsError}
    <Card class="border-attention text-sm" role="alert" data-testid="settings-warning">
      Project settings could not be read ({settingsError}). Inherited values are unknown here; set anything you need explicitly. Your input is kept.
    </Card>
  {/if}
  {#if error}
    <Card role="alert" class="border-attention text-sm">{error}{#if error.includes('network') || error.includes('fetch') || error.includes('offline')} <span>Your input is kept, and an unchanged retry reuses the same request ID.</span>{/if}</Card>
  {/if}
  {#if item}
    <Card class="flex flex-col gap-3" data-testid="draft-preview">
      <h2 class="font-semibold">Draft saved — review before starting</h2>
      <p>{item.title} · {item.repoId} · {item.state}</p>
      <p class="whitespace-pre-wrap break-words text-sm">{item.draft.plan}</p>
      <dl class="grid gap-x-3 gap-y-1 text-sm sm:grid-cols-[max-content_minmax(0,1fr)]">
        <dt class="text-muted">Target branch</dt><dd data-testid="draft-target-branch">{item.draft.config.targetBranch || 'Not set'}</dd>
        <dt class="text-muted">Environment</dt><dd data-testid="draft-environment">{item.draft.config.native ? 'Native' : item.draft.config.workspace || 'No workspace'}</dd>
        <dt class="text-muted">Host</dt><dd>{item.draft.config.hostId || 'local'}</dd>
        <dt class="text-muted">Implementer</dt><dd data-testid="draft-implementer">{profileOf(item.draft.config.implementerRouting) || 'Not set'}</dd>
        <dt class="text-muted">Reviewer</dt><dd data-testid="draft-reviewer">{profileOf(item.draft.config.reviewerRouting) || 'Not set'}</dd>
        <dt class="text-muted">Expected CI checks</dt>
        <dd data-testid="draft-checks">{#if item.draft.config.expectedChecks.length}{#each item.draft.config.expectedChecks as c (formatCheck(c))}<span class="mr-2 font-mono">{formatCheck(c)}</span>{/each}{:else}None — no required CI acknowledged{/if}</dd>
        <dt class="text-muted">Rounds</dt><dd data-testid="draft-rounds">{item.draft.config.maxRounds}</dd>
        <dt class="text-muted">CI poll / wait</dt><dd>{item.draft.config.ciPollSeconds}s / {item.draft.config.ciWaitSeconds}s</dd>
        <dt class="text-muted">Reviewer instructions</dt><dd class="whitespace-pre-wrap break-words">{item.draft.config.reviewerInstructions || 'None'}</dd>
      </dl>
      <a class="text-accent underline" href={formatFactoryRoute({ itemId: item.id })} onclick={(e) => { e.preventDefault(); onNavigate(formatFactoryRoute({ itemId: item!.id })) }}>Review and approve this draft</a>
    </Card>
  {:else}
    <form class="grid gap-4" onsubmit={(e) => { e.preventDefault(); if (!controller) void save() }}>
      <Card class="grid gap-3">
        <label class="grid gap-1 text-sm">Title <input class="w-full min-w-0 rounded border border-border bg-bg p-2" bind:value={content.title} required /></label>
        <label class="grid gap-1 text-sm">Plan <textarea class="min-h-40 w-full min-w-0 rounded border border-border bg-bg p-2" bind:value={content.plan} required></textarea></label>
        <label class="grid gap-1 text-sm">Specification (optional) <textarea class="min-h-24 w-full min-w-0 rounded border border-border bg-bg p-2" bind:value={content.spec}></textarea></label>
      </Card>
      <Card class="grid gap-3">
        <h2 class="text-sm font-semibold">Environment</h2>
        <label class="grid gap-1 text-sm">Repository <select class="w-full min-w-0 rounded border border-border bg-bg p-2" bind:value={content.repoId} required><option value="">Choose repository</option>{#each repoChoices as repo (repo.value)}<option value={repo.value}>{repo.label}</option>{/each}</select></label>
        <label class="grid gap-1 text-sm">Target branch
          <input class="w-full min-w-0 rounded border border-border bg-bg p-2" bind:value={content.targetBranchOverride} placeholder={branch.value} />
        </label>
        <p class="text-xs text-muted" data-testid="target-branch-effective">{describe(branch, 'no branch to target')}</p>
        <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={content.native} onchange={() => (content.nativeChosen = true)} /> Native environment</label>
        <label class="grid gap-1 text-sm">Workspace
          <select class="w-full min-w-0 rounded border border-border bg-bg p-2" bind:value={content.workspaceOverride} disabled={content.native} onchange={() => { content.native = false; content.nativeChosen = true }}>
            <option value="">Follow inherited setting</option>
            {#each workspaceChoices as ws (ws.value)}<option value={ws.value}>{ws.label}</option>{/each}
          </select>
        </label>
        <p class="text-xs text-muted" data-testid="workspace-effective">{content.native ? 'Native environment — no workspace is used' : describe(workspace, 'choose a workspace or run natively')}</p>
        {#if workspaceConflict}
          <p class="text-xs text-attention" data-testid="workspace-conflict">The project's factory workspace "{factoryWorkspace}" and a native run cannot both apply. Choose a non-native environment above, or change the project's factory settings.</p>
        {/if}
      </Card>
      <Card class="grid gap-3">
        <h2 class="text-sm font-semibold">Model profiles</h2>
        <label class="grid gap-1 text-sm">Implementer model profile
          <select class="w-full min-w-0 rounded border border-border bg-bg p-2" bind:value={content.implementerOverride}>
            <option value="">Follow inherited setting</option>
            {#each implementerChoices as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
          </select>
        </label>
        <p class="text-xs text-muted" data-testid="implementer-effective">{describe(implementer, 'set a project model profile or choose one here')}</p>
        <label class="grid gap-1 text-sm">Reviewer model profile
          <select class="w-full min-w-0 rounded border border-border bg-bg p-2" bind:value={content.reviewerOverride}>
            <option value="">Follow inherited setting</option>
            {#each reviewerChoices as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
          </select>
        </label>
        <p class="text-xs text-muted" data-testid="reviewer-effective">{describe(reviewer, 'set a project model profile or choose one here')}</p>
        <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={content.implementerAuto} /> Implementer auto permission setting</label>
      </Card>
      <Card class="grid gap-3">
        <label class="grid gap-1 text-sm">Reviewer instructions <textarea class="min-h-24 w-full min-w-0 rounded border border-border bg-bg p-2" bind:value={content.reviewerInstructions} placeholder={factoryDefaults.reviewerInstructions || ''}></textarea></label>
        <p class="text-xs text-muted" data-testid="reviewer-instructions-effective">{content.reviewerInstructions.trim() ? 'This handoff: its own instructions' : describe(reviewerInstructionsEffective, 'no instructions')}</p>
        <label class="grid gap-1 text-sm">Expected checks source
          <select class="w-full min-w-0 rounded border border-border bg-bg p-2" bind:value={content.checksMode}>
            <option value="inherit">Inherit from the project's factory defaults</option>
            <option value="none">No required checks</option>
            <option value="custom">List this handoff's checks</option>
          </select>
        </label>
        {#if content.checksMode === 'custom'}
          <label class="grid gap-1 text-sm">Expected CI checks (one per line: check_run:name:appId or commit_status:context) <textarea class="min-h-24 w-full min-w-0 rounded border border-border bg-bg p-2 font-mono" bind:value={content.expectedChecksText}></textarea></label>
        {/if}
        <p class="text-xs text-muted" data-testid="checks-effective">{checksEffective}</p>
        <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={content.noRequiredCiAcknowledged} /> This repository has no required CI</label>
        <label class="grid gap-1 text-sm">Total rounds
          <input class="w-full min-w-0 rounded border border-border bg-bg p-2" type="number" min="1" value={content.maxRoundsOverride} oninput={(e) => (content.maxRoundsOverride = e.currentTarget.value)} placeholder={maxRounds.value} />
        </label>
        <p class="text-xs text-muted" data-testid="max-rounds-effective">{describe(maxRounds, 'no round limit')}</p>
        <details class="min-w-0">
          <summary class="cursor-pointer text-xs text-muted">Advanced</summary>
          <div class="mt-2 grid gap-3">
            <label class="grid gap-1 text-sm">CI poll seconds
              <input class="w-full min-w-0 rounded border border-border bg-bg p-2" type="number" min="1" value={content.ciPollOverride} oninput={(e) => (content.ciPollOverride = e.currentTarget.value)} placeholder={ciPoll.value} />
            </label>
            <p class="text-xs text-muted" data-testid="ci-poll-effective">{describe(ciPoll, 'no polling interval')}</p>
            <label class="grid gap-1 text-sm">CI wait seconds
              <input class="w-full min-w-0 rounded border border-border bg-bg p-2" type="number" min="1" value={content.ciWaitOverride} oninput={(e) => (content.ciWaitOverride = e.currentTarget.value)} placeholder={ciWait.value} />
            </label>
            <p class="text-xs text-muted" data-testid="ci-wait-effective">{describe(ciWait, 'no wait limit')}</p>
          </div>
        </details>
      </Card>
      {#if !controller}
        <Button type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save draft'}</Button>
      {/if}
    </form>
  {/if}
</div>
