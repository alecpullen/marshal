<script lang="ts">
  import { onMount } from 'svelte'
  import { listProjects, listRecipes, recentPrompts, runRecipe, spawnAgent, errMessage, type Issue, type ProjectStatus, type Recipe } from '../lib/api'
  import { MODES as MODE_LIST } from '../lib/newagent/newAgent'
  import { limitsLabel, missingRequired } from '../lib/recipes/recipes'
  import IssuePicker from '../lib/IssuePicker.svelte'
  import Chip from '../lib/newagent/Chip.svelte'
  import Button from '../lib/ui/Button.svelte'
  import { MODES, canIsolate, defaults, loadRemembered, loadWorkspace, remember, rememberWorkspace, type Choice } from '../lib/newagent/newAgent'
  import WorkspaceChip from '../lib/workspaces/WorkspaceChip.svelte'

  let { onDone }: { onDone: (id: string | null, warning?: string) => void } = $props()

  let projects = $state<ProjectStatus[]>([])
  let choice = $state<Choice>({ project: '', mode: 'edit', isolated: false, branch: '', baseRef: '' })
  let workspace = $state('')
  let prompt = $state('')
  let tab = $state<'issues' | 'recent' | 'recipes'>('recent')
  let recipes = $state<Recipe[]>([])
  let recipe = $state<Recipe | null>(null)
  let recipeInputs = $state<Record<string, string>>({})
  let recent = $state<string[]>([])
  let error = $state('')
  let busy = $state(false)
  let promptEl = $state<HTMLTextAreaElement | null>(null)

  const selected = $derived(projects.find((p) => p.root === choice.project))
  const isolationOk = $derived(canIsolate(selected))
  const projectName = (root: string) => root.split('/').filter(Boolean).pop() || root

  onMount(async () => {
    promptEl?.focus()
    try {
      projects = await listProjects()
      choice = defaults(projects, loadRemembered())
      workspace = loadWorkspace(choice.project)
      recipes = await listRecipes().catch(() => [])
    } catch (e) {
      error = errMessage(e)
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
    workspace = loadWorkspace(root)
  }

  function pickIssue(i: Issue) {
    prompt = i.body ? `${i.title}\n\n${i.body}` : i.title
    promptEl?.focus()
  }

  function pickRecipe(r: Recipe) {
    recipe = r
    recipeInputs = {}
    prompt = r.prompt
    if (r.mode && (MODE_LIST as readonly string[]).includes(r.mode)) choice = { ...choice, mode: r.mode as Choice['mode'] }
    if (r.workspace) workspace = r.workspace
  }

  function clearRecipe() {
    recipe = null
    recipeInputs = {}
    prompt = ''
  }

  async function create() {
    if (!choice.project) {
      error = 'Pick a project first.'
      return
    }
    if (recipe) {
      const missing = missingRequired(recipe, recipeInputs)
      if (missing.length) {
        error = `Fill in: ${missing.join(', ')}`
        return
      }
    }
    busy = true
    error = ''
    try {
      if (recipe) {
        // The recipe's limits only apply through its own run route.
        const r = await runRecipe(recipe.name, { project: choice.project, inputs: recipeInputs })
        remember(choice)
        onDone(r.agentId)
        return
      }
      const r = await spawnAgent({
        project: choice.project,
        prompt: prompt.trim() || undefined,
        mode: choice.mode,
        workspace: workspace || undefined,
        isolated: choice.isolated || undefined,
        branch: choice.isolated && choice.branch.trim() ? choice.branch.trim() : undefined,
        baseRef: choice.isolated && choice.baseRef.trim() ? choice.baseRef.trim() : undefined,
      })
      remember(choice)
      rememberWorkspace(choice.project, workspace)
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
    {#if recipe}
      <div class="flex flex-col gap-2 rounded border border-border bg-bg p-3 text-sm" data-testid="recipe-inputs">
        <div class="flex items-center gap-2">
          <span class="font-medium">{recipe.title || recipe.name}</span>
          <span class="text-xs text-muted">{limitsLabel(recipe)}</span>
          <button type="button" class="ml-auto text-xs text-muted hover:text-fg" onclick={clearRecipe}>Clear recipe</button>
        </div>
        {#each recipe.inputs ?? [] as input (input.name)}
          <label class="flex flex-col gap-1 text-xs">{input.label || input.name}{input.required ? ' *' : ''}
            <input bind:value={recipeInputs[input.name]} class="rounded border border-border bg-surface p-1.5 text-sm" />
          </label>
        {/each}
      </div>
    {/if}
    <textarea
      bind:this={promptEl}
      bind:value={prompt}
      rows="7"
      placeholder="What should the agent do?"
      aria-label="Prompt"
      readonly={!!recipe}
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

      {#if choice.project}
        {#key choice.project}
          <WorkspaceChip project={choice.project} value={workspace} onChange={(ref) => (workspace = ref)} />
        {/key}
      {/if}

      <Button class="ml-auto" disabled={busy || !choice.project} onclick={create}>Create agent <span class="text-xs opacity-70">⌘↵</span></Button>
    </div>
  </div>

  <div class="flex flex-col gap-3">
    <div class="flex gap-2 border-b border-border text-sm" role="tablist">
      <button type="button" role="tab" aria-selected={tab === 'recent'} class="px-3 py-1.5 {tab === 'recent' ? 'border-b-2 border-accent' : 'text-muted'}" onclick={() => (tab = 'recent')}>Recent prompts</button>
      <button type="button" role="tab" aria-selected={tab === 'issues'} class="px-3 py-1.5 {tab === 'issues' ? 'border-b-2 border-accent' : 'text-muted'}" onclick={() => (tab = 'issues')}>Issues</button>
      <button type="button" role="tab" aria-selected={tab === 'recipes'} class="px-3 py-1.5 {tab === 'recipes' ? 'border-b-2 border-accent' : 'text-muted'}" onclick={() => (tab = 'recipes')}>Recipes</button>
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
    {:else if tab === 'recipes'}
      {#if recipes.length === 0}
        <p class="text-sm text-muted">No recipes yet. Add one in the Library.</p>
      {:else}
        <ul class="flex flex-col gap-1">
          {#each recipes as r (r.name)}
            <li>
              <button type="button" class="w-full rounded border bg-surface px-3 py-2 text-left text-sm hover:bg-hover {recipe?.name === r.name ? 'border-accent' : 'border-border'}" aria-pressed={recipe?.name === r.name} onclick={() => pickRecipe(r)}>
                <span class="font-medium">{r.title || r.name}</span>
                {#if r.description}<span class="block text-xs text-muted">{r.description}</span>{/if}
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    {:else}
      <IssuePicker onPick={pickIssue} />
    {/if}
  </div>
</div>
