<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../../lib/ui/Card.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Tag from '../../lib/ui/Tag.svelte'
  import Segmented from '../../lib/ui/Segmented.svelte'
  import {
    getModels,
    getBudgets,
    setPresets,
    setRouting,
    setBudgets,
    errMessage,
    type Budgets,
    type BudgetStatus,
    type ModelsConfig,
    type PresetWire,
  } from '../../lib/api'
  import { matrix, setCell } from '../../lib/models/routingMatrix'

  let { onToast }: { onToast: (t: string) => void } = $props()

  const APPLIES = 'Applies to agents started from now'

  let cfg = $state<ModelsConfig | null>(null)
  let status = $state<BudgetStatus | null>(null)
  let error = $state('')
  let profile = $state('')
  let drafts = $state<Record<string, PresetWire>>({})
  let budgets = $state<Budgets>({ dailyUsd: 0, perAgentUsd: 0, onDailyCap: 'warn', onAgentCap: 'warn' })
  let busy = $state(false)

  async function load() {
    try {
      const [c, b] = await Promise.all([getModels(), getBudgets().catch(() => null)])
      cfg = c
      status = b
      drafts = Object.fromEntries(Object.entries(c.presets).map(([k, v]) => [k, { ...v }]))
      budgets = { ...(b?.budgets ?? c.budgets) }
      if (!profile || !c.profiles[profile]) profile = c.defaultProfile || Object.keys(c.profiles).sort()[0] || ''
      error = ''
    } catch (e) {
      error = errMessage(e)
    }
  }
  onMount(() => void load())

  const presetNames = $derived(cfg ? Object.keys(cfg.presets).sort() : [])
  const m = $derived(cfg ? matrix(cfg) : { profiles: [], rows: [] })
  const column = $derived(m.profiles.indexOf(profile))
  const dirty = (name: string) => !!cfg && JSON.stringify(drafts[name]) !== JSON.stringify(cfg.presets[name])

  async function run(fn: () => Promise<void>, ok: string) {
    busy = true
    error = ''
    try {
      await fn()
      onToast(`${ok}. ${APPLIES}`)
      await load()
    } catch (e) {
      error = errMessage(e)
    } finally {
      busy = false
    }
  }

  const savePreset = (name: string) => run(() => setPresets({ [name]: drafts[name] }), `Saved preset ${name}`)
  const changeCell = (role: string, preset: string) =>
    cfg && run(() => setRouting({ profiles: setCell(cfg!, profile, role, preset) }), `Updated ${role} in ${profile}`)
  const makeDefault = () => run(() => setRouting({ defaultProfile: profile }), `${profile} is now the default profile`)
  const saveBudgets = () => run(() => setBudgets(budgets), 'Saved budgets')

  const usd = (n: number) => `$${n.toFixed(2)}`
</script>

