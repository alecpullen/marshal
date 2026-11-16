<script module lang="ts">
  import { errMessage, type ProjectSettings, type RepoRow } from '../lib/api'
  import type { FactoryCheckKey, FactoryCreate, FactoryCreateConfig, FactoryDefaults } from '../lib/factory'
  import type { WorkingDraftContent } from '../lib/working-drafts'

  /**
   * The handoff's configuration rules, as pure functions of a working draft's
   * content, its project's settings and its repositories.
   *
   * The conversion review in `WorkingDrafts.svelte` imports them, so what it
   * shows before converting is exactly what this form would submit: one
   * implementation of the inheritance rules, not two that can drift.
   */
  export type EffectiveSource = 'handoff' | 'factory' | 'project' | 'repository' | 'builtin' | 'none'
  export interface Effective { value: string; source: EffectiveSource }
  const SOURCE_TEXT: Record<EffectiveSource, string> = {
    handoff: 'This handoff',
    factory: 'Project factory default',
    project: 'Project agent default',
    repository: 'Repository branch',
    builtin: 'Built-in',
    none: 'Not configured',
  }
  /** One effective value and where it came from, worded once for every surface. */
  export const describeEffective = (e: Effective, missing: string): string =>
    e.value ? `${SOURCE_TEXT[e.source]}: ${e.value}` : `${SOURCE_TEXT.none} — ${missing}`
  const describe = describeEffective
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

  /** A control a failed rule can point at, so the message can sit beside it. */
  export type FactoryField = 'title' | 'plan' | 'repoId' | 'implementer' | 'reviewer' | 'checks' | 'noCi'
  /**
   * A rule's refusal, naming the control it is about when one exists. The form
   * shows the message above the handoff and marks the control; a rule with no
   * single control leaves the field empty.
   */
  export class FactoryInputError extends Error {
    field: FactoryField | ''
    constructor(field: FactoryField | '', message: string) {
      super(message)
      this.name = 'FactoryInputError'
      this.field = field
    }
  }

  const formatCheck = (c: FactoryCheckKey): string =>
    c.kind === 'commit_status' ? `${c.kind}:${c.name}` : `${c.kind}:${c.name}:${c.appId}`
  const formatChecks = (list: FactoryCheckKey[]): string => (list.length ? list.map(formatCheck).join(', ') : 'none')
  function parseChecks(text: string): FactoryCheckKey[] {
    return text.split('\n').map((line) => line.trim()).filter(Boolean).map((line) => {
      const [kind, name, app] = line.split(':')
      if (kind === 'check_run' && name && Number(app) > 0) return { kind, name, appId: Number(app) } as FactoryCheckKey
      if (kind === 'commit_status' && name && !app) return { kind, name, appId: 0 } as FactoryCheckKey
      throw new FactoryInputError('checks', 'Each check must be check_run:name:appId or commit_status:context')
    })
  }
  function positive(text: string, what: string): number {
    const n = Number(text)
    if (!Number.isFinite(n) || n <= 0) throw new FactoryInputError('', `${what} must be a positive number`)
    return Math.trunc(n)
  }
  // Kept visible when a saved value is no longer in a picker, so the choice is
  // never silently discarded.
  const keepUnavailable = (options: { value: string; label: string }[], value: string) =>
    value && !options.some((o) => o.value === value) ? [...options, { value, label: `${value} (unavailable)` }] : options

  /** Everything the form shows and the review shows, from one draft and project. */
  export interface FactoryInput {
    content: WorkingDraftContent
    projectRoot: string
    settings: ProjectSettings | null
    repos: RepoRow[]
  }

  export interface FactoryResolution {
    factoryDefaults: FactoryDefaults
    projectWorkspace: string
    projectProfile: string
    factoryWorkspace: string
    factoryBranch: string
    repoBranch: string
    workspace: Effective
    implementer: Effective
    reviewer: Effective
    branch: Effective
    maxRounds: Effective
    ciPoll: Effective
    ciWait: Effective
    reviewerInstructionsEffective: Effective
    workspaceConflict: boolean
    checkPlan: { checks: FactoryCheckKey[]; error: string }
    checksEffective: string
  }

  function resolveChecks(content: WorkingDraftContent, defaults: FactoryDefaults): { checks: FactoryCheckKey[]; error: string } {
    if (content.checksMode === 'none') return { checks: [], error: '' }
    if (content.checksMode === 'inherit') return { checks: defaults.expectedChecks ?? [], error: '' }
    try { return { checks: parseChecks(content.expectedChecksText), error: '' } }
    catch (e) { return { checks: [], error: errMessage(e) } }
  }

  function describeChecks(content: WorkingDraftContent, plan: { checks: FactoryCheckKey[]; error: string }, defaults: FactoryDefaults): string {
    if (content.checksMode === 'inherit') {
      return defaults.expectedChecks?.length
        ? `Project factory default: ${formatChecks(defaults.expectedChecks)}`
        : 'Not configured — no required checks are inherited'
    }
    if (content.checksMode === 'none') return 'This handoff: no required checks'
    return plan.error ? plan.error : `This handoff: ${formatChecks(plan.checks)}`
  }

  /** Every effective value, each with the source it came from. */
  export function resolveFactory(input: FactoryInput): FactoryResolution {
    const { content, settings, repos } = input
    const factoryDefaults: FactoryDefaults = settings?.factoryDefaults ?? {}
    const projectWorkspace = settings?.workspace ?? ''
    const projectProfile = profileOf(settings?.routing)
    const factoryWorkspace = factoryDefaults.workspace ?? ''
    const factoryBranch = factoryDefaults.targetBranch ?? ''
    const repoBranch = repos.find((r) => r.id === content.repoId)?.branch || 'main'
    const checkPlan = resolveChecks(content, factoryDefaults)
    return {
      factoryDefaults,
      projectWorkspace,
      projectProfile,
      factoryWorkspace,
      factoryBranch,
      repoBranch,
      workspace: pick(content.workspaceOverride, factoryWorkspace, projectWorkspace),
      implementer: pick(content.implementerOverride, profileOf(factoryDefaults.implementerRouting), projectProfile),
      reviewer: pick(content.reviewerOverride, profileOf(factoryDefaults.reviewerRouting), projectProfile),
      branch: content.targetBranchOverride.trim()
        ? { value: content.targetBranchOverride.trim(), source: 'handoff' }
        : factoryBranch
          ? { value: factoryBranch, source: 'factory' }
          : { value: repoBranch, source: 'repository' },
      maxRounds: pickLimit(content.maxRoundsOverride, factoryDefaults.maxRounds, 5),
      ciPoll: pickLimit(content.ciPollOverride, factoryDefaults.ciPollSeconds, 15),
      ciWait: pickLimit(content.ciWaitOverride, factoryDefaults.ciWaitSeconds, 1800),
      reviewerInstructionsEffective: pick('', factoryDefaults.reviewerInstructions ?? '', ''),
      // A native handoff suppresses the *project agent* workspace fallback; only
      // a separately configured factory workspace remains an explicit conflict.
      workspaceConflict: content.native && factoryWorkspace !== '',
      checkPlan,
      checksEffective: describeChecks(content, checkPlan, factoryDefaults),
    }
  }

  /** The configuration this handoff submits, or the reason it cannot be one. */
  export function factoryConfigOf(input: FactoryInput): FactoryCreateConfig {
    const { content, projectRoot, repos } = input
    const r = resolveFactory(input)
    if (r.workspaceConflict) throw new FactoryInputError('', `The project's factory workspace "${r.factoryWorkspace}" cannot be combined with a native run. Choose a non-native environment, or change the project's factory settings.`)
    if (!content.native && !r.workspace.value) throw new FactoryInputError('', 'This project has no workspace. Choose one, or run natively.')
    if (!r.implementer.value) throw new FactoryInputError('implementer', 'Choose an implementer model profile, or set a project model profile.')
    if (!r.reviewer.value) throw new FactoryInputError('reviewer', 'Choose a reviewer model profile, or set a project model profile.')
    if (!r.branch.value) throw new FactoryInputError('', 'Choose a target branch')
    if (r.checkPlan.error) throw new FactoryInputError('checks', r.checkPlan.error)
    if (content.checksMode === 'custom' && !r.checkPlan.checks.length) throw new FactoryInputError('checks', 'Enter at least one expected check, or choose no required checks')
    if (!r.checkPlan.checks.length && !content.noRequiredCiAcknowledged) throw new FactoryInputError('noCi', 'Confirm that this repository has no required CI, or enter expected checks')

    const config: FactoryCreateConfig = { repoId: content.repoId, projectRoot, native: content.native, implementerAuto: content.implementerAuto, noRequiredCiAcknowledged: content.noRequiredCiAcknowledged }
    // Every field the backend can resolve is omitted while it follows: an
    // empty string or zero would be a different, explicit choice.
    if (!content.native && content.workspaceOverride) config.workspace = content.workspaceOverride
    // A target branch is required to create: inherit the project's factory
    // default when it has one, otherwise submit the repository's branch.
    if (content.targetBranchOverride.trim()) config.targetBranch = content.targetBranchOverride.trim()
    else if (!r.factoryBranch) config.targetBranch = r.repoBranch
    if (content.implementerOverride) config.implementerRouting = { profile: content.implementerOverride }
    if (content.reviewerOverride) config.reviewerRouting = { profile: content.reviewerOverride }
    if (content.reviewerInstructions.trim()) config.reviewerInstructions = content.reviewerInstructions.trim()
    if (content.checksMode !== 'inherit') config.expectedChecks = r.checkPlan.checks
    if (content.maxRoundsOverride.trim()) config.maxRounds = positive(content.maxRoundsOverride, 'Total rounds')
    if (content.ciPollOverride.trim()) config.ciPollSeconds = positive(content.ciPollOverride, 'CI poll seconds')
    if (content.ciWaitOverride.trim()) config.ciWaitSeconds = positive(content.ciWaitOverride, 'CI wait seconds')
    return config
  }

  /** The exact body a handoff — or a conversion review — sends. */
  export function factoryRequestOf(input: FactoryInput & { requestId: string }): FactoryCreate {
    const { content, requestId } = input
    if (!content.title.trim()) throw new FactoryInputError('title', 'A title is required')
    if (!content.plan.trim()) throw new FactoryInputError('plan', 'A plan is required')
    if (!content.repoId) throw new FactoryInputError('repoId', 'Choose a registered repository')
    return { requestId, title: content.title.trim(), plan: content.plan, spec: content.spec, config: factoryConfigOf(input) }
  }
