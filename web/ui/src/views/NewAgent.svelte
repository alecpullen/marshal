<script lang="ts">
  import { onMount } from 'svelte'
  import { getModels, listProjects, recentPrompts, spawnAgent, errMessage, type Issue, type ModelsConfig, type ProjectStatus } from '../lib/api'
  import IssuePicker from '../lib/IssuePicker.svelte'
  import Chip from '../lib/newagent/Chip.svelte'
  import Button from '../lib/ui/Button.svelte'
  import { DEFAULT_MODEL, MODES, canIsolate, defaults, loadRemembered, modelLabel, remember, routingFor, validModel, type Choice, type ModelChoice } from '../lib/newagent/newAgent'

  let { onDone }: { onDone: (id: string | null, warning?: string) => void } = $props()

  let projects = $state<ProjectStatus[]>([])
  let choice = $state<Choice>({ project: '', mode: 'edit', isolated: false, branch: '', baseRef: '' })
  let models = $state<ModelsConfig | null>(null)
  let model = $state<ModelChoice>(DEFAULT_MODEL)
  let prompt = $state('')
  let tab = $state<'issues' | 'recent'>('recent')
  let recent = $state<string[]>([])
  let error = $state('')
  let busy = $state(false)
  let promptEl = $state<HTMLTextAreaElement | null>(null)

  const selected = $derived(projects.find((p) => p.root === choice.project))
  const isolationOk = $derived(canIsolate(selected))
  const projectName = (root: string) => root.split('/').filter(Boolean).pop() || root

  onMount(async () => {
    promptEl?.focus()
    const remembered = loadRemembered()
    try {
      projects = await listProjects()
      choice = defaults(projects, remembered)
    } catch (e) {
      error = errMessage(e)
    }
    // The model list is optional: without it the chip stays on the default.
    try {
      models = await getModels()
      model = validModel(remembered.model, models)
    } catch {
      models = null
    }
  })

  // Recent prompts follow the project chip.
  $effect(() => {
    const p = choice.project
    if (!p) return
    recentPrompts(p).then((r) => (recent = r)).catch(() => (recent = []))
  })

  function pickProject(root: string) {
    const p = projects.find((x) => x.root === root)
    choice = { ...choice, project: root, isolated: canIsolate(p) ? choice.isolated : false }
  }

  function pickIssue(i: Issue) {
    prompt = i.body ? `${i.title}\n\n${i.body}` : i.title
    promptEl?.focus()
  }

  async function create() {
    if (!choice.project) {
      error = 'Pick a project first.'
      return
    }
    busy = true
    error = ''
    try {
      const r = await spawnAgent({
        project: choice.project,
        prompt: prompt.trim() || undefined,
        mode: choice.mode,
        isolated: choice.isolated || undefined,
        branch: choice.isolated && choice.branch.trim() ? choice.branch.trim() : undefined,
        baseRef: choice.isolated && choice.baseRef.trim() ? choice.baseRef.trim() : undefined,
        routing: routingFor(model, models?.roles ?? []),
      })
      remember(choice, model)
      onDone(r.agentId, r.warning)
    } catch (e) {
      // The prompt stays put so nothing typed is lost.
      error = errMessage(e)
    } finally {
      busy = false
    }
  }
</script>

