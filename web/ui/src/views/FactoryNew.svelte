<script lang="ts">
  import { onMount, tick } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import EffectiveConfigSummary from '../lib/factory/EffectiveConfigSummary.svelte'
  import ProfileSelect from '../lib/factory/ProfileSelect.svelte'
  import { formatFactoryCheck, keepUnavailable, profileOf, resolvedConfigRows, type ConfigSummaryRow } from '../lib/factory/config'
  import { createFactoryItem, errMessage, getModels, getProjectSettings, listRepos, listWorkspaces, type ProjectSettings, type RepoRow, type WorkspaceListItem } from '../lib/api'
  import type { FactoryCheckKey, FactoryCreate, FactoryCreateConfig, FactoryItem } from '../lib/factory'
  import type { RouteLeaveGuard } from '../lib/navigationGuard'
  import { formatFactoryRoute, formatProjectRoute } from '../lib/routes'

  let {
    projectRoot = '',
    onNavigate,
    // The shell's route guard, so unsaved input is protected wherever the
    // person leaves from — a nav link, the palette, or browser Back.
    registerGuard = () => {},
    // The shell's store, so a saved draft is already in the list the moment
    // it exists. A view rendered without it simply does not publish.
    upsert = () => {},
  }: { projectRoot?: string; onNavigate: (hash: string) => void; registerGuard?: (guard?: RouteLeaveGuard) => void; upsert?: (item: FactoryItem) => void } = $props()

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
  // A row names the value and, when it follows something, where it came from.
  const sourced = (label: string, e: Effective, missing: string): ConfigSummaryRow =>
    e.value ? { label, value: e.value, source: SOURCE_TEXT[e.source] } : { label, value: 'Not set', source: `${SOURCE_TEXT.none} — ${missing}` }

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
  const reviewerInstructionsEffective = $derived(reviewerInstructions.trim() ? '' : factoryDefaults.reviewerInstructions ?? '')

  // A native handoff suppresses the *project agent* workspace fallback; only a
  // separately configured factory workspace remains an explicit conflict.
  const workspaceConflict = $derived(native && factoryWorkspace !== '')

  const formatChecks = (list: FactoryCheckKey[]): string => (list.length ? list.map(formatFactoryCheck).join(', ') : 'none')
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
  const checksRow = $derived<ConfigSummaryRow>(checksSummary())
  function checksSummary(): ConfigSummaryRow {
    const label = 'Expected checks'
    if (checksMode === 'none') {
      // The deliberate "none" choice is only a decision once it is acknowledged.
      return { label, value: 'None', source: 'This handoff: no required checks', warning: !noRequiredCiAcknowledged }
    }
    if (checksMode === 'inherit') {
      const inherited = factoryDefaults.expectedChecks ?? []
      return inherited.length
        ? { label, value: formatChecks(inherited), source: SOURCE_TEXT.factory }
        : { label, value: 'None', source: `${SOURCE_TEXT.none} — no required checks are inherited`, warning: true }
    }
    if (checkPlan.error) return { label, value: checkPlan.error, source: SOURCE_TEXT.handoff, warning: true }
    return {
      label,
      value: checkPlan.checks.length ? formatChecks(checkPlan.checks) : 'None',
      source: SOURCE_TEXT.handoff,
      warning: !checkPlan.checks.length && !noRequiredCiAcknowledged,
    }
  }
  // Kept visible when a saved value is no longer in a picker, so the choice is
  // never silently discarded.
  const wsOptions = $derived(workspaces.map((w) => ({ value: w.source === 'repo' ? `repo:${w.name}` : w.name, label: w.source === 'repo' ? `${w.name} (repo)` : w.name })))
  const workspaceChoices = $derived(keepUnavailable(wsOptions, workspaceOverride))

  // What this handoff will create, and where every value came from. It reads
  // the same effective values the payload is built from, so an override shows
  // up here the moment it is chosen and nothing is written back into a field
  // that is still following.
  const summaryRows = $derived<ConfigSummaryRow[]>([
    { label: 'Repository', value: repoId || 'Not set' },
    { label: 'Project', value: projectRoot || 'Not set' },
    { label: 'Target branch', value: branch.value || 'Not set', source: SOURCE_TEXT[branch.source] },
    native
      ? { label: 'Environment', value: 'Native', source: 'This handoff — no workspace is used' }
      : sourced('Environment', workspace, 'choose a workspace or run natively'),
    sourced('Implementer', implementer, 'set a project model profile or choose one here'),
    sourced('Reviewer', reviewer, 'set a project model profile or choose one here'),
    {
      label: 'Reviewer instructions',
      value: reviewerInstructions.trim() ? 'This handoff\'s own instructions' : reviewerInstructionsEffective || 'None',
      source: reviewerInstructions.trim() ? SOURCE_TEXT.handoff : reviewerInstructionsEffective ? SOURCE_TEXT.factory : SOURCE_TEXT.none,
    },
    checksRow,
    { label: 'Auto permission', value: implementerAuto ? 'Automatic' : 'Asks before each action', source: SOURCE_TEXT.handoff },
    { label: 'Total rounds', value: maxRounds.value, source: SOURCE_TEXT[maxRounds.source] },
    { label: 'CI poll', value: `${ciPoll.value}s`, source: SOURCE_TEXT[ciPoll.source] },
    { label: 'CI wait', value: `${ciWait.value}s`, source: SOURCE_TEXT[ciWait.source] },
  ])

  /*
    Below 768px the form's sections are disclosures and the Scope section and
    the summary stay open, so a phone shows the work being handed over, then
    the consequence. A missing `matchMedia` keeps the wide form, which every
    control still renders in.
  */
  const PHONE_QUERY = '(max-width: 767px)'
  let phone = $state(false)
  onMount(() => {
    const width = typeof window.matchMedia === 'function' ? window.matchMedia(PHONE_QUERY) : null
    phone = width?.matches ?? false
    const onWidth = (e: MediaQueryListEvent) => (phone = e.matches)
    width?.addEventListener('change', onWidth)
    return () => width?.removeEventListener('change', onWidth)
  })

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

  /** The controls a failed submission can point at, in the order they appear. */
  type FieldKey = 'title' | 'plan' | 'repoId' | 'implementer' | 'reviewer' | 'checks' | 'noCi'
  const FIELD_ORDER: FieldKey[] = ['title', 'plan', 'repoId', 'implementer', 'reviewer', 'checks', 'noCi']

  /**
   * A validation failure that names its control. Every rule lives where it
   * always did — this only adds the link between a rule and the field it is
   * about, so the message can be shown beside the control as well as above
   * the form. A rule with no single control leaves the field empty.
   */
  class FieldError extends Error {
    field: FieldKey | ''
    constructor(field: FieldKey | '', message: string) {
      super(message)
      this.field = field
    }
  }

  let fieldErrors = $state<Partial<Record<FieldKey, string>>>({})
  function clearField(field: FieldKey) {
    if (!(field in fieldErrors)) return
    const next = { ...fieldErrors }
    delete next[field]
    fieldErrors = next
  }
  function focusField(field: FieldKey) {
    const host = document.querySelector<HTMLElement>(`[data-field="${field}"]`)
    if (!host) return
    // The marker sits on the control itself or, for a shared control, on the
    // wrapper around it: focus whatever a person would actually type into.
    const target = host.matches('input, select, textarea, button')
      ? host
      : host.querySelector<HTMLElement>('input, select, textarea, button')
    ;(target ?? host).focus()
  }

  function positive(text: string, what: string): number {
    const n = Number(text)
    if (!Number.isFinite(n) || n <= 0) throw new Error(`${what} must be a positive number`)
    return Math.trunc(n)
  }

  function buildConfig(): FactoryCreateConfig {
    if (workspaceConflict) throw new FieldError('', `The project's factory workspace "${factoryWorkspace}" cannot be combined with a native run. Choose a non-native environment, or change the project's factory settings.`)
    if (!native && !workspace.value) throw new FieldError('', 'This project has no workspace. Choose one, or run natively.')
    if (!implementer.value) throw new FieldError('implementer', 'Choose an implementer model profile, or set a project model profile.')
    if (!reviewer.value) throw new FieldError('reviewer', 'Choose a reviewer model profile, or set a project model profile.')
    if (!branch.value) throw new FieldError('', 'Choose a target branch')
    if (checkPlan.error) throw new FieldError('checks', checkPlan.error)
    if (checksMode === 'custom' && !checkPlan.checks.length) throw new FieldError('checks', 'Enter at least one expected check, or choose no required checks')
    if (!checkPlan.checks.length && !noRequiredCiAcknowledged) throw new FieldError('noCi', 'Confirm that this repository has no required CI, or enter expected checks')

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
    if (!title.trim()) throw new FieldError('title', 'A title is required')
    if (!plan.trim()) throw new FieldError('plan', 'A plan is required')
    if (!repoId) throw new FieldError('repoId', 'Choose a registered repository')
    return { requestId: crypto.randomUUID(), title: title.trim(), plan, spec, config: buildConfig() }
  }

  /** Identity of a request, so a byte-identical retry keeps the same ID. */
  const signature = (r: FactoryCreate): string => JSON.stringify({ ...r, requestId: '' })

  async function save() {
    busy = true; error = ''
    // The previous attempt's field links are stale the moment this one starts.
    fieldErrors = {}
    try {
      const next = payload()
      const retry = pending && signature(pending) === signature(next) ? pending : next
      pending = retry
      const saved = await createFactoryItem(retry)
      // Publish the saved draft before its preview renders, so whichever view
      // the person opens next reads the store rather than fetching again.
      upsert(saved)
      draft = saved
      pending = null
    } catch (e) {
      error = errMessage(e)
      if (e instanceof FieldError) {
        // One rule failed, so one control is marked — and focused once the
        // message it is described by has been rendered.
        if (e.field) fieldErrors = { [e.field]: e.message }
        await tick()
        const first = FIELD_ORDER.find((field) => field in fieldErrors)
        if (first) focusField(first)
      }
    } finally { busy = false }
  }

  // An in-progress handoff is only lost on purpose: the browser covers reload
  // and close; nothing here claims the draft outlives the tab.
  function hasInput(): boolean {
    return !!(title.trim() || plan.trim() || spec.trim())
  }
  /*
    A draft the bridge already saved is not unsaved input: leaving the form
    costs nothing at that point, so neither the route guard nor the reload
    warning may treat the text still in the fields as dirty.
  */
  const hasUnsavedInput = $derived(!draft && hasInput())
  /*
    The guard asks before the shell changes the route, so it is registered for
    as long as this screen is mounted and cleared when it is torn down — a
    screen that is gone must never answer for the one that replaced it.
  */
  $effect(() => {
    registerGuard(() => {
      if (!hasUnsavedInput) return true
      return window.confirm('You have an unsaved factory handoff. Leave without saving it?')
    })
    return () => registerGuard(undefined)
  })
  $effect(() => {
    const warn = (e: BeforeUnloadEvent) => {
      if (!hasUnsavedInput) return
      e.preventDefault()
      e.returnValue = ''
    }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  })
