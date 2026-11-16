<script lang="ts">
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Tag from '../lib/ui/Tag.svelte'
  import Tabs from '../lib/ui/Tabs.svelte'
  import {
    APIError,
    errMessage,
    getModels,
    getProjectHealth,
    getVersionedProjectSettings,
    getWorkspacePolicy,
    listClients,
    listRepos,
    listWorkspaces,
    putVersionedProjectSettings,
    type MCPClient,
    type ProjectHealth,
    type ProjectSettings,
    type RepoRow,
    type RoutingChoice,
    type VersionedProjectSettings,
    type WorkspaceListItem,
  } from '../lib/api'
  import type { FactoryCheckKey, FactoryDefaults } from '../lib/factory'
  import type { AgentRow, AgentTelemetry } from '../lib/fleet'
  import { MODES, SHIP_TARGETS, autoFrom, autoOf, healthChecks, soft, isolatedFrom, isolationOf, latestAgent, parseWorkspaceRef, type AutoChoice, type Dot, type IsolationChoice } from '../lib/project/model'
  import { shortName } from '../lib/utils'
  import type { RouteLeaveGuard } from '../lib/navigationGuard'
  import { formatAutomationRoute, formatFactoryRoute, type AutomationPage } from '../lib/routes'

  let { root, agents, telemetry = {}, onNavigate, registerGuard = () => {} }: { root: string; agents: AgentRow[]; telemetry?: Record<string, AgentTelemetry>; onNavigate: (hash: string) => void; registerGuard?: (guard?: RouteLeaveGuard) => void } = $props()

  let tab = $state<'overview' | 'session' | 'automations'>('overview')
  let error = $state('')
  let notice = $state('')
  let saving = $state(false)
  let loaded = $state(false)
  // True after the bridge refused a save because the settings moved on.
  let stale = $state(false)

  /**
   * The settings record and the version it was read at. Every write sends that
   * version back as `If-Match`, so this screen can never silently replace a
   * change another surface made in between.
   */
  let saved = $state<VersionedProjectSettings | null>(null)
  const settings = $derived(saved?.settings)

  // Workspace and model drafts. For the agent default '' means "not set"; for
  // a factory choice it means "follow the project default". Neither is ever
  // written as a fallback: an unset field is deleted from the record.
  let workspace = $state('')
  let factoryWorkspace = $state('')
  let profile = $state('')
  let implementerProfile = $state('')
  let reviewerProfile = $state('')
  // Session defaults.
  let mode = $state('')
  let autoMode = $state<AutoChoice>('default')
  let isolation = $state<IsolationChoice>('default')
  let shipTarget = $state('')
  /**
   * Automatic assessment of plan changes. It is the project's own field, not a
   * factory default: absent follows the handoff (which means on), and only a
   * deliberate change writes it, so saving an unrelated field never
   * materialises a value the user did not choose.
   */
  let autoAssessMode = $state<AutoChoice>('default')
  // Factory-only defaults, held as typed text so a cleared field stays absent.
  let targetBranch = $state('')
  let expectedChecksText = $state('')
  let reviewerInstructions = $state('')
  let maxRoundsText = $state('')
  let ciPollText = $state('')
  let ciWaitText = $state('')
  // A stored host, kept verbatim. Only a value this build does not offer is
  // shown; 'local' needs no field of its own but is preserved all the same.
  let factoryHost = $state('')
  // Intake drafts.
  let repoId = $state('')
  let labels = $state<string[]>([])
  let newLabel = $state('')
  let clientIds = $state<string[]>([])
  // Planner delegation drafts (MCP factory handoff). Off and unselected by
  // default: a client that was never designated answers nothing, and an off
  // toggle stores no client at all.
  let delegation = $state(false)
  let delegateClient = $state('')
  // The drafts as adopted, so an unsaved edit can be recognised.
  let baseline = $state('')

  const routingProfile = (r: Record<string, unknown> | undefined): string => (typeof r?.profile === 'string' ? r.profile : '')

  const checksText = (checks: FactoryCheckKey[] | undefined): string =>
    (checks ?? []).map((c) => (c.kind === 'commit_status' ? `${c.kind}:${c.name}` : `${c.kind}:${c.name}:${c.appId}`)).join('\n')

  function draftSignature(): string {
    return JSON.stringify([
      workspace, factoryWorkspace, profile, implementerProfile, reviewerProfile,
      mode, autoMode, isolation, shipTarget, autoAssessMode,
      targetBranch, expectedChecksText, reviewerInstructions, maxRoundsText, ciPollText, ciWaitText,
      repoId, labels, clientIds, delegation, delegateClient,
    ])
  }
  const dirty = $derived(!!saved && draftSignature() !== baseline)

  /**
   * Take the record's values into the drafts. Only a read or a save calls
   * this: a background fetch must never overwrite what the user is typing.
   */
  function adopt(v: VersionedProjectSettings) {
    saved = v
    const s = v.settings
    const f = s.factoryDefaults ?? {}
    workspace = s.workspace ?? ''
    profile = s.routing?.profile ?? ''
    mode = s.mode ?? ''
    autoMode = autoOf(s)
    isolation = isolationOf(s)
    shipTarget = s.shipTarget ?? ''
    // Absent is "follow the handoff", which is on; only an explicit false is a
    // stored override, so the control shows the effective value rather than a
    // default it invented.
    autoAssessMode = autoOf({ auto: s.autoAssessPlanChanges })
    factoryWorkspace = f.workspace ?? ''
    implementerProfile = routingProfile(f.implementerRouting)
    reviewerProfile = routingProfile(f.reviewerRouting)
    targetBranch = f.targetBranch ?? ''
    reviewerInstructions = f.reviewerInstructions ?? ''
    expectedChecksText = checksText(f.expectedChecks)
    maxRoundsText = f.maxRounds ? String(f.maxRounds) : ''
    ciPollText = f.ciPollSeconds ? String(f.ciPollSeconds) : ''
    ciWaitText = f.ciWaitSeconds ? String(f.ciWaitSeconds) : ''
    factoryHost = f.hostId ?? ''
    repoId = s.intake.repoId ?? ''
    labels = [...(s.intake.labels ?? [])]
    clientIds = [...(s.intake.clients ?? [])]
    // Both delegation fields are optional: absent means off and nobody
    // designated, which is what the drafts start from.
    delegation = s.plannerDelegationEnabled === true
    delegateClient = s.designatedMCPClientId ?? ''
    baseline = draftSignature()
    stale = false
  }

  // The pickers are optional context: a bridge without one of them still lets the rest of the page work.
  let workspaces = $state<WorkspaceListItem[]>([])
  let profiles = $state<string[]>([])
  let repos = $state<RepoRow[]>([])
  let clients = $state<MCPClient[]>([])

  async function load() {
    try {
      adopt(await getVersionedProjectSettings(root))
      error = ''
    } catch (e) {
      // A read that failed must not leave a save enabled: an unconditional
      // write would replace settings this screen never saw.
      saved = null
      error = errMessage(e)
    }
    loaded = true
    const [w, m, r, c] = await Promise.all([
      soft(listWorkspaces(), []),
      soft(getModels().then((x) => Object.keys(x.profiles).sort()), []),
      soft(listRepos(), []),
      soft(listClients(), []),
    ])
    workspaces = w
    profiles = m
    repos = r
    clients = c
  }

  const PROJECT_CHANGED = 'These settings changed elsewhere. Your edits are kept until you reload: reloading discards them and shows the saved values.'

  async function save(next: ProjectSettings, what: string) {
    if (!saved) {
      error = 'Project settings could not be read, so there is nothing safe to save over. Reload first.'
      return
    }
    saving = true
    error = ''
    notice = ''
    stale = false
    // What the drafts held when the write left. A field the user changes while
    // it is in flight is theirs: the response updates the stored record and the
    // version token, but never overwrites a draft typed after this point.
    const sent = draftSignature()
    try {
      const written = await putVersionedProjectSettings(root, next, saved.etag)
      if (draftSignature() === sent) {
        adopt(written)
      } else {
        saved = written
        baseline = sent
      }
      notice = `${what} saved`
      void loadPolicy()
    } catch (e) {
      // A failed write changes nothing: every draft stays as the user left it.
      if (e instanceof APIError && e.status === 412) {
        stale = true
        error = PROJECT_CHANGED
      } else {
        error = errMessage(e)
      }
    } finally {
      saving = false
    }
  }

  /**
   * A role route keeps every member the server sent, including ones this build
   * does not know. An empty profile removes only the profile member: a route
   * that never had one is not something saving an unrelated field may delete.
   * A route left with nothing at all is dropped.
   */
  function roleRoute(base: Record<string, unknown> | undefined, value: string): Record<string, unknown> | undefined {
    const route: Record<string, unknown> = { ...(base ?? {}) }
    if (value) route.profile = value
    else delete route.profile
    return Object.keys(route).length ? route : undefined
  }

  /** The same syntax the handoff form accepts; an empty list means "follow", not "no checks". */
  function parseChecks(): FactoryCheckKey[] {
    return expectedChecksText.split('\n').map((line) => line.trim()).filter(Boolean).map((line) => {
      const [kind, name, app] = line.split(':')
      if (kind === 'check_run' && name && Number(app) > 0) return { kind: 'check_run' as const, name, appId: Number(app) }
      if (kind === 'commit_status' && name && !app) return { kind: 'commit_status' as const, name, appId: 0 }
      throw new Error('Each check must be check_run:name:appId or commit_status:context')
    })
  }

  const positiveInt = (text: string): number | undefined => {
    const n = Number(text)
    return text.trim() && Number.isFinite(n) && n > 0 ? Math.trunc(n) : undefined
  }

  function factoryDefaultsBody(): FactoryDefaults | undefined {
    const stored = settings?.factoryDefaults
    const f: FactoryDefaults = {}
    // The only supported host is local, which needs no field. A host this
    // build does not offer is kept exactly as it was stored.
    if (factoryHost) f.hostId = factoryHost
    if (targetBranch.trim()) f.targetBranch = targetBranch.trim()
    if (factoryWorkspace) f.workspace = factoryWorkspace
    if (implementerProfile || stored?.implementerRouting) {
      const route = roleRoute(stored?.implementerRouting, implementerProfile)
      if (route) f.implementerRouting = route
    }
    if (reviewerProfile || stored?.reviewerRouting) {
      const route = roleRoute(stored?.reviewerRouting, reviewerProfile)
      if (route) f.reviewerRouting = route
    }
    if (reviewerInstructions.trim()) f.reviewerInstructions = reviewerInstructions.trim()
    const checks = parseChecks()
    if (checks.length) f.expectedChecks = checks
    const rounds = positiveInt(maxRoundsText)
    if (rounds !== undefined) f.maxRounds = rounds
    const poll = positiveInt(ciPollText)
    if (poll !== undefined) f.ciPollSeconds = poll
    const wait = positiveInt(ciWaitText)
    if (wait !== undefined) f.ciWaitSeconds = wait
    return Object.keys(f).length ? f : undefined
  }

  function defaultsBody(): ProjectSettings {
    const base = settings
    if (!base) throw new Error('Project settings could not be read; reload before saving.')
    // Only the profile changes here: every other routing member, including
    // ones this build does not know, is carried through untouched.
    const routing: RoutingChoice = { ...(base.routing ?? {}) }
    if (profile) routing.profile = profile
    else delete routing.profile

    const next: ProjectSettings = { ...base }
    if (workspace) next.workspace = workspace
    else delete next.workspace
    if (Object.keys(routing).length) next.routing = routing
    else delete next.routing
    if (mode) next.mode = mode
    else delete next.mode
    const auto = autoFrom(autoMode)
    if (auto === undefined) delete next.auto
    else next.auto = auto
    const isolated = isolatedFrom(isolation)
    if (isolated === undefined) delete next.isolated
    else next.isolated = isolated
    if (shipTarget) next.shipTarget = shipTarget as ProjectSettings['shipTarget']
    else delete next.shipTarget
    const factoryDefaults = factoryDefaultsBody()
    if (factoryDefaults) next.factoryDefaults = factoryDefaults
    else delete next.factoryDefaults
    // Only a deliberate choice writes this: following the handoff leaves the
    // record exactly as it was read, which is what "absent means on" requires.
    const assess = autoFrom(autoAssessMode)
    if (assess === undefined) delete next.autoAssessPlanChanges
    else next.autoAssessPlanChanges = assess
    return next
  }

  function saveDefaults() {
    try {
      void save(defaultsBody(), 'Defaults')
    } catch (e) {
      notice = ''
      error = errMessage(e)
    }
  }

  function saveIntake() {
    if (!settings) {
      error = 'Project settings could not be read, so there is nothing safe to save over. Reload first.'
      return
    }
    void save({ ...settings, intake: { repoId: repoId || undefined, labels: repoId ? labels : [], clients: clientIds } }, 'Intake')
  }

  /**
   * Only the two delegation fields change here; every other member of the
   * record is carried through exactly as it was read. Client records hold no
   * tokens, so nothing secret can be written back by this save.
   */
  function delegationBody(): ProjectSettings {
    const base = settings
    if (!base) throw new Error('Project settings could not be read; reload before saving.')
    const next: ProjectSettings = { ...base }
    if (delegation) next.plannerDelegationEnabled = true
    else delete next.plannerDelegationEnabled
    // Off stores no designation: an inert client ID in the record would look
    // like authority the user did not grant.
    if (delegation && delegateClient) next.designatedMCPClientId = delegateClient
    else delete next.designatedMCPClientId
    return next
  }

  /** Delegation with nobody designated answers nothing, so it is not a savable state. */
  const delegationIncomplete = $derived(delegation && !delegateClient)

  function saveDelegation() {
    try {
      void save(delegationBody(), 'Delegation')
    } catch (e) {
      notice = ''
      error = errMessage(e)
    }
  }

  function addLabel() {
    const l = newLabel.trim()
    // The bridge's issue poller watches one label per repo, so the card keeps one.
    if (l) labels = [l]
    newLabel = ''
  }
  const toggleClient = (id: string, on: boolean) => (clientIds = on ? [...new Set([...clientIds, id])] : clientIds.filter((c) => c !== id))

  // Health.
  let health = $state<ProjectHealth | null>(null)
  let healthBusy = $state(false)
  let healthError = $state('')
  async function loadHealth() {
    healthBusy = true
    try {
      health = await getProjectHealth(root)
      healthError = ''
    } catch (e) {
      healthError = errMessage(e)
    } finally {
      healthBusy = false
    }
  }
  const checks = $derived(health ? healthChecks(health) : [])
  const dotClass: Record<Dot, string> = { ok: 'bg-ok', warn: 'bg-warn', err: 'bg-err', unknown: 'bg-dim' }

  // Policy: the default workspace's [policy], read-only.
  let policy = $state<{ mode?: string; allow?: string[] } | null>(null)
  let policyNote = $state('')
  async function loadPolicy() {
    policy = null
    policyNote = ''
    if (!settings?.workspace) {
      policyNote = 'No default workspace is set.'
      return
    }
    const ref = parseWorkspaceRef(settings.workspace)
    if (ref.source === 'repo') {
      policyNote = 'This workspace comes from the repo; its policy is in the template file.'
      return
    }
    try {
      policy = (await getWorkspacePolicy(ref.name, ref.version)) ?? {}
    } catch (e) {
      policyNote = errMessage(e)
    }
  }
  const designerHash = $derived(settings?.workspace && parseWorkspaceRef(settings.workspace).source === 'studio' ? `#workspaces/${encodeURIComponent(parseWorkspaceRef(settings.workspace).name)}/edit` : '')

  $effect(() => {
    void load().then(loadPolicy)
    void loadHealth()
  })

  // Unsaved edits are only lost on purpose: in-app navigation asks the shell's
  // route guard, and the browser's own unload warning covers reload and close.
  function confirmLeave(): boolean {
    if (!dirty) return true
    return window.confirm('You have unsaved project settings. Leave without saving them?')
  }
  /*
    The guard asks before the shell changes the route, so it is registered for
    as long as this screen is mounted and cleared when it is torn down — a
    screen that is gone must never answer for the one that replaced it.
  */
  $effect(() => {
    registerGuard(confirmLeave)
    return () => registerGuard(undefined)
  })
  $effect(() => {
    const warn = (e: BeforeUnloadEvent) => {
      if (!dirty) return
      e.preventDefault()
      e.returnValue = ''
    }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  })

  // Session sheet: the telemetry of the project's most recent agent.
  const agent = $derived(latestAgent(agents, root))
  const tele = $derived(agent ? (telemetry[agent.id] ?? { contextPct: agent.contextPct ?? 0, changedFiles: agent.changedFiles ?? 0, at: 0 }) : undefined)

  const wsOptions = $derived(workspaces.map((w) => ({ value: w.source === 'repo' ? `repo:${w.name}` : w.name, label: w.source === 'repo' ? `${w.name} (repo)` : w.name })))
  /** A saved choice the picker no longer lists stays visible, labelled so it is not mistaken for an available one. */
  const keepUnavailable = (options: { value: string; label: string }[], value: string) =>
    value && !options.some((o) => o.value === value) ? [...options, { value, label: `${value} (unavailable)` }] : options
  const wsChoices = $derived(keepUnavailable(wsOptions, workspace))
  const factoryWsChoices = $derived(keepUnavailable(wsOptions, factoryWorkspace))
  const profileOptions = $derived(profiles.map((p) => ({ value: p, label: p })))
  const profileChoices = $derived(keepUnavailable(profileOptions, profile))
  const implementerChoices = $derived(keepUnavailable(profileOptions, implementerProfile))
  const reviewerChoices = $derived(keepUnavailable(profileOptions, reviewerProfile))
  // What each factory choice resolves to right now, inherited or overridden.
  const following = (own: string, inherited: string) => (own ? `${own} (own choice)` : `Following the project default: ${inherited || 'Not configured'}`)
  const effectiveWorkspace = $derived(following(factoryWorkspace, workspace))
  const effectiveImplementer = $derived(following(implementerProfile, profile))
  const effectiveReviewer = $derived(following(reviewerProfile, profile))
  const repoChoices = $derived([...new Set([...repos.map((r) => r.id), ...(repoId ? [repoId] : [])])])
  // A designated client the list no longer offers stays selected and labelled,
  // so saving an unrelated field cannot silently clear the designation.
  const clientChoices = $derived(keepUnavailable(clients.map((c) => ({ value: c.id, label: c.name || c.id })), delegateClient))
  const canReload = $derived(stale || (loaded && !saved))
  const field = 'w-full min-w-0 rounded border border-border bg-bg p-2 text-sm'
