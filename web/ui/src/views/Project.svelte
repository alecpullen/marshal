<script lang="ts">
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Tag from '../lib/ui/Tag.svelte'
  import Tabs from '../lib/ui/Tabs.svelte'
  import {
    errMessage,
    getModels,
    getProjectHealth,
    getProjectSettings,
    getWorkspacePolicy,
    listClients,
    listRepos,
    listWorkspaces,
    putProjectSettings,
    type MCPClient,
    type ProjectHealth,
    type ProjectSettings,
    type RepoRow,
    type WorkspaceListItem,
  } from '../lib/api'
  import type { AgentRow, AgentTelemetry } from '../lib/fleet'
  import { MODES, SHIP_TARGETS, healthChecks, soft, isolatedFrom, isolationOf, latestAgent, parseWorkspaceRef, type Dot, type IsolationChoice } from '../lib/project/model'
  import { shortName } from '../lib/utils'

  let { root, agents, telemetry = {}, onNavigate }: { root: string; agents: AgentRow[]; telemetry?: Record<string, AgentTelemetry>; onNavigate: (hash: string) => void } = $props()

  let tab = $state<'overview' | 'session'>('overview')
  let error = $state('')
  let notice = $state('')

  // The last saved settings; each card saves its own fields over this.
  let saved = $state<ProjectSettings>({ intake: {} })
  let loaded = $state(false)
  let workspaces = $state<WorkspaceListItem[]>([])
  let profiles = $state<string[]>([])
  let repos = $state<RepoRow[]>([])
  let clients = $state<MCPClient[]>([])

  // Defaults card draft.
  let workspace = $state('')
  let profile = $state('')
  let mode = $state('')
  let isolation = $state<IsolationChoice>('default')
  let shipTarget = $state('')
  // Intake card draft.
  let repoId = $state('')
  let labels = $state<string[]>([])
  let newLabel = $state('')
  let clientIds = $state<string[]>([])
  let saving = $state(false)

  function adopt(s: ProjectSettings) {
    saved = s
    workspace = s.workspace ?? ''
    profile = s.routing?.profile ?? ''
    mode = s.mode ?? ''
    isolation = isolationOf(s)
    shipTarget = s.shipTarget ?? ''
    repoId = s.intake.repoId ?? ''
    labels = [...(s.intake.labels ?? [])]
    clientIds = [...(s.intake.clients ?? [])]
  }

  // The pickers are optional context: a bridge without one of them still lets the rest of the page work.

  async function load() {
    try {
      adopt(await getProjectSettings(root))
      error = ''
    } catch (e) {
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

  async function save(next: ProjectSettings, what: string) {
    saving = true
    error = ''
    notice = ''
    try {
      adopt(await putProjectSettings(root, next))
      notice = `${what} saved`
      void loadPolicy()
    } catch (e) {
      error = errMessage(e)
    } finally {
      saving = false
    }
  }

  const saveDefaults = () =>
    save(
      {
        ...saved,
        workspace: workspace || undefined,
        // A profile choice keeps the saved per-role overrides.
        routing: profile || saved.routing?.overrides ? { ...saved.routing, profile: profile || undefined } : undefined,
        mode: mode || undefined,
        isolated: isolatedFrom(isolation),
        shipTarget: (shipTarget || undefined) as ProjectSettings['shipTarget'],
      },
      'Defaults',
    )
  const saveIntake = () => save({ ...saved, intake: { repoId: repoId || undefined, labels: repoId ? labels : [], clients: clientIds } }, 'Intake')

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
    if (!saved.workspace) {
      policyNote = 'No default workspace is set.'
      return
    }
    const ref = parseWorkspaceRef(saved.workspace)
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
  const designerHash = $derived(saved.workspace && parseWorkspaceRef(saved.workspace).source === 'studio' ? `#workspaces/${encodeURIComponent(parseWorkspaceRef(saved.workspace).name)}/edit` : '')

  $effect(() => {
    void load().then(loadPolicy)
    void loadHealth()
  })

  // Session sheet: the telemetry of the project's most recent agent.
  const agent = $derived(latestAgent(agents, root))
  const tele = $derived(agent ? (telemetry[agent.id] ?? { contextPct: agent.contextPct ?? 0, changedFiles: agent.changedFiles ?? 0, at: 0 }) : undefined)

  const wsOptions = $derived(workspaces.map((w) => ({ value: w.source === 'repo' ? `repo:${w.name}` : w.name, label: w.source === 'repo' ? `${w.name} (repo)` : w.name })))
  // Keep a saved value visible even when the picker does not list it.
  const wsChoices = $derived(workspace && !wsOptions.some((o) => o.value === workspace) ? [...wsOptions, { value: workspace, label: workspace }] : wsOptions)
  const profileChoices = $derived(profile && !profiles.includes(profile) ? [...profiles, profile] : profiles)
  const repoChoices = $derived([...new Set([...repos.map((r) => r.id), ...(repoId ? [repoId] : [])])])
  const field = 'rounded border border-border bg-bg p-2 text-sm'
</script>

<div class="mx-auto flex max-w-5xl flex-col gap-4 p-6">
  <header>
    <p class="text-xs text-muted"><a class="text-accent hover:underline" href="#projects">Projects</a> /</p>
    <h1 class="text-lg font-semibold" title={root}>{shortName(root)}</h1>
    <p class="truncate font-mono text-xs text-muted">{root}</p>
  </header>

  <Tabs label="Project" tabs={[{ value: 'overview', label: 'Overview' }, { value: 'session', label: 'Session sheet' }]} value={tab} onchange={(t) => (tab = t as 'overview' | 'session')} />

  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}
  {#if notice}<Card class="text-sm" role="status">{notice}</Card>{/if}

  {#if tab === 'overview'}
    <div class="grid gap-4 lg:grid-cols-2">
      <Card class="flex flex-col gap-3" data-testid="defaults-card">
        <h2 class="text-sm font-semibold">Defaults</h2>
        <p class="text-xs text-muted">New agents in this project start with these, unless the person choosing says otherwise.</p>
        <label class="flex flex-col gap-1 text-xs">Workspace
          <select aria-label="Workspace" bind:value={workspace} class={field}>
            <option value="">None</option>
            {#each wsChoices as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
          </select>
        </label>
        <label class="flex flex-col gap-1 text-xs">Model
          <select aria-label="Model profile" bind:value={profile} class={field}>
            <option value="">Engine default</option>
            {#each profileChoices as p (p)}<option value={p}>{p}</option>{/each}
          </select>
        </label>
        <label class="flex flex-col gap-1 text-xs">Mode
          <select aria-label="Mode" bind:value={mode} class={field}>
            <option value="">Caller's choice</option>
            {#each MODES as m (m)}<option value={m}>{m}</option>{/each}
          </select>
        </label>
        <label class="flex flex-col gap-1 text-xs">Isolation
          <select aria-label="Isolation" bind:value={isolation} class={field}>
            <option value="default">Caller's choice</option>
            <option value="isolated">Isolated worktree</option>
            <option value="shared">Shared checkout</option>
          </select>
        </label>
        <label class="flex flex-col gap-1 text-xs">Ship target
          <select aria-label="Ship target" bind:value={shipTarget} class={field}>
            <option value="">Derived from the agent</option>
            {#each SHIP_TARGETS as t (t)}<option value={t}>{t}</option>{/each}
          </select>
        </label>
        <div><Button onclick={saveDefaults} disabled={saving || !loaded}>Save defaults</Button></div>
      </Card>

      <Card class="flex flex-col gap-3" data-testid="intake-card">
        <h2 class="text-sm font-semibold">Intake</h2>
        <label class="flex flex-col gap-1 text-xs">Repo
          <select aria-label="Intake repo" bind:value={repoId} class={field}>
            <option value="">None</option>
            {#each repoChoices as r (r)}<option value={r}>{r}</option>{/each}
          </select>
        </label>
        <div class="flex flex-col gap-1 text-xs">
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
        <div><Button onclick={saveIntake} disabled={saving || !loaded}>Save intake</Button></div>
      </Card>

      <Card class="flex flex-col gap-3" data-testid="health-card">
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

      <Card class="flex flex-col gap-3" data-testid="policy-card">
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
  {:else}
    <Card class="flex flex-col gap-3" data-testid="session-sheet">
      {#if agent && tele}
        <div class="flex items-center gap-2">
          <h2 class="text-sm font-semibold">{agent.name || agent.id}</h2>
          <Tag tone="neutral">{agent.status}</Tag>
          <span class="flex-1"></span>
          <a class="text-sm text-accent hover:underline" href="#chat/{agent.id}">Open session</a>
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