<div class="flex flex-col gap-6">
  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}

  <section class="flex flex-col gap-2">
    <h2 class="text-sm font-semibold">Presets</h2>
    <div class="overflow-x-auto">
      <table class="w-full text-left text-sm">
        <thead class="text-xs text-muted">
          <tr><th class="py-1 pr-3">Name</th><th class="pr-3">Provider</th><th class="pr-3">Model</th><th class="pr-3">Context</th><th class="pr-3">Local only</th><th></th></tr>
        </thead>
        <tbody>
          {#each presetNames as name (name)}
            {@const d = drafts[name]}
            {#if d}
              <tr class="border-t border-border" data-testid="preset-row">
                <td class="py-1.5 pr-3 font-mono text-xs">{name}</td>
                <td class="pr-3">
                  <select aria-label="Provider for {name}" class="rounded border border-border bg-bg px-1.5 py-1 text-xs" bind:value={d.provider}>
                    {#each Object.keys(cfg!.providers).sort() as p (p)}<option value={p}>{p}</option>{/each}
                    {#if !cfg!.providers[d.provider]}<option value={d.provider}>{d.provider}</option>{/if}
                  </select>
                </td>
                <td class="pr-3"><input aria-label="Model for {name}" class="w-44 rounded border border-border bg-bg px-1.5 py-1 text-xs" bind:value={d.model} /></td>
                <td class="pr-3"><input aria-label="Context window for {name}" type="number" min="0" class="w-24 rounded border border-border bg-bg px-1.5 py-1 text-xs" value={d.contextWindow ?? 0} oninput={(e) => (d.contextWindow = Number(e.currentTarget.value) || undefined)} /></td>
                <td class="pr-3"><input aria-label="Local only for {name}" type="checkbox" checked={!!d.localOnly} onchange={(e) => (d.localOnly = e.currentTarget.checked)} /></td>
                <td class="text-right"><Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" disabled={busy || !dirty(name)} onclick={() => savePreset(name)}>Save</Button></td>
              </tr>
            {/if}
          {:else}
            <tr><td colspan="6" class="py-2 text-sm text-muted">{cfg ? 'No presets yet.' : 'Loading…'}</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>

  <section class="flex flex-col gap-2">
    <div class="flex flex-wrap items-center gap-3">
      <h2 class="text-sm font-semibold">Routing</h2>
      {#if m.profiles.length > 0}
        <Segmented label="Profile" value={profile} onchange={(v) => (profile = v)} options={m.profiles.map((p) => ({ value: p, label: p }))} />
        {#if cfg?.defaultProfile === profile}
          <Tag tone="accent">default</Tag>
        {:else}
          <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" disabled={busy} onclick={makeDefault}>Set default</Button>
        {/if}
      {/if}
    </div>
    {#if m.rows.length === 0}
      <p class="text-sm text-muted">{cfg ? 'No routing profiles are configured.' : 'Loading…'}</p>
    {:else}
      <table class="w-full max-w-xl text-left text-sm">
        <thead class="text-xs text-muted"><tr><th class="py-1 pr-3">Role</th><th>{profile}</th></tr></thead>
        <tbody>
          {#each m.rows as row (row.role)}
            {@const cell = row.cells[column]}
            <tr class="border-t border-border" data-testid="routing-row">
              <td class="py-1.5 pr-3 font-mono text-xs">{row.role}</td>
              <td>
                <select
                  aria-label="{row.role} preset"
                  class="rounded border border-border bg-bg px-1.5 py-1 text-xs"
                  value={cell?.preset ?? (cell?.customAgent ? `@${cell.customAgent}` : '')}
                  disabled={busy}
                  onchange={(e) => {
                    const v = e.currentTarget.value
                    if (!v.startsWith('@')) void changeCell(row.role, v)
                  }}
                >
                  <option value="">unbound</option>
                  {#if cell?.customAgent}<option value={`@${cell.customAgent}`}>agent: {cell.customAgent}</option>{/if}
                  {#each presetNames as p (p)}<option value={p}>{p}</option>{/each}
                </select>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </section>

  <section class="flex flex-col gap-3">
    <h2 class="text-sm font-semibold">Budgets</h2>
    {#if status}
      <p class="text-sm text-muted" data-testid="budget-spend">
        Spent today {usd(status.spentTodayUsd)}{budgets.dailyUsd > 0 ? ` of ${usd(budgets.dailyUsd)}` : ' (no daily cap)'}
        {#if status.blocked}<Tag tone="err">blocked</Tag>{/if}
      </p>
    {/if}
    <div class="grid max-w-xl gap-3 sm:grid-cols-2">
      <label class="flex flex-col gap-1 text-xs">Daily cap (USD, 0 = none)
        <input aria-label="Daily cap" type="number" min="0" step="0.01" class="rounded border border-border bg-bg p-2 text-sm" bind:value={budgets.dailyUsd} />
      </label>
      <label class="flex flex-col gap-1 text-xs">When the daily cap is reached
        <select aria-label="Daily cap action" class="rounded border border-border bg-bg p-2 text-sm" bind:value={budgets.onDailyCap}>
          <option value="warn">warn</option>
          <option value="block">block new work</option>
        </select>
      </label>
      <label class="flex flex-col gap-1 text-xs">Per-agent cap (USD, 0 = none)
        <input aria-label="Per-agent cap" type="number" min="0" step="0.01" class="rounded border border-border bg-bg p-2 text-sm" bind:value={budgets.perAgentUsd} />
      </label>
      <label class="flex flex-col gap-1 text-xs">When an agent reaches its cap
        <select aria-label="Agent cap action" class="rounded border border-border bg-bg p-2 text-sm" bind:value={budgets.onAgentCap}>
          <option value="warn">warn</option>
          <option value="pause">pause the agent</option>
        </select>
      </label>
    </div>
    <div><Button disabled={busy} onclick={saveBudgets}>Save budgets</Button></div>
  </section>
</div>
