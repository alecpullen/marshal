<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Tag from '../lib/ui/Tag.svelte'
  import WatchesTabs from '../lib/watches/WatchesTabs.svelte'
  import { deleteSchedule, errMessage, listRecipes, listSchedules, runSchedule, saveSchedule, type ProjectStatus, type Recipe, type Schedule } from '../lib/api'
  import { describe, nextRun, nextRuns, PRESETS, validCron } from '../lib/schedules/cron'
  import { missingRequired } from '../lib/recipes/recipes'
  import { shortName } from '../lib/utils'

  let { projects, onNavigate }: { projects: ProjectStatus[]; onNavigate: (hash: string) => void } = $props()

  let schedules = $state<Schedule[]>([])
  let recipes = $state<Recipe[]>([])
  let loaded = $state(false)
  let error = $state('')
  let creating = $state(false)
  let busy = $state(false)

  let name = $state('')
  let recipeName = $state('')
  let project = $state('')
  let inputs = $state<Record<string, string>>({})
  let cron = $state('0 9 * * *')

  async function load() {
    try {
      const [s, r] = await Promise.all([listSchedules(), listRecipes()])
      schedules = s
      recipes = r
      error = ''
    } catch (e) {
      error = errMessage(e)
    } finally {
      loaded = true
    }
  }
  onMount(load)

  const recipe = $derived(recipes.find((r) => r.name === recipeName))
  const upcoming = $derived(validCron(cron) ? nextRuns(cron, new Date(), 3) : [])
  const when = (d: Date) => d.toISOString().slice(0, 16).replace('T', ' ') + ' UTC'

  function start() {
    creating = true
    name = ''
    recipeName = recipes[0]?.name ?? ''
    project = projects.find((p) => p.available)?.root ?? ''
    inputs = {}
    cron = '0 9 * * *'
  }

  async function create() {
    error = ''
    if (!name.trim()) return void (error = 'Give the schedule a name.')
    if (!recipe) return void (error = 'Pick a recipe.')
    if (!project) return void (error = 'Pick a project.')
    if (!validCron(cron)) return void (error = 'The cron expression is not valid.')
    const missing = missingRequired(recipe, inputs)
    if (missing.length) return void (error = `Fill in: ${missing.join(', ')}`)
    busy = true
    try {
      await saveSchedule({ name: name.trim(), recipe: recipe.name, project, inputs, cron: cron.trim(), enabled: true })
      creating = false
      await load()
    } catch (e) {
      error = errMessage(e)
    } finally {
      busy = false
    }
  }

  async function toggle(s: Schedule) {
    error = ''
    try {
      const saved = await saveSchedule({ ...s, enabled: !s.enabled })
      schedules = schedules.map((x) => (x.id === s.id ? saved : x))
    } catch (e) {
      error = errMessage(e)
    }
  }

  async function runNow(s: Schedule) {
    error = ''
    try {
      const r = await runSchedule(s.id)
      onNavigate(`#chat/${encodeURIComponent(r.agentId)}`)
    } catch (e) {
      error = errMessage(e)
    }
  }

  async function remove(s: Schedule) {
    try {
      await deleteSchedule(s.id)
      schedules = schedules.filter((x) => x.id !== s.id)
    } catch (e) {
      error = errMessage(e)
    }
  }

  const next = (s: Schedule) => {
    if (!s.enabled || !validCron(s.cron)) return '—'
    const d = nextRun(s.cron, new Date())
    return d ? when(d) : '—'
  }
</script>

