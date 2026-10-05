<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../ui/Card.svelte'
  import Button from '../ui/Button.svelte'
  import Tag from '../ui/Tag.svelte'
  import Modal from '../ui/Modal.svelte'
  import RecipeEditor from './RecipeEditor.svelte'
  import { copyRecipe, deleteRecipe, errMessage, listRecipes, runRecipe, type ProjectStatus, type Recipe } from '../api'
  import { limitsLabel, missingRequired } from '../recipes/recipes'
  import { shortName } from '../utils'

  let {
    projects,
    onToast,
    onNavigate,
  }: { projects: ProjectStatus[]; onToast: (t: string) => void; onNavigate: (hash: string) => void } = $props()

  let recipes = $state<Recipe[]>([])
  let loaded = $state(false)
  let error = $state('')
  let editing = $state<{ recipe: Recipe; isNew: boolean } | null>(null)
  let running = $state<Recipe | null>(null)
  let runProject = $state('')
  let runInputs = $state<Record<string, string>>({})
  let runError = $state('')
  let busy = $state(false)

  async function load() {
    try {
      recipes = await listRecipes()
      error = ''
    } catch (e) {
      error = errMessage(e)
    } finally {
      loaded = true
    }
  }
  onMount(load)

  const blank = (): Recipe => ({ name: '', title: '', kind: 'prompt', mode: 'edit', prompt: '', inputs: [], output: 'none' })

  function startRun(r: Recipe) {
    running = r
    runInputs = {}
    runError = ''
    runProject = projects.find((p) => p.available)?.root ?? ''
  }

  async function run() {
    if (!running) return
    const missing = missingRequired(running, runInputs)
    if (!runProject) {
      runError = 'Pick a project.'
      return
    }
    if (missing.length) {
      runError = `Fill in: ${missing.join(', ')}`
      return
    }
    busy = true
    runError = ''
    try {
      const r = await runRecipe(running.name, { project: runProject, inputs: runInputs })
      running = null
      onNavigate(`#chat/${encodeURIComponent(r.agentId)}`)
    } catch (e) {
      runError = errMessage(e)
    } finally {
      busy = false
    }
  }

  async function copy(r: Recipe) {
    const name = window.prompt(`Name for the copy of ${r.name}`, `${r.name}-copy`)
    if (!name) return
    try {
      const c = await copyRecipe(r.name, name)
      await load()
      editing = { recipe: c, isNew: false }
    } catch (e) {
      error = errMessage(e)
    }
  }

  async function remove(r: Recipe) {
    try {
      await deleteRecipe(r.name)
      recipes = recipes.filter((x) => x.name !== r.name)
      onToast(`Deleted ${r.name}`)
    } catch (e) {
      error = errMessage(e)
    }
  }
</script>

<div class="flex flex-col gap-4">
  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}

  {#if editing}
    <RecipeEditor
      recipe={editing.recipe}
      isNew={editing.isNew}
      onCancel={() => (editing = null)}
      onSaved={(r) => {
        editing = null
        onToast(`Saved ${r.name}`)
        void load()
      }}
    />
  {:else}
    <div><Button variant="ghost" onclick={() => (editing = { recipe: blank(), isNew: true })}>New recipe</Button></div>
    {#if loaded && recipes.length === 0}
      <p class="text-sm text-muted">No recipes yet.</p>
    {:else}
      <table class="w-full text-left text-sm">
        <thead class="text-xs text-muted">
          <tr><th class="py-1 pr-3">Recipe</th><th class="pr-3">Kind</th><th class="pr-3">Mode</th><th class="pr-3">Limits</th><th></th></tr>
        </thead>
        <tbody>
          {#each recipes as r (r.name)}
            <tr class="border-t border-border align-top" data-testid="recipe-row">
              <td class="py-2 pr-3">
                <div class="flex items-center gap-2">
                  <span class="font-medium">{r.title || r.name}</span>
                  {#if r.builtin}<Tag>built-in</Tag>{/if}
                </div>
                <div class="font-mono text-xs text-muted">{r.name}</div>
                {#if r.description}<div class="text-xs text-sub">{r.description}</div>{/if}
              </td>
              <td class="pr-3 font-mono text-xs">{r.kind}</td>
              <td class="pr-3 font-mono text-xs">{r.mode ?? ''}</td>
              <td class="pr-3 text-xs text-muted">{limitsLabel(r)}</td>
              <td class="text-right whitespace-nowrap">
                <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => startRun(r)}>Run</Button>
                {#if r.builtin}
                  <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => copy(r)}>Copy</Button>
                {:else}
                  <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => (editing = { recipe: r, isNew: false })}>Edit</Button>
                  <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => remove(r)}>Delete</Button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  {/if}
</div>

{#if running}
  <Modal title="Run {running.title || running.name}" description={running.description} onDismiss={() => void (running = null)}>
    {#if runError}<p class="text-sm text-err" role="alert">{runError}</p>{/if}
    <label class="flex flex-col gap-1 text-xs">Project
      <select bind:value={runProject} class="rounded border border-border bg-bg p-1.5 text-sm">
        {#each projects.filter((p) => p.available) as p (p.root)}<option value={p.root}>{shortName(p.root)}</option>{/each}
      </select>
    </label>
    {#each running.inputs ?? [] as input (input.name)}
      <label class="flex flex-col gap-1 text-xs">{input.label || input.name}{input.required ? ' *' : ''}
        <input bind:value={runInputs[input.name]} class="rounded border border-border bg-bg p-1.5 text-sm" />
      </label>
    {/each}
    {#snippet footer()}
      <Button variant="ghost" onclick={() => (running = null)}>Cancel</Button>
      <Button disabled={busy} onclick={run}>Run</Button>
    {/snippet}
  </Modal>
{/if}