</script>
<div class="mx-auto flex max-w-4xl flex-col gap-4 p-4 sm:p-6 min-[1200px]:max-w-7xl">
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
    <div class="grid gap-3" data-testid="draft-preview">
      <Card class="flex flex-col gap-1">
        <h2 class="font-semibold">Draft saved — review before starting</h2>
        <p class="text-sm text-muted">{draft.title} · {draft.repoId} · {draft.state}</p>
      </Card>
      <EffectiveConfigSummary
        rows={resolvedConfigRows(draft.draft.config)}
        title="Resolved configuration"
        consequence="Approving this draft is what authorizes execution under the configuration above. Saving it started nothing."
        plan={draft.draft.plan}
        spec={draft.draft.spec}
      />
      <a class="text-accent underline" href={formatFactoryRoute({ itemId: draft.id })} onclick={(e) => { e.preventDefault(); onNavigate(formatFactoryRoute({ itemId: draft!.id })) }}>Review and approve this draft</a>
    </div>
  {:else}
    <form class="grid gap-4" novalidate onsubmit={(e) => { e.preventDefault(); void save() }}>
      <div class="grid gap-4 min-[1200px]:grid-cols-[minmax(0,1fr)_minmax(0,22rem)] min-[1200px]:items-start">
        <div class="grid gap-4">
          <Card class="grid gap-3" data-testid="handoff-scope">
            <h2 class="text-sm font-semibold">Scope</h2>
            <label class="grid gap-1 text-sm">Title
              <input
                class="rounded border border-border bg-bg p-2"
                class:border-attention={!!fieldErrors.title}
                bind:value={title}
                data-field="title"
                aria-invalid={fieldErrors.title ? 'true' : undefined}
                aria-describedby={fieldErrors.title ? 'error-title' : undefined}
                oninput={() => clearField('title')}
              />
            </label>
            {#if fieldErrors.title}<p class="text-xs text-attention" id="error-title" data-testid="error-title">{fieldErrors.title}</p>{/if}
            <label class="grid gap-1 text-sm">Plan
              <textarea
                class="min-h-40 rounded border border-border bg-bg p-2"
                class:border-attention={!!fieldErrors.plan}
                bind:value={plan}
                data-field="plan"
                aria-invalid={fieldErrors.plan ? 'true' : undefined}
                aria-describedby={fieldErrors.plan ? 'error-plan' : undefined}
                oninput={() => clearField('plan')}
              ></textarea>
            </label>
            {#if fieldErrors.plan}<p class="text-xs text-attention" id="error-plan" data-testid="error-plan">{fieldErrors.plan}</p>{/if}
            <label class="grid gap-1 text-sm">Specification (optional) <textarea class="min-h-24 rounded border border-border bg-bg p-2" bind:value={spec}></textarea></label>
          </Card>
          <details class="min-w-0" open={!phone}>
            <summary class="cursor-pointer text-sm"><h2 class="inline text-sm font-semibold">Repository and environment</h2></summary>
            <Card class="mt-2 grid gap-3">
              <label class="grid gap-1 text-sm">Repository
                <select
                  class="rounded border border-border bg-bg p-2"
                  class:border-attention={!!fieldErrors.repoId}
                  bind:value={repoId}
                  data-field="repoId"
                  aria-invalid={fieldErrors.repoId ? 'true' : undefined}
                  aria-describedby={fieldErrors.repoId ? 'error-repoId' : undefined}
                  onchange={() => clearField('repoId')}
                ><option value="">Choose repository</option>{#each repos as repo (repo.id)}<option value={repo.id}>{repo.id}</option>{/each}</select>
              </label>
              {#if fieldErrors.repoId}<p class="text-xs text-attention" id="error-repoId" data-testid="error-repoId">{fieldErrors.repoId}</p>{/if}
              <label class="grid gap-1 text-sm">Target branch
                <input class="rounded border border-border bg-bg p-2" bind:value={targetBranchOverride} placeholder={branch.value} />
              </label>
              <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={native} onchange={() => (nativeChosen = true)} /> Native environment</label>
              <label class="grid gap-1 text-sm">Workspace
                <select class="rounded border border-border bg-bg p-2" bind:value={workspaceOverride} disabled={native} onchange={() => { native = false; nativeChosen = true }}>
                  <option value="">Follow inherited setting</option>
                  {#each workspaceChoices as ws (ws.value)}<option value={ws.value}>{ws.label}</option>{/each}
                </select>
              </label>
              {#if workspaceConflict}
                <p class="text-xs text-attention" data-testid="workspace-conflict">The project's factory workspace "{factoryWorkspace}" and a native run cannot both apply. Choose a non-native environment above, or change the project's factory settings.</p>
              {/if}
            </Card>
          </details>
          <details class="min-w-0" open={!phone}>
            <summary class="cursor-pointer text-sm"><h2 class="inline text-sm font-semibold">Roles and review</h2></summary>
            <Card class="mt-2 grid gap-3">
              <div class="grid gap-1" data-field="implementer">
                <ProfileSelect
                  label="Implementer model profile"
                  bind:value={implementerOverride}
                  profiles={profiles}
                  followLabel="Follow inherited setting"
                  invalid={!!fieldErrors.implementer}
                  describedBy={fieldErrors.implementer ? 'error-implementer' : undefined}
                />
                {#if fieldErrors.implementer}<p class="text-xs text-attention" id="error-implementer" data-testid="error-implementer">{fieldErrors.implementer}</p>{/if}
              </div>
              <div class="grid gap-1" data-field="reviewer">
                <ProfileSelect
                  label="Reviewer model profile"
                  bind:value={reviewerOverride}
                  profiles={profiles}
                  followLabel="Follow inherited setting"
                  invalid={!!fieldErrors.reviewer}
                  describedBy={fieldErrors.reviewer ? 'error-reviewer' : undefined}
                />
                {#if fieldErrors.reviewer}<p class="text-xs text-attention" id="error-reviewer" data-testid="error-reviewer">{fieldErrors.reviewer}</p>{/if}
              </div>
              <label class="grid gap-1 text-sm">Reviewer instructions <textarea class="min-h-24 rounded border border-border bg-bg p-2" bind:value={reviewerInstructions} placeholder={factoryDefaults.reviewerInstructions || ''}></textarea></label>
              <label class="grid gap-1 text-sm">Expected checks source
                <select class="rounded border border-border bg-bg p-2" bind:value={checksMode}>
                  <option value="inherit">Inherit from the project's factory defaults</option>
                  <option value="none">No required checks</option>
                  <option value="custom">List this handoff's checks</option>
                </select>
              </label>
              {#if checksMode === 'custom'}
                <label class="grid gap-1 text-sm">Expected CI checks (one per line: check_run:name:appId or commit_status:context)
                  <textarea
                    class="min-h-24 rounded border border-border bg-bg p-2 font-mono"
                    class:border-attention={!!fieldErrors.checks}
                    bind:value={expectedChecksText}
                    data-field="checks"
                    aria-invalid={fieldErrors.checks ? 'true' : undefined}
                    aria-describedby={fieldErrors.checks ? 'error-checks' : undefined}
                    oninput={() => clearField('checks')}
                  ></textarea>
                </label>
              {/if}
              {#if fieldErrors.checks}<p class="text-xs text-attention" id="error-checks" data-testid="error-checks">{fieldErrors.checks}</p>{/if}
              <label class="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  bind:checked={noRequiredCiAcknowledged}
                  data-field="noCi"
                  aria-invalid={fieldErrors.noCi ? 'true' : undefined}
                  aria-describedby={fieldErrors.noCi ? 'error-noCi' : undefined}
                  onchange={() => clearField('noCi')}
                /> This repository has no required CI
              </label>
              {#if fieldErrors.noCi}<p class="text-xs text-attention" id="error-noCi" data-testid="error-noCi">{fieldErrors.noCi}</p>{/if}
            </Card>
          </details>
          <details class="min-w-0" open={!phone}>
            <summary class="cursor-pointer text-sm"><h2 class="inline text-sm font-semibold">Authority and limits</h2></summary>
            <Card class="mt-2 grid gap-3">
              <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={implementerAuto} /> Implementer auto permission setting</label>
              <label class="grid gap-1 text-sm">Total rounds
                <input class="rounded border border-border bg-bg p-2" type="number" min="1" value={maxRoundsOverride} oninput={(e) => (maxRoundsOverride = e.currentTarget.value)} placeholder={maxRounds.value} />
              </label>
              <details class="min-w-0">
                <summary class="cursor-pointer text-xs text-muted">Advanced</summary>
                <div class="mt-2 grid gap-3">
                  <label class="grid gap-1 text-sm">CI poll seconds
                    <input class="rounded border border-border bg-bg p-2" type="number" min="1" value={ciPollOverride} oninput={(e) => (ciPollOverride = e.currentTarget.value)} placeholder={ciPoll.value} />
                  </label>
                  <label class="grid gap-1 text-sm">CI wait seconds
                    <input class="rounded border border-border bg-bg p-2" type="number" min="1" value={ciWaitOverride} oninput={(e) => (ciWaitOverride = e.currentTarget.value)} placeholder={ciWait.value} />
                  </label>
                </div>
              </details>
            </Card>
          </details>
        </div>
        <div class="grid gap-4 min-[1200px]:sticky min-[1200px]:top-4">
          <EffectiveConfigSummary
            rows={summaryRows}
            consequence="Save draft records this handoff and the configuration above. No work starts until you review the saved draft and approve it."
          />
        </div>
      </div>
      <Button type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save draft'}</Button>
    </form>
  {/if}
</div>