</script>

<script lang="ts">
  import { onMount, tick } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import EffectiveConfigSummary from '../lib/factory/EffectiveConfigSummary.svelte'
  import ProfileSelect from '../lib/factory/ProfileSelect.svelte'
  import { resolvedConfigRows, type ConfigSummaryRow } from '../lib/factory/config'
  import { createFactoryItem, getModels, getProjectSettings, listRepos, listWorkspaces, type WorkspaceListItem } from '../lib/api'
  import type { FactoryItem } from '../lib/factory'
  import type { RouteLeaveGuard } from '../lib/navigationGuard'
  import { formatFactoryRoute, formatProjectRoute, formatWorkingDraftsRoute, plannerRoute } from '../lib/routes'
  import { emptyWorkingDraftContent, sameWorkingDraftContent, type WorkingDraftController } from '../lib/working-drafts'

  /**
   * A saved working draft this view edits. With one, every change is handed to
   * the controller (which keeps it on this device and autosaves it) and the
   * manual "Save draft" path below is not offered. Without one, this is the
   * handoff form it always was.
   */
  interface WorkingDraftBinding { controller?: WorkingDraftController; draftId?: string }

  let {
    projectRoot = '',
    onNavigate,
    // The shell's route guard, so unsaved input is protected wherever the
    // person leaves from — a nav link, the palette, or browser Back.
    registerGuard = () => {},
    // The shell's store, so a saved draft is already in the list the moment
    // it exists. A view rendered without it simply does not publish.
    upsert = () => {},
    draft: binding,
  }: {
    projectRoot?: string
    onNavigate: (hash: string) => void
    registerGuard?: (guard?: RouteLeaveGuard) => void
    upsert?: (item: FactoryItem) => void
    draft?: WorkingDraftBinding
  } = $props()

  const controller = binding?.controller ?? null
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
  // The draft the bridge created from this handoff, shown for review.
  let saved = $state<FactoryItem | null>(null)
  let pending = $state<FactoryCreate | null>(null)

  /**
   * Every effective value comes from the same resolution the conversion review
   * uses, so the two surfaces cannot describe the same handoff differently.
   */
  const resolved = $derived(resolveFactory({ content, projectRoot, settings, repos }))
  const factoryDefaults = $derived(resolved.factoryDefaults)
  const workspace = $derived(resolved.workspace)
  const implementer = $derived(resolved.implementer)
  const reviewer = $derived(resolved.reviewer)
  const branch = $derived(resolved.branch)
  const maxRounds = $derived(resolved.maxRounds)
  const ciPoll = $derived(resolved.ciPoll)
  const ciWait = $derived(resolved.ciWait)
  // A native handoff suppresses the *project agent* workspace fallback; only a
  // separately configured factory workspace remains an explicit conflict.
  const workspaceConflict = $derived(resolved.workspaceConflict)
  const repoChoices = $derived(keepUnavailable(repos.map((r) => ({ value: r.id, label: r.id })), content.repoId))
  const wsOptions = $derived(workspaces.map((w) => ({ value: w.source === 'repo' ? `repo:${w.name}` : w.name, label: w.source === 'repo' ? `${w.name} (repo)` : w.name })))
  const workspaceChoices = $derived(keepUnavailable(wsOptions, content.workspaceOverride))

  const checksRow = $derived<ConfigSummaryRow>(checksSummary())
  function checksSummary(): ConfigSummaryRow {
    const label = 'Expected checks'
    if (content.checksMode === 'none') {
      // The deliberate "none" choice is only a decision once it is acknowledged.
      return { label, value: 'None', source: 'This handoff: no required checks', warning: !content.noRequiredCiAcknowledged }
    }
    if (content.checksMode === 'inherit') {
      const inherited = factoryDefaults.expectedChecks ?? []
      return inherited.length
        ? { label, value: formatChecks(inherited), source: SOURCE_TEXT.factory }
        : { label, value: 'None', source: `${SOURCE_TEXT.none} — no required checks are inherited`, warning: true }
    }
    if (resolved.checkPlan.error) return { label, value: resolved.checkPlan.error, source: SOURCE_TEXT.handoff, warning: true }
    return {
      label,
      value: resolved.checkPlan.checks.length ? formatChecks(resolved.checkPlan.checks) : 'None',
      source: SOURCE_TEXT.handoff,
      warning: !resolved.checkPlan.checks.length && !content.noRequiredCiAcknowledged,
    }
  }

  // What this handoff will create, and where every value came from. It reads
  // the same effective values the payload is built from, so an override shows
  // up here the moment it is chosen and nothing is written back into a field
  // that is still following.
  const summaryRows = $derived<ConfigSummaryRow[]>([
    { label: 'Repository', value: content.repoId || 'Not set' },
    { label: 'Project', value: projectRoot || 'Not set' },
    { label: 'Target branch', value: branch.value || 'Not set', source: SOURCE_TEXT[branch.source] },
    content.native
      ? { label: 'Environment', value: 'Native', source: 'This handoff — no workspace is used' }
      : { label: 'Environment', value: workspace.value || 'Not set', source: describe(workspace, 'choose a workspace or run natively') },
    { label: 'Implementer', value: implementer.value || 'Not set', source: describe(implementer, 'set a project model profile or choose one here') },
    { label: 'Reviewer', value: reviewer.value || 'Not set', source: describe(reviewer, 'set a project model profile or choose one here') },
    {
      label: 'Reviewer instructions',
      value: content.reviewerInstructions.trim() ? 'This handoff\'s own instructions' : factoryDefaults.reviewerInstructions || 'None',
      source: content.reviewerInstructions.trim() ? SOURCE_TEXT.handoff : factoryDefaults.reviewerInstructions ? SOURCE_TEXT.factory : SOURCE_TEXT.none,
    },
    checksRow,
    { label: 'Auto permission', value: content.implementerAuto ? 'Automatic' : 'Asks before each action', source: SOURCE_TEXT.handoff },
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
        // the default when nothing else can supply an environment. A saved
        // draft's own choice is not a default to be overwritten here.
        if (!content.nativeChosen && !restored) content.native = !(next.factoryDefaults?.workspace || next.workspace)
        if (!content.repoId && next.intake.repoId) content.repoId = next.intake.repoId
      } catch (e) { settingsError = errMessage(e) }
    }
    if (!content.repoId && repos.length === 1) content.repoId = repos[0].id
  }

  /** The controls a failed submission can point at, in the order they appear. */
  type FieldKey = FactoryField
  const FIELD_ORDER: FieldKey[] = ['title', 'plan', 'repoId', 'implementer', 'reviewer', 'checks', 'noCi']

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

  /** Identity of a request, so a byte-identical retry keeps the same ID. */
  const signature = (r: FactoryCreate): string => JSON.stringify({ ...r, requestId: '' })

  async function save() {
    busy = true; error = ''
    // The previous attempt's field links are stale the moment this one starts.
    fieldErrors = {}
    try {
      const next = factoryRequestOf({ content, projectRoot, settings, repos, requestId: crypto.randomUUID() })
      const retry = pending && signature(pending) === signature(next) ? pending : next
      pending = retry
      const created = await createFactoryItem(retry)
      // Publish the saved draft before its preview renders, so whichever view
      // the person opens next reads the store rather than fetching again.
      upsert(created)
      saved = created
      pending = null
    } catch (e) {
      error = errMessage(e)
      if (e instanceof FactoryInputError && e.field) {
        // One rule failed, so one control is marked — and focused once the
        // message it is described by has been rendered.
        fieldErrors = { [e.field]: e.message }
        await tick()
        const first = FIELD_ORDER.find((field) => field in fieldErrors)
        if (first) focusField(first)
      }
    } finally { busy = false }
  }

  // An in-progress handoff is only lost on purpose: the browser covers reload
  // and close; nothing here claims the draft outlives the tab. A working draft
  // is already kept on this device by its controller, so it needs no warning.
  function hasInput(): boolean {
    return !!(content.title.trim() || content.plan.trim() || content.spec.trim())
  }
  /*
    A draft the bridge already saved is not unsaved input: leaving the form
    costs nothing at that point, so neither the route guard nor the reload
    warning may treat the text still in the fields as dirty.
  */
  const hasUnsavedInput = $derived(!controller && !saved && hasInput())
  /*
    The guard asks before the shell changes the route, so it is registered for
    as long as this screen is mounted and cleared when it is torn down — a
    screen that is gone must never answer for the one that replaced it.
  */
  $effect(() => {
    if (controller) return
    registerGuard(() => {
      if (!hasUnsavedInput) return true
      return window.confirm('You have an unsaved factory handoff. Leave without saving it?')
    })
    return () => registerGuard(undefined)
  })
  $effect(() => {
    if (controller) return
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
  {#if !controller}
    <div class="flex flex-wrap items-center justify-between gap-2">
      <div class="flex flex-wrap gap-3">
        <a class="text-sm text-accent underline" href="#factory" onclick={(e) => { e.preventDefault(); onNavigate('#factory') }}>Factory</a>
        <a class="text-sm text-accent underline" href={formatWorkingDraftsRoute({}, projectRoot)} onclick={(e) => { e.preventDefault(); onNavigate(formatWorkingDraftsRoute({}, projectRoot)) }}>Working drafts</a>
        <!--
          The planner drafts the plan this form would otherwise ask for. The
          link is additive: this form still creates a handoff typed by hand,
          with the same rules it always applied.
        -->
        <a
          class="text-sm text-accent underline"
          href={plannerRoute({ kind: 'new', ...(projectRoot ? { project: projectRoot } : {}) })}
          onclick={(e) => { e.preventDefault(); onNavigate(plannerRoute({ kind: 'new', ...(projectRoot ? { project: projectRoot } : {}) })) }}
        >Plan with a Studio planner</a>
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
  {#if saved}
    <div class="grid gap-3" data-testid="draft-preview">
      <Card class="flex flex-col gap-1">
        <h2 class="font-semibold">Draft saved — review before starting</h2>
        <p class="text-sm text-muted">{saved.title} · {saved.repoId} · {saved.state}</p>
      </Card>
      <EffectiveConfigSummary
        rows={resolvedConfigRows(saved.draft.config)}
        title="Resolved configuration"
        consequence="Approving this draft is what authorizes execution under the configuration above. Saving it started nothing."
        plan={saved.draft.plan}
        spec={saved.draft.spec}
      />
      <a class="text-accent underline" href={formatFactoryRoute({ itemId: saved.id })} onclick={(e) => { e.preventDefault(); onNavigate(formatFactoryRoute({ itemId: saved!.id })) }}>Review and approve this draft</a>
    </div>
  {:else}
    <form class="grid gap-4" novalidate onsubmit={(e) => { e.preventDefault(); if (!controller) void save() }}>
      <div class="grid gap-4 min-[1200px]:grid-cols-[minmax(0,1fr)_minmax(0,22rem)] min-[1200px]:items-start">
        <div class="grid gap-4">
          <Card class="grid gap-3" data-testid="handoff-scope">
            <h2 class="text-sm font-semibold">Scope</h2>
            <label class="grid gap-1 text-sm">Title
              <input
                class="rounded border border-border bg-bg p-2"
                class:border-attention={!!fieldErrors.title}
                bind:value={content.title}
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
                bind:value={content.plan}
                data-field="plan"
                aria-invalid={fieldErrors.plan ? 'true' : undefined}
                aria-describedby={fieldErrors.plan ? 'error-plan' : undefined}
                oninput={() => clearField('plan')}
              ></textarea>
            </label>
            {#if fieldErrors.plan}<p class="text-xs text-attention" id="error-plan" data-testid="error-plan">{fieldErrors.plan}</p>{/if}
            <label class="grid gap-1 text-sm">Specification (optional) <textarea class="min-h-24 rounded border border-border bg-bg p-2" bind:value={content.spec}></textarea></label>
          </Card>
          <details class="min-w-0" open={!phone}>
            <summary class="cursor-pointer text-sm"><h2 class="inline text-sm font-semibold">Repository and environment</h2></summary>
            <Card class="mt-2 grid gap-3">
              <label class="grid gap-1 text-sm">Repository
                <select
                  class="rounded border border-border bg-bg p-2"
                  class:border-attention={!!fieldErrors.repoId}
                  bind:value={content.repoId}
                  data-field="repoId"
                  aria-invalid={fieldErrors.repoId ? 'true' : undefined}
                  aria-describedby={fieldErrors.repoId ? 'error-repoId' : undefined}
                  onchange={() => clearField('repoId')}
                ><option value="">Choose repository</option>{#each repoChoices as repo (repo.value)}<option value={repo.value}>{repo.label}</option>{/each}</select>
              </label>
              {#if fieldErrors.repoId}<p class="text-xs text-attention" id="error-repoId" data-testid="error-repoId">{fieldErrors.repoId}</p>{/if}
              <label class="grid gap-1 text-sm">Target branch
                <input class="rounded border border-border bg-bg p-2" bind:value={content.targetBranchOverride} placeholder={branch.value} />
              </label>
              <p class="text-xs text-muted" data-testid="target-branch-effective">{describe(branch, 'no branch to target')}</p>
              <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={content.native} onchange={() => (content.nativeChosen = true)} /> Native environment</label>
              <label class="grid gap-1 text-sm">Workspace
                <select class="rounded border border-border bg-bg p-2" bind:value={content.workspaceOverride} disabled={content.native} onchange={() => { content.native = false; content.nativeChosen = true }}>
                  <option value="">Follow inherited setting</option>
                  {#each workspaceChoices as ws (ws.value)}<option value={ws.value}>{ws.label}</option>{/each}
                </select>
              </label>
              <p class="text-xs text-muted" data-testid="workspace-effective">{content.native ? 'Native environment — no workspace is used' : describe(workspace, 'choose a workspace or run natively')}</p>
              {#if workspaceConflict}
                <p class="text-xs text-attention" data-testid="workspace-conflict">The project's factory workspace "{resolved.factoryWorkspace}" and a native run cannot both apply. Choose a non-native environment above, or change the project's factory settings.</p>
              {/if}
            </Card>
          </details>
          <details class="min-w-0" open={!phone}>
            <summary class="cursor-pointer text-sm"><h2 class="inline text-sm font-semibold">Roles and review</h2></summary>
            <Card class="mt-2 grid gap-3">
              <div class="grid gap-1" data-field="implementer">
                <ProfileSelect
                  label="Implementer model profile"
                  bind:value={content.implementerOverride}
                  profiles={profiles}
                  followLabel="Follow inherited setting"
                  invalid={!!fieldErrors.implementer}
                  describedBy={fieldErrors.implementer ? 'error-implementer' : undefined}
                />
                {#if fieldErrors.implementer}<p class="text-xs text-attention" id="error-implementer" data-testid="error-implementer">{fieldErrors.implementer}</p>{/if}
              </div>
              <p class="text-xs text-muted" data-testid="implementer-effective">{describe(implementer, 'set a project model profile or choose one here')}</p>
              <div class="grid gap-1" data-field="reviewer">
                <ProfileSelect
                  label="Reviewer model profile"
                  bind:value={content.reviewerOverride}
                  profiles={profiles}
                  followLabel="Follow inherited setting"
                  invalid={!!fieldErrors.reviewer}
                  describedBy={fieldErrors.reviewer ? 'error-reviewer' : undefined}
                />
                {#if fieldErrors.reviewer}<p class="text-xs text-attention" id="error-reviewer" data-testid="error-reviewer">{fieldErrors.reviewer}</p>{/if}
              </div>
              <p class="text-xs text-muted" data-testid="reviewer-effective">{describe(reviewer, 'set a project model profile or choose one here')}</p>
              <label class="grid gap-1 text-sm">Reviewer instructions <textarea class="min-h-24 rounded border border-border bg-bg p-2" bind:value={content.reviewerInstructions} placeholder={factoryDefaults.reviewerInstructions || ''}></textarea></label>
              <p class="text-xs text-muted" data-testid="reviewer-instructions-effective">{content.reviewerInstructions.trim() ? 'This handoff: its own instructions' : describe(resolved.reviewerInstructionsEffective, 'no instructions')}</p>
              <label class="grid gap-1 text-sm">Expected checks source
                <select class="rounded border border-border bg-bg p-2" bind:value={content.checksMode}>
                  <option value="inherit">Inherit from the project's factory defaults</option>
                  <option value="none">No required checks</option>
                  <option value="custom">List this handoff's checks</option>
                </select>
              </label>
              {#if content.checksMode === 'custom'}
                <label class="grid gap-1 text-sm">Expected CI checks (one per line: check_run:name:appId or commit_status:context)
                  <textarea
                    class="min-h-24 rounded border border-border bg-bg p-2 font-mono"
                    class:border-attention={!!fieldErrors.checks}
                    bind:value={content.expectedChecksText}
                    data-field="checks"
                    aria-invalid={fieldErrors.checks ? 'true' : undefined}
                    aria-describedby={fieldErrors.checks ? 'error-checks' : undefined}
                    oninput={() => clearField('checks')}
                  ></textarea>
                </label>
              {/if}
              {#if fieldErrors.checks}<p class="text-xs text-attention" id="error-checks" data-testid="error-checks">{fieldErrors.checks}</p>{/if}
              <p class="text-xs text-muted" data-testid="checks-effective">{resolved.checksEffective}</p>
              <label class="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  bind:checked={content.noRequiredCiAcknowledged}
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
              <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={content.implementerAuto} /> Implementer auto permission setting</label>
              <label class="grid gap-1 text-sm">Total rounds
                <input class="rounded border border-border bg-bg p-2" type="number" min="1" value={content.maxRoundsOverride} oninput={(e) => (content.maxRoundsOverride = e.currentTarget.value)} placeholder={maxRounds.value} />
              </label>
              <p class="text-xs text-muted" data-testid="max-rounds-effective">{describe(maxRounds, 'no round limit')}</p>
              <details class="min-w-0">
                <summary class="cursor-pointer text-xs text-muted">Advanced</summary>
                <div class="mt-2 grid gap-3">
                  <label class="grid gap-1 text-sm">CI poll seconds
                    <input class="rounded border border-border bg-bg p-2" type="number" min="1" value={content.ciPollOverride} oninput={(e) => (content.ciPollOverride = e.currentTarget.value)} placeholder={ciPoll.value} />
                  </label>
                  <p class="text-xs text-muted" data-testid="ci-poll-effective">{describe(ciPoll, 'no polling interval')}</p>
                  <label class="grid gap-1 text-sm">CI wait seconds
                    <input class="rounded border border-border bg-bg p-2" type="number" min="1" value={content.ciWaitOverride} oninput={(e) => (content.ciWaitOverride = e.currentTarget.value)} placeholder={ciWait.value} />
                  </label>
                  <p class="text-xs text-muted" data-testid="ci-wait-effective">{describe(ciWait, 'no wait limit')}</p>
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
      {#if !controller}
        <Button type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save draft'}</Button>
      {/if}
    </form>
  {/if}
</div>
