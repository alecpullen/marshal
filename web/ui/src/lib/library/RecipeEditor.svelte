<script lang="ts">
  import { onMount } from 'svelte'
  import Button from '../ui/Button.svelte'
  import { errMessage, listWorkspaces, saveRecipe, type Recipe, type RecipeInput, type WorkspaceListItem } from '../api'
  import { MODES } from '../newagent/newAgent'
  import { tokenRuns, undeclared } from '../recipes/recipes'

  let {
    recipe,
    isNew = false,
    onSaved,
    onCancel,
  }: { recipe: Recipe; isNew?: boolean; onSaved: (r: Recipe) => void; onCancel: () => void } = $props()

  const KINDS = ['prompt', 'sdd', 'swarm']
  const OUTPUTS = ['none', 'review-findings', 'ci-result']
  const NAME = /^[a-z0-9][a-z0-9-]*$/

  // svelte-ignore state_referenced_locally
  let draft = $state<Recipe>({ ...recipe, inputs: (recipe.inputs ?? []).map((i) => ({ ...i })), limits: { ...(recipe.limits ?? {}) } })
  let workspaces = $state<WorkspaceListItem[]>([])
  let error = $state('')
  let busy = $state(false)

  onMount(async () => {
    try {
      workspaces = (await listWorkspaces()).filter((w) => w.source === 'studio')
    } catch {
      workspaces = []
    }
  })

  const missing = $derived(undeclared(draft))
  const runs = $derived(tokenRuns(draft.prompt))

  function addInput() {
    draft.inputs = [...(draft.inputs ?? []), { name: '', label: '', required: false } as RecipeInput]
  }
  function removeInput(i: number) {
    draft.inputs = (draft.inputs ?? []).filter((_, j) => j !== i)
  }
  const num = (v: string) => (v.trim() === '' ? undefined : Number(v))

  async function save() {
    error = ''
    if (!NAME.test(draft.name)) {
      error = 'Name must be lowercase letters, digits and dashes.'
      return
    }
    if (!draft.prompt.trim()) {
      error = 'The prompt is empty.'
      return
    }
    if (missing.length) {
      error = `Declare every placeholder as an input: ${missing.map((m) => `{{${m}}}`).join(', ')}`
      return
    }
    if ((draft.inputs ?? []).some((i) => !i.name.trim())) {
      error = 'Every input needs a name.'
      return
    }
    const limits = draft.limits && (draft.limits.maxMinutes || draft.limits.maxUsd) ? draft.limits : undefined
    busy = true
    try {
      onSaved(await saveRecipe({ ...draft, limits, builtin: false }))
    } catch (e) {
      error = errMessage(e)
    } finally {
      busy = false
    }
  }
</script>

<form
  class="flex flex-col gap-3 rounded-lg border border-border bg-surface p-4 text-sm"
  aria-label="Recipe editor"
  onsubmit={(e) => {
    e.preventDefault()
    void save()
  }}
>
  {#if error}<p class="text-err" role="alert">{error}</p>{/if}

  <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
    <label class="flex flex-col gap-1 text-xs">Name
      <input bind:value={draft.name} disabled={!isNew} class="rounded border border-border bg-bg p-1.5 text-sm" />
    </label>
    <label class="flex flex-col gap-1 text-xs">Title
      <input bind:value={draft.title} class="rounded border border-border bg-bg p-1.5 text-sm" />
    </label>
  </div>
  <label class="flex flex-col gap-1 text-xs">Description
    <input bind:value={draft.description} class="rounded border border-border bg-bg p-1.5 text-sm" />
  </label>

  <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
    <label class="flex flex-col gap-1 text-xs">Kind
      <select bind:value={draft.kind} class="rounded border border-border bg-bg p-1.5 text-sm">
        {#each KINDS as k (k)}<option value={k}>{k}</option>{/each}
      </select>
    </label>
    <label class="flex flex-col gap-1 text-xs">Mode
      <select bind:value={draft.mode} class="rounded border border-border bg-bg p-1.5 text-sm">
        <option value="">default</option>
        {#each MODES as m (m)}<option value={m}>{m}</option>{/each}
      </select>
    </label>
    <label class="flex flex-col gap-1 text-xs">Workspace
      <select bind:value={draft.workspace} class="rounded border border-border bg-bg p-1.5 text-sm">
        <option value="">none</option>
        {#each workspaces as w (w.name)}<option value={w.name}>{w.name}</option>{/each}
      </select>
    </label>
    <label class="flex flex-col gap-1 text-xs">Output
      <select bind:value={draft.output} class="rounded border border-border bg-bg p-1.5 text-sm">
        {#each OUTPUTS as o (o)}<option value={o}>{o}</option>{/each}
      </select>
    </label>
  </div>

  {#if draft.routing && (draft.routing.profile || Object.keys(draft.routing.overrides ?? {}).length)}
    <p class="text-xs text-muted">Routing: <span class="font-mono">{draft.routing.profile ?? 'custom overrides'}</span> (kept as is)</p>
  {/if}

  <fieldset class="flex flex-col gap-2">
    <legend class="text-xs text-muted">Inputs</legend>
    {#each draft.inputs ?? [] as input, i (i)}
      <div class="flex flex-wrap items-center gap-2" data-testid="recipe-input">
        <input aria-label="Input name {i + 1}" placeholder="name" bind:value={input.name} class="w-32 rounded border border-border bg-bg p-1.5 font-mono text-xs" />
        <input aria-label="Input label {i + 1}" placeholder="label" bind:value={input.label} class="min-w-0 flex-1 rounded border border-border bg-bg p-1.5 text-xs" />
        <label class="flex items-center gap-1 text-xs"><input type="checkbox" bind:checked={input.required} /> required</label>
        <button type="button" class="text-xs text-muted hover:text-fg" onclick={() => removeInput(i)}>Remove</button>
      </div>
    {/each}
    <div><Button type="button" variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={addInput}>Add input</Button></div>
  </fieldset>

  <label class="flex flex-col gap-1 text-xs">Prompt
    <textarea bind:value={draft.prompt} rows="8" class="rounded border border-border bg-bg p-2 font-mono text-xs"></textarea>
  </label>
  <p class="font-mono text-xs break-words whitespace-pre-wrap text-muted" aria-label="Prompt tokens" data-testid="recipe-tokens">
    {#each runs as r, i (i)}{#if r.token}<mark class="rounded bg-accent/20 px-0.5 text-accent {missing.includes(r.text.replace(/[{}\s]/g, '')) ? 'bg-err/20 text-err' : ''}">{r.text}</mark>{:else}{r.text}{/if}{/each}
  </p>

  <div class="grid grid-cols-2 gap-3 sm:w-80">
    <label class="flex flex-col gap-1 text-xs">Max minutes
      <input type="number" min="0" value={draft.limits?.maxMinutes ?? ''} oninput={(e) => (draft.limits = { ...draft.limits, maxMinutes: num(e.currentTarget.value) })} class="rounded border border-border bg-bg p-1.5 text-sm" />
    </label>
    <label class="flex flex-col gap-1 text-xs">Max USD
      <input type="number" min="0" step="0.01" value={draft.limits?.maxUsd ?? ''} oninput={(e) => (draft.limits = { ...draft.limits, maxUsd: num(e.currentTarget.value) })} class="rounded border border-border bg-bg p-1.5 text-sm" />
    </label>
  </div>

  <div class="flex justify-end gap-2">
    <Button type="button" variant="ghost" onclick={onCancel}>Cancel</Button>
    <Button type="submit" disabled={busy}>Save</Button>
  </div>
</form>