<div class="mx-auto flex max-w-5xl flex-col gap-4 p-6">
  <header class="flex items-center justify-between">
    <h1 class="text-lg font-semibold">Watches</h1>
    <Button onclick={start} disabled={creating || recipes.length === 0}>New schedule</Button>
  </header>
  <WatchesTabs tab="schedules" {onNavigate} />

  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}

  {#if creating}
    <form
      class="flex flex-col gap-3 rounded-lg border border-border bg-surface p-4 text-sm"
      aria-label="New schedule"
      onsubmit={(e) => {
        e.preventDefault()
        void create()
      }}
    >
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <label class="flex flex-col gap-1 text-xs">Name
          <input bind:value={name} class="rounded border border-border bg-bg p-1.5 text-sm" />
        </label>
        <label class="flex flex-col gap-1 text-xs">Recipe
          <select bind:value={recipeName} onchange={() => (inputs = {})} class="rounded border border-border bg-bg p-1.5 text-sm">
            {#each recipes as r (r.name)}<option value={r.name}>{r.title || r.name}</option>{/each}
          </select>
        </label>
        <label class="flex flex-col gap-1 text-xs">Project
          <select bind:value={project} class="rounded border border-border bg-bg p-1.5 text-sm">
            {#each projects.filter((p) => p.available) as p (p.root)}<option value={p.root}>{shortName(p.root)}</option>{/each}
          </select>
        </label>
      </div>
      {#each recipe?.inputs ?? [] as input (input.name)}
        <label class="flex flex-col gap-1 text-xs">{input.label || input.name}{input.required ? ' *' : ''}
          <input bind:value={inputs[input.name]} class="rounded border border-border bg-bg p-1.5 text-sm" />
        </label>
      {/each}

      <div class="flex flex-col gap-2">
        <div class="flex flex-wrap gap-1" role="group" aria-label="Presets">
          {#each PRESETS as p (p.cron)}
            <button type="button" class="rounded border px-2 py-1 text-xs hover:bg-hover {cron === p.cron ? 'border-accent text-accent' : 'border-border text-muted'}" onclick={() => (cron = p.cron)}>{p.label}</button>
          {/each}
        </div>
        <label class="flex flex-col gap-1 text-xs">Cron (UTC)
          <input bind:value={cron} aria-invalid={!validCron(cron)} class="w-64 rounded border border-border bg-bg p-1.5 font-mono text-sm" />
        </label>
        {#if validCron(cron)}
          <p class="text-xs text-muted" data-testid="cron-preview">{describe(cron)}. Next: {upcoming.map(when).join(' · ')}</p>
        {:else}
          <p class="text-xs text-err">Not a valid five-field cron expression.</p>
        {/if}
      </div>

      <div class="flex justify-end gap-2">
        <Button type="button" variant="ghost" onclick={() => (creating = false)}>Cancel</Button>
        <Button type="submit" disabled={busy}>Create</Button>
      </div>
    </form>
  {/if}

  {#if loaded && schedules.length === 0}
    <p class="text-sm text-muted">No schedules yet. A schedule runs a recipe on a timer.</p>
  {:else if schedules.length}
    <table class="w-full text-left text-sm">
      <thead class="text-xs text-muted">
        <tr><th class="py-1 pr-3">Name</th><th class="pr-3">Recipe</th><th class="pr-3">Project</th><th class="pr-3">When</th><th class="pr-3">Next run</th><th class="pr-3">Last result</th><th class="pr-3">On</th><th></th></tr>
      </thead>
      <tbody>
        {#each schedules as s (s.id)}
          <tr class="border-t border-border align-top" data-testid="schedule-row">
            <td class="py-2 pr-3 font-medium">{s.name}</td>
            <td class="pr-3 font-mono text-xs">{s.recipe}</td>
            <td class="pr-3 text-xs">{s.project ? shortName(s.project) : (s.repoId ?? '')}</td>
            <td class="pr-3 text-xs" title={s.cron}>{describe(s.cron)}</td>
            <td class="pr-3 text-xs text-muted">{next(s)}</td>
            <td class="pr-3 text-xs">
              {#if s.lastResult}<Tag tone={s.lastResult === 'running' ? 'accent' : s.lastResult.startsWith('gave up') || s.lastResult.startsWith('failed') ? 'err' : 'ok'}>{s.lastResult}</Tag>{:else}<span class="text-muted">never ran</span>{/if}
              {#if s.lastRunAgent}<a class="ml-1 text-accent underline" href="#chat/{encodeURIComponent(s.lastRunAgent)}">agent</a>{/if}
            </td>
            <td class="pr-3">
              <input type="checkbox" role="switch" aria-label="Enabled: {s.name}" checked={s.enabled} onchange={() => toggle(s)} />
            </td>
            <td class="text-right whitespace-nowrap">
              <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => runNow(s)}>Run now</Button>
              <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => remove(s)}>Delete</Button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>
