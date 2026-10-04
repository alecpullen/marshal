<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../ui/Card.svelte'
  import Button from '../ui/Button.svelte'
  import { createWatch, getModels, errMessage, type ModelsConfig, type OnTrip, type WatchSpec } from '../api'
  import type { AgentRow } from '../fleet'

  let { agents, onCreated, onCancel }: { agents: AgentRow[]; onCreated: () => void; onCancel: () => void } = $props()

  const MIN_INTERVAL = 2

  let owner = $state('studio')
  let name = $state('')
  let kind = $state<'command' | 'job' | 'file'>('command')
  let command = $state('')
  let jobId = $state('')
  let path = $state('')
  let condition = $state('change')
  let mode = $state('once')
  let intervalSec = $state(30)
  let notify = $state(true)
  let resume = $state(false)
  let reroute = $state(false)
  let role = $state('')
  let preset = $state('')
  let cfg = $state<ModelsConfig | null>(null)
  let error = $state('')
  let busy = $state(false)

  const isStudio = $derived(owner === 'studio')
  const roles = $derived(cfg?.roles ?? [])
  const presets = $derived(Object.keys(cfg?.presets ?? {}).sort())

  onMount(async () => {
    try {
      cfg = await getModels()
    } catch {
      cfg = null
    }
  })

  const detail = $derived(kind === 'command' ? command.trim() : kind === 'job' ? jobId.trim() : path.trim())
  const rerouteOk = $derived(!isStudio || !reroute || (!!role && !!preset))
  const valid = $derived(!!name.trim() && !!detail && intervalSec >= MIN_INTERVAL && rerouteOk)

  async function submit() {
    if (!valid) return
    busy = true
    error = ''
    const spec: WatchSpec = {
      name: name.trim(),
      kind,
      ...(kind === 'command' ? { command: command.trim() } : kind === 'job' ? { jobId: jobId.trim() } : { path: path.trim() }),
      condition: condition.trim() || 'change',
      mode,
      intervalMs: Math.round(intervalSec * 1000),
      notify,
      resume,
    }
    // Reroute is held by the bridge and only Studio watches may carry it.
    const onTrip: OnTrip | undefined = isStudio && reroute ? { reroute: { role, preset } } : undefined
    try {
      await createWatch({ ...(isStudio ? {} : { agentId: owner }), spec, ...(onTrip ? { onTrip } : {}) })
      onCreated()
    } catch (e) {
      // Limit errors from the agent arrive as the route's reason.
      error = errMessage(e)
    } finally {
      busy = false
    }
  }
</script>

<Card class="flex flex-col gap-3" data-testid="watch-form">
  <h2 class="text-sm font-semibold">New watch</h2>
  {#if error}<p class="text-sm text-err" role="alert">{error}</p>{/if}

  <div class="grid gap-3 sm:grid-cols-2">
    <label class="flex flex-col gap-1 text-xs">Owner
      <select aria-label="Owner" class="rounded border border-border bg-bg p-2 text-sm" bind:value={owner}>
        <option value="studio">Studio</option>
        {#each agents as a (a.id)}<option value={a.id}>{a.name || a.id}</option>{/each}
      </select>
    </label>
    <label class="flex flex-col gap-1 text-xs">Name
      <input aria-label="Name" class="rounded border border-border bg-bg p-2 text-sm" bind:value={name} />
    </label>
    <label class="flex flex-col gap-1 text-xs">Kind
      <select aria-label="Kind" class="rounded border border-border bg-bg p-2 text-sm" bind:value={kind}>
        <option value="command">command</option>
        <option value="job">job</option>
        <option value="file">file</option>
      </select>
    </label>
    {#if kind === 'command'}
      <label class="flex flex-col gap-1 text-xs">Command
        <input aria-label="Command" class="rounded border border-border bg-bg p-2 font-mono text-sm" bind:value={command} />
      </label>
    {:else if kind === 'job'}
      <label class="flex flex-col gap-1 text-xs">Job ID
        <input aria-label="Job ID" class="rounded border border-border bg-bg p-2 font-mono text-sm" bind:value={jobId} />
      </label>
    {:else}
      <label class="flex flex-col gap-1 text-xs">Path or glob
        <input aria-label="Path" class="rounded border border-border bg-bg p-2 font-mono text-sm" bind:value={path} />
      </label>
    {/if}
    <label class="flex flex-col gap-1 text-xs">Condition
      <input aria-label="Condition" class="rounded border border-border bg-bg p-2 font-mono text-sm" bind:value={condition} placeholder="change, exit_code 0, regex …, json a.b > 5" />
    </label>
    <label class="flex flex-col gap-1 text-xs">Mode
      <select aria-label="Mode" class="rounded border border-border bg-bg p-2 text-sm" bind:value={mode}>
        <option value="once">once</option>
        <option value="repeat">repeat</option>
      </select>
    </label>
    <label class="flex flex-col gap-1 text-xs">Interval (seconds, minimum {MIN_INTERVAL})
      <input aria-label="Interval" type="number" min={MIN_INTERVAL} class="rounded border border-border bg-bg p-2 text-sm" bind:value={intervalSec} />
    </label>
  </div>

  <div class="flex flex-wrap gap-4 text-sm">
    <label class="flex items-center gap-2"><input type="checkbox" bind:checked={notify} /> Notify</label>
    <label class="flex items-center gap-2"><input type="checkbox" bind:checked={resume} /> Resume the agent</label>
  </div>

  {#if isStudio}
    <fieldset class="flex flex-col gap-2 rounded border border-border p-3" data-testid="reroute-fields">
      <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={reroute} /> Reroute a role when this trips</label>
      {#if reroute}
        <div class="grid gap-3 sm:grid-cols-2">
          <label class="flex flex-col gap-1 text-xs">Role
            <select aria-label="Role" class="rounded border border-border bg-bg p-2 text-sm" bind:value={role}>
              <option value="">choose a role</option>
              {#each roles as r (r)}<option value={r}>{r}</option>{/each}
            </select>
          </label>
          <label class="flex flex-col gap-1 text-xs">Preset
            <select aria-label="Preset" class="rounded border border-border bg-bg p-2 text-sm" bind:value={preset}>
              <option value="">choose a preset</option>
              {#each presets as p (p)}<option value={p}>{p}</option>{/each}
            </select>
          </label>
        </div>
        <p class="text-xs text-muted">Changes the active profile. Applies to agents started after it trips; the Home page offers Undo.</p>
      {/if}
    </fieldset>
  {/if}

  <div class="flex gap-2">
    <Button disabled={busy || !valid} onclick={submit}>Start watch</Button>
    <Button variant="ghost" onclick={onCancel}>Cancel</Button>
  </div>
</Card>