<div class="mx-auto flex max-w-3xl flex-col gap-4 p-6">
  <header class="flex items-center justify-between">
    <h1 class="text-lg font-semibold">New agent</h1>
    <Button variant="ghost" onclick={() => onDone(null)}>Cancel</Button>
  </header>

  {#if error}<div class="rounded border border-danger bg-danger/10 p-3 text-sm" role="alert">{error}</div>{/if}

  <div class="flex flex-col gap-3 rounded-lg border border-border bg-surface p-3">
    <textarea
      bind:this={promptEl}
      bind:value={prompt}
      rows="7"
      placeholder="What should the agent do?"
      aria-label="Prompt"
      class="w-full resize-y rounded bg-bg p-3 text-sm"
      onkeydown={(e) => {
        if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
          e.preventDefault()
          void create()
        }
      }}
    ></textarea>

    <div class="flex flex-wrap items-center gap-2">
      <Chip label="Project" value={choice.project ? projectName(choice.project) : 'none'}>
        {#each projects as p (p.root)}
          <button type="button" class="rounded px-2 py-1 text-left hover:bg-hover disabled:text-dim" disabled={!p.available} aria-pressed={p.root === choice.project} onclick={() => pickProject(p.root)}>
            {p.root === choice.project ? '● ' : ''}{p.root}{p.available ? '' : ' (unavailable)'}
          </button>
        {/each}
      </Chip>

      <Chip label="Branch" value={choice.isolated ? choice.branch || 'auto' : 'n/a'}>
        <label class="flex flex-col gap-1 text-xs">Branch name
          <input bind:value={choice.branch} disabled={!choice.isolated} placeholder="auto" class="rounded border border-border bg-bg p-1.5 text-sm" />
        </label>
        <label class="flex flex-col gap-1 text-xs">Base ref
          <input bind:value={choice.baseRef} disabled={!choice.isolated} placeholder="HEAD" class="rounded border border-border bg-bg p-1.5 text-sm" />
        </label>
        {#if !choice.isolated}<div class="text-xs text-muted">Branch settings need isolation.</div>{/if}
      </Chip>

      <Chip label="Mode" value={choice.mode}>
        {#each MODES as m (m)}
          <label class="flex items-center gap-2 rounded px-2 py-1 hover:bg-hover">
            <input type="radio" name="mode" value={m} checked={choice.mode === m} onchange={() => (choice = { ...choice, mode: m })} /> {m}
          </label>
        {/each}
      </Chip>

      <Chip label="Isolation" value={choice.isolated ? 'worktree' : 'off'}>
        <label class="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={choice.isolated} disabled={!isolationOk} onchange={(e) => (choice = { ...choice, isolated: e.currentTarget.checked })} /> Isolate in a git worktree
        </label>
        <div class="text-xs text-muted">
          {#if isolationOk}The agent works on its own branch, so it cannot collide with other agents.{:else}Unavailable: {selected?.isolation}.{/if}
        </div>
      </Chip>

      <Chip label="Model" value={modelLabel(model)}>
        <button type="button" class="rounded px-2 py-1 text-left hover:bg-hover" aria-pressed={model.kind === 'default'} onclick={() => (model = DEFAULT_MODEL)}>
          {model.kind === 'default' ? '● ' : ''}Default profile
        </button>
        {#if models && Object.keys(models.profiles).length > 0}
          <div class="px-2 pt-1 text-xs text-muted">Profiles</div>
          {#each Object.keys(models.profiles).sort() as name (name)}
            <button type="button" class="rounded px-2 py-1 text-left hover:bg-hover" aria-pressed={model.kind === 'profile' && model.name === name} onclick={() => (model = { kind: 'profile', name })}>
              {model.kind === 'profile' && model.name === name ? '● ' : ''}{name}{name === models.defaultProfile ? ' (default)' : ''}
            </button>
          {/each}
        {/if}
        {#if models && Object.keys(models.presets).length > 0}
          <div class="px-2 pt-1 text-xs text-muted">Presets (all roles except the fast ones)</div>
          {#each Object.keys(models.presets).sort() as name (name)}
            <button type="button" class="rounded px-2 py-1 text-left hover:bg-hover" aria-pressed={model.kind === 'preset' && model.name === name} onclick={() => (model = { kind: 'preset', name })}>
              {model.kind === 'preset' && model.name === name ? '● ' : ''}{name}
            </button>
          {/each}
        {/if}
      </Chip>

      <Button class="ml-auto" disabled={busy || !choice.project} onclick={create}>Create agent <span class="text-xs opacity-70">⌘↵</span></Button>
    </div>
  </div>

  <div class="flex flex-col gap-3">
    <div class="flex gap-2 border-b border-border text-sm" role="tablist">
      <button type="button" role="tab" aria-selected={tab === 'recent'} class="px-3 py-1.5 {tab === 'recent' ? 'border-b-2 border-accent' : 'text-muted'}" onclick={() => (tab = 'recent')}>Recent prompts</button>
      <button type="button" role="tab" aria-selected={tab === 'issues'} class="px-3 py-1.5 {tab === 'issues' ? 'border-b-2 border-accent' : 'text-muted'}" onclick={() => (tab = 'issues')}>Issues</button>
    </div>
    {#if tab === 'recent'}
      {#if recent.length === 0}
        <p class="text-sm text-muted">No earlier prompts for this project.</p>
      {:else}
        <ul class="flex flex-col gap-1">
          {#each recent as r (r)}
            <li>
              <button type="button" class="w-full truncate rounded border border-border bg-surface px-3 py-2 text-left text-sm hover:bg-hover" onclick={() => { prompt = r; promptEl?.focus() }}>{r}</button>
            </li>
          {/each}
        </ul>
      {/if}
    {:else}
      <IssuePicker onPick={pickIssue} />
    {/if}
  </div>
</div>