</script>

<div class="mx-auto flex max-w-5xl flex-col gap-4 p-3 sm:p-6">
  <header class="min-w-0">
    <p class="text-xs text-muted"><a class="text-accent hover:underline" href="#projects" onclick={(e) => { e.preventDefault(); onNavigate('#projects') }}>Projects</a> /</p>
    <h1 class="text-lg font-semibold" title={root}>{shortName(root)}</h1>
    <p class="truncate font-mono text-xs text-muted">{root}</p>
  </header>

  <div class="flex flex-wrap items-center gap-3 text-sm">
    <a class="text-accent underline" href={`#factory/new?project=${encodeURIComponent(root)}`} onclick={(e) => { e.preventDefault(); onNavigate(`#factory/new?project=${encodeURIComponent(root)}`) }}>Create factory work item for this project{repoId ? ` (${repoId})` : ''}</a>
    <a class="text-accent underline" href={formatFactoryRoute({ projectRoot: root })} onclick={(e) => { e.preventDefault(); onNavigate(formatFactoryRoute({ projectRoot: root })) }}>Open project factory</a>
  </div>
  <Tabs label="Project" tabs={[{ value: 'overview', label: 'Overview' }, { value: 'session', label: 'Session sheet' }, { value: 'automations', label: 'Automations' }]} value={tab} onchange={(t) => (tab = t as 'overview' | 'session' | 'automations')} />

  {#if error}
    <Card class="border-attention text-sm" role="alert">
      <p>{error}</p>
      {#if canReload}<Button variant="ghost" class="mt-2" onclick={() => void load()}>Reload</Button>{/if}
    </Card>
  {/if}
  {#if notice}<Card class="text-sm" role="status">{notice}</Card>{/if}

  {#if tab === 'overview'}
    <div class="grid gap-4 lg:grid-cols-2">
      <Card class="flex min-w-0 flex-col gap-3" data-testid="workspace-card">
        <h2 class="text-sm font-semibold">Workspace</h2>
        <p class="text-xs text-muted">New work in this project starts here. Drafts and work already created keep what they were made with.</p>
        <label class="flex min-w-0 flex-col gap-1 text-xs">Default for new agents
          <select aria-label="Workspace" bind:value={workspace} class={field}>
            <option value="">Not configured</option>
            {#each wsChoices as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
          </select>
        </label>
        <label class="flex min-w-0 flex-col gap-1 text-xs">Factory workspace
          <select aria-label="Factory workspace" bind:value={factoryWorkspace} class={field}>
            <option value="">Use project default</option>
            {#each factoryWsChoices as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
          </select>
        </label>
        <p class="text-xs text-muted" data-testid="factory-workspace-effective">{effectiveWorkspace}</p>
        {#if factoryHost && factoryHost !== 'local'}<p class="text-xs text-warn" data-testid="factory-host">Saved factory host {factoryHost} is not available to this screen; the value is kept.</p>{/if}
      </Card>

      <Card class="flex min-w-0 flex-col gap-3" data-testid="model-card">
        <h2 class="text-sm font-semibold">Model</h2>
        <p class="text-xs text-muted">The agent profile applies to new sessions; each factory role follows it until it chooses its own.</p>
        <label class="flex min-w-0 flex-col gap-1 text-xs">Default profile for new agents
          <select aria-label="Model profile" bind:value={profile} class={field}>
            <option value="">Engine default</option>
            {#each profileChoices as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
          </select>
        </label>
        <label class="flex min-w-0 flex-col gap-1 text-xs">Factory implementer profile
          <select aria-label="Implementer profile" bind:value={implementerProfile} class={field}>
            <option value="">Use project default</option>
            {#each implementerChoices as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
          </select>
        </label>
        <p class="text-xs text-muted" data-testid="implementer-effective">{effectiveImplementer}</p>
        <label class="flex min-w-0 flex-col gap-1 text-xs">Factory reviewer profile
          <select aria-label="Reviewer profile" bind:value={reviewerProfile} class={field}>
            <option value="">Use project default</option>
            {#each reviewerChoices as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
          </select>
        </label>
        <p class="text-xs text-muted" data-testid="reviewer-effective">{effectiveReviewer}</p>
      </Card>

      <Card class="flex min-w-0 flex-col gap-3" data-testid="factory-card">
        <h2 class="text-sm font-semibold">Factory</h2>
        <p class="text-xs text-muted">Defaults for new handoffs in this project. Leave a field empty to follow the handoff or the project default.</p>
        <label class="flex min-w-0 flex-col gap-1 text-xs">Target branch
          <input aria-label="Target branch" bind:value={targetBranch} placeholder="Follow the handoff" class={field} />
        </label>
        <label class="flex min-w-0 flex-col gap-1 text-xs">Expected CI checks
          <span class="text-muted">One per line: check_run:name:appId or commit_status:context</span>
          <textarea aria-label="Expected CI checks" bind:value={expectedChecksText} class="min-h-24 w-full min-w-0 rounded border border-border bg-bg p-2 font-mono"></textarea>
        </label>
        <label class="flex min-w-0 flex-col gap-1 text-xs">Reviewer instructions
          <textarea aria-label="Reviewer instructions" bind:value={reviewerInstructions} class="min-h-24 w-full min-w-0 rounded border border-border bg-bg p-2"></textarea>
        </label>
        <label class="flex min-w-0 flex-col gap-1 text-xs">Max rounds
          <input aria-label="Max rounds" type="number" min="1" value={maxRoundsText} oninput={(e) => (maxRoundsText = e.currentTarget.value)} placeholder="Follow the handoff" class={field} />
        </label>
        <label class="flex min-w-0 flex-col gap-1 text-xs">Automatic plan assessment
          <select aria-label="Automatic plan assessment" bind:value={autoAssessMode} class={field}>
            <option value="default">On — the default when nothing is set</option>
            <option value="on">On — your explicit choice</option>
            <option value="off">Off — assess only when asked</option>
          </select>
        </label>
        <p class="text-xs text-muted" data-testid="auto-assess-effective">
          {#if autoAssessMode === 'off'}
            Effective now: off. A proposed plan change is only assessed when someone asks for an assessment.
          {:else if autoAssessMode === 'on'}
            Effective now: on, because you turned it on. Every proposal is assessed automatically.
          {:else}
            Effective now: on. Projects assess plan changes unless they explicitly turn it off, and this one does not.
          {/if}
        </p>
        <details class="min-w-0">
          <summary class="cursor-pointer text-xs text-muted">Advanced</summary>
          <div class="mt-2 flex flex-col gap-3">
            <label class="flex min-w-0 flex-col gap-1 text-xs">CI poll seconds
              <input aria-label="CI poll seconds" type="number" min="1" value={ciPollText} oninput={(e) => (ciPollText = e.currentTarget.value)} placeholder="Default" class={field} />
            </label>
            <label class="flex min-w-0 flex-col gap-1 text-xs">CI wait seconds
              <input aria-label="CI wait seconds" type="number" min="1" value={ciWaitText} oninput={(e) => (ciWaitText = e.currentTarget.value)} placeholder="Default" class={field} />
            </label>
          </div>
        </details>
      </Card>

      <Card class="flex min-w-0 flex-col gap-3" data-testid="defaults-card">
        <h2 class="text-sm font-semibold">Session</h2>
        <p class="text-xs text-muted">New agents in this project start with these, unless the person choosing says otherwise.</p>
        <label class="flex min-w-0 flex-col gap-1 text-xs">Mode
          <select aria-label="Mode" bind:value={mode} class={field}>
            <option value="">Caller's choice</option>
            {#each MODES as m (m)}<option value={m}>{m === 'ask' ? 'Ask' : 'Act'}</option>{/each}
          </select>
        </label>
        <label class="flex min-w-0 flex-col gap-1 text-xs">Auto
          <select aria-label="Auto" bind:value={autoMode} class={field}>
            <option value="default">Caller's choice</option>
            <option value="on">Preauthorized</option>
            <option value="off">Explicitly off</option>
          </select>
        </label>
        <label class="flex min-w-0 flex-col gap-1 text-xs">Isolation
          <select aria-label="Isolation" bind:value={isolation} class={field}>
            <option value="default">Caller's choice</option>
            <option value="isolated">Isolated worktree</option>
            <option value="shared">Shared checkout</option>
          </select>
        </label>
        <label class="flex min-w-0 flex-col gap-1 text-xs">Ship target
          <select aria-label="Ship target" bind:value={shipTarget} class={field}>
            <option value="">Derived from the agent</option>
            {#each SHIP_TARGETS as t (t)}<option value={t}>{t}</option>{/each}
          </select>
        </label>
      </Card>

      <div class="flex flex-wrap items-center gap-3 lg:col-span-2">
        <Button onclick={saveDefaults} disabled={saving || !saved}>Save defaults</Button>
        {#if dirty}<span class="text-xs text-muted" data-testid="unsaved">Unsaved changes</span>{/if}
      </div>

      <Card class="flex min-w-0 flex-col gap-3" data-testid="intake-card">
        <h2 class="text-sm font-semibold">Intake</h2>
        <label class="flex min-w-0 flex-col gap-1 text-xs">Repo
          <select aria-label="Intake repo" bind:value={repoId} class={field}>
            <option value="">None</option>
            {#each repoChoices as r (r)}<option value={r}>{r}</option>{/each}
          </select>
        </label>
        <div class="flex min-w-0 flex-col gap-1 text-xs">
          <span>Labels</span>
          <div class="flex flex-wrap items-center gap-1.5">
            {#each labels as l (l)}
              <Tag tone="info">{l} <button type="button" class="cursor-pointer" aria-label="Remove label {l}" onclick={() => (labels = labels.filter((x) => x !== l))}>✕</button></Tag>
            {/each}
            <form class="flex gap-1" onsubmit={(e) => { e.preventDefault(); addLabel() }}>
              <input aria-label="New label" bind:value={newLabel} placeholder="label" disabled={!repoId} class="w-28 rounded border border-border bg-bg p-1 text-sm" />
              <Button type="submit" variant="ghost" class="min-h-8 px-2 py-1 text-xs" disabled={!repoId || !newLabel.trim()}>Add</Button>
            </form>
          </div>
          {#if !repoId}<span class="text-muted">Labels apply to a repo; pick one first.</span>{:else}<span class="text-muted">The watcher follows one label per repo; adding a label replaces the current one.</span>{/if}
        </div>
        <fieldset class="flex flex-col gap-1 text-xs">
          <legend class="mb-1">Allowed MCP clients</legend>
          {#each clients as c (c.id)}
            <label class="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={clientIds.includes(c.id)} onchange={(e) => toggleClient(c.id, e.currentTarget.checked)} />
              {c.name || c.id}
            </label>
          {:else}
            <span class="text-muted">No MCP clients are registered.</span>
          {/each}
        </fieldset>
        <div><Button onclick={saveIntake} disabled={saving || !saved}>Save intake</Button></div>
      </Card>

      <Card class="flex min-w-0 flex-col gap-3" data-testid="delegation-card">
        <h2 class="text-sm font-semibold">Planner delegation</h2>
        <p class="text-xs text-muted">
          Off by default. When on, the one client you designate may answer this project's planner questions
          on your behalf; it cannot approve or queue work from that, and the delegation is recorded as the
          planner's answer, not as your own.
        </p>
        <label class="flex items-center gap-2 text-sm">
          <input type="checkbox" bind:checked={delegation} />
          Allow one MCP client to answer planner questions
        </label>
        <label class="flex min-w-0 flex-col gap-1 text-xs">Designated MCP client
          <select aria-label="Designated MCP client" bind:value={delegateClient} disabled={!delegation} class={field}>
            <option value="">No client designated</option>
            {#each clientChoices as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
          </select>
        </label>
        {#if delegationIncomplete}<p class="text-xs text-attention">Choose the client that may answer before saving; delegation with nobody designated answers nothing.</p>{/if}
        <div><Button onclick={saveDelegation} disabled={saving || !saved || delegationIncomplete}>Save delegation</Button></div>
      </Card>

      <Card class="flex min-w-0 flex-col gap-3" data-testid="health-card">
        <div class="flex items-center justify-between">
          <h2 class="text-sm font-semibold">Health</h2>
          <Button variant="ghost" onclick={loadHealth} disabled={healthBusy}>Refresh</Button>
        </div>
        {#if healthError}<p class="text-sm text-err" role="alert">{healthError}</p>{/if}
        <ul class="flex flex-col gap-2">
          {#each checks as c (c.id)}
            <li class="flex items-start gap-2 text-sm" data-testid="health-check">
              <span class="mt-1.5 size-2 shrink-0 rounded-full {dotClass[c.dot]}" role="img" aria-label={c.dot}></span>
              <span class="min-w-0"><span class="font-medium">{c.label}</span> <span class="text-muted">{c.detail}</span></span>
            </li>
          {:else}
            {#if !healthError}<li class="text-sm text-muted">{healthBusy ? 'Checking…' : 'No health data.'}</li>{/if}
          {/each}
        </ul>
      </Card>

      <Card class="flex min-w-0 flex-col gap-3" data-testid="policy-card">
        <div class="flex items-center justify-between">
          <h2 class="text-sm font-semibold">Policy</h2>
          {#if designerHash}
            <a class="text-sm text-accent hover:underline" href={designerHash} onclick={(e) => { e.preventDefault(); onNavigate(designerHash) }}>Edit in designer</a>
          {/if}
        </div>
        {#if policy}
          <dl class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
            <dt class="text-muted">Mode</dt>
            <dd class="font-mono">{policy.mode || 'not set'}</dd>
            <dt class="text-muted">Allow</dt>
            <dd class="font-mono">{#if policy.allow?.length}{policy.allow.join(', ')}{:else}none{/if}</dd>
          </dl>
        {:else}
          <p class="text-sm text-muted">{policyNote}</p>
        {/if}
      </Card>
    </div>
  {:else if tab === 'automations'}
    <div class="grid gap-4 lg:grid-cols-2" data-testid="automations-tab">
      {#each [{ page: 'review-bot', title: 'Review bot', blurb: 'Reviews new pull requests and keeps the findings as a draft for you to post.', on: settings?.automations?.reviewBot?.enabled }, { page: 'ci-fixer', title: 'CI fixer', blurb: 'Reproduces a failing check and opens a fix, or gives up and says why.', on: settings?.automations?.ciFixer?.enabled }] as a (a.page)}
        <Card class="flex min-w-0 flex-col gap-2">
          <div class="flex items-center gap-2"><h2 class="text-sm font-semibold">{a.title}</h2><Tag tone={a.on ? 'ok' : 'neutral'}>{a.on ? 'on' : 'off'}</Tag></div>
          <p class="text-sm text-muted">{a.blurb}</p>
          <a class="text-sm text-accent hover:underline" href={formatAutomationRoute(root, a.page as AutomationPage)} onclick={(e) => { e.preventDefault(); onNavigate(formatAutomationRoute(root, a.page as AutomationPage)) }}>Open {a.title}</a>
        </Card>
      {/each}
    </div>
  {:else}
    <Card class="flex flex-col gap-3" data-testid="session-sheet">
      {#if agent && tele}
        <div class="flex items-center gap-2">
          <h2 class="text-sm font-semibold">{agent.name || agent.id}</h2>
          <Tag tone="neutral">{agent.status}</Tag>
          <span class="flex-1"></span>
          <a class="text-sm text-accent hover:underline" href="#chat/{agent.id}" onclick={(e) => { e.preventDefault(); onNavigate(`#chat/${agent.id}`) }}>Open session</a>
        </div>
        <section aria-label="Context">
          <h3 class="mb-1 text-xs tracking-wide text-muted uppercase">Context</h3>
          <div class="h-2 overflow-hidden rounded-full bg-line" role="progressbar" aria-valuenow={tele.contextPct} aria-valuemin="0" aria-valuemax="100" aria-label="Context used">
            <div class="h-full bg-accent" style="width: {Math.min(100, tele.contextPct)}%"></div>
          </div>
          <p class="mt-1 text-sm" data-testid="context-pct">{tele.contextPct}% used</p>
        </section>
        <section aria-label="Changed files">
          <h3 class="mb-1 text-xs tracking-wide text-muted uppercase">Changed files</h3>
          <p class="text-sm" data-testid="changed-files">{tele.changedFiles} file{tele.changedFiles === 1 ? '' : 's'}</p>
        </section>
        <section aria-label="Tool stats">
          <h3 class="mb-1 text-xs tracking-wide text-muted uppercase">Tool stats</h3>
          {#if tele.toolStats?.length}
            <ul class="text-sm">
              {#each tele.toolStats as t (t.name)}
                <li class="flex justify-between font-mono"><span>{t.name}</span><span>{t.calls}{#if t.errors} <span class="text-err">({t.errors} failed)</span>{/if}{#if t.slowestMs} <span class="text-muted">slowest {t.slowestMs} ms</span>{/if}</span></li>
              {/each}
            </ul>
          {:else}
            <p class="text-sm text-muted">Not reported yet.</p>
          {/if}
        </section>
        <section aria-label="Rules">
          <h3 class="mb-1 text-xs tracking-wide text-muted uppercase">Rules</h3>
          {#if tele.rules?.length}
            <ul class="flex flex-col gap-0.5 font-mono text-sm">{#each tele.rules as r (r)}<li>{r}</li>{/each}</ul>
          {:else}
            <p class="text-sm text-muted">Not reported yet.</p>
          {/if}
        </section>
      {:else}
        <p class="text-sm text-muted">No agent has run in this project yet.</p>
      {/if}
    </Card>
  {/if}
</div>
