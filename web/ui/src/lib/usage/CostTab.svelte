<script lang="ts">
  import { untrack } from 'svelte'
  import Card from '../ui/Card.svelte'
  import Button from '../ui/Button.svelte'
  import Segmented from '../ui/Segmented.svelte'
  import BarChart from './BarChart.svelte'
  import { getUsage, getBudgets, overrideBudget, errMessage, type BudgetStatus, type UsageBy, type UsageReport } from '../api'
  import type { AgentRow } from '../fleet'
  import { compactTokens, usd } from './format'

  let { agents = [], budgetTick = 0 }: { agents?: AgentRow[]; /** Moves on every `budget` fleet delta. */ budgetTick?: number } = $props()

  const BREAKDOWNS: { by: UsageBy; title: string }[] = [
    { by: 'project', title: 'By project' },
    { by: 'role', title: 'By role' },
    { by: 'model', title: 'By model' },
  ]

  let range = $state<'7d' | '30d'>('7d')
  let day = $state<UsageReport | null>(null)
  let breakdown = $state<Partial<Record<UsageBy, UsageReport>>>({})
  let budget = $state<BudgetStatus | null>(null)
  let error = $state('')

  async function load() {
    error = ''
    try {
      const [d, p, r, m] = await Promise.all([getUsage(range, 'day'), getUsage(range, 'project'), getUsage(range, 'role'), getUsage(range, 'model')])
      day = d
      breakdown = { project: p, role: r, model: m }
    } catch (e) {
      error = errMessage(e)
    }
  }
  async function loadBudget() {
    try {
      budget = await getBudgets()
    } catch {
      budget = null
    }
  }
  // Reload when the range changes.
  $effect(() => {
    void range
    untrack(() => void load())
  })
  // Loads on mount and again on each budget delta (a cap trip or a pause).
  $effect(() => {
    void budgetTick
    untrack(() => void loadBudget())
  })

  async function override(id: string) {
    try {
      await overrideBudget(id)
      await loadBudget()
    } catch (e) {
      error = errMessage(e)
    }
  }

  const t = $derived(day?.totals)
  const cap = $derived(budget?.loaded ? budget.budgets.dailyUsd : 0)
  const paused = $derived((budget?.agents ?? []).filter((a) => a.paused))
  const name = (id: string) => agents.find((a) => a.id === id)?.name || id
  const label = (by: UsageBy, key: string) => (by === 'project' ? key.split('/').filter(Boolean).pop() || key : key)
</script>

<div class="flex flex-col gap-5">
  <div class="flex items-center justify-between">
    <Segmented
      label="Range"
      value={range}
      onchange={(v) => (range = v as '7d' | '30d')}
      options={[
        { value: '7d', label: '7 days' },
        { value: '30d', label: '30 days' },
      ]}
    />
  </div>

  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}

  <div class="grid grid-cols-2 gap-3 sm:grid-cols-4" data-testid="tiles">
    <Card class="p-3"><div class="text-xs text-muted">Spend</div><div class="text-xl font-semibold" data-testid="tile-spend">{usd(t?.costUsd ?? 0)}</div></Card>
    <Card class="p-3"><div class="text-xs text-muted">Tokens</div><div class="text-xl font-semibold" data-testid="tile-tokens">{compactTokens((t?.promptTokens ?? 0) + (t?.completionTokens ?? 0))}</div></Card>
    <Card class="p-3"><div class="text-xs text-muted">Agent hours</div><div class="text-xl font-semibold" data-testid="tile-hours">{(t?.agentHours ?? 0).toFixed(1)}</div></Card>
    <Card class="p-3"><div class="text-xs text-muted">PRs shipped</div><div class="text-xl font-semibold" data-testid="tile-prs">{t?.prsShipped ?? 0}</div></Card>
  </div>

  <Card class="flex flex-col gap-2">
    <h2 class="text-sm font-semibold">Cost by day</h2>
    <BarChart bars={(day?.series ?? []).map((s) => ({ key: s.key, value: s.costUsd }))} {cap} format={usd} />
    {#if cap > 0}<p class="text-xs text-muted">Dashed line: daily cap {usd(cap)}.</p>{/if}
  </Card>

  {#if budget}
    <Card class="flex flex-col gap-2" data-testid="budget-state">
      <h2 class="text-sm font-semibold">Budget</h2>
      <p class="text-sm">
        Today {usd(budget.daily.spentUsd)}{cap > 0 ? ` of ${usd(cap)}` : ' (no daily cap)'}
        {#if budget.daily.blocked}<span class="text-err"> · new work is blocked until tomorrow or the cap is raised</span>{/if}
      </p>
      {#each paused as a (a.agentId)}
        <div class="flex items-center gap-3 text-sm" data-testid="paused-agent">
          <span class="font-mono">{name(a.agentId)}</span>
          <span class="text-muted">paused at its cap ({usd(a.spentUsd)})</span>
          <span class="flex-1"></span>
          <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => override(a.agentId)}>Override</Button>
        </div>
      {/each}
    </Card>
  {/if}

  <div class="grid gap-4 lg:grid-cols-3">
    {#each BREAKDOWNS as b (b.by)}
      <section class="flex flex-col gap-1" data-testid="breakdown-{b.by}">
        <h2 class="text-sm font-semibold">{b.title}</h2>
        <table class="w-full text-left text-sm">
          <tbody>
            {#each breakdown[b.by]?.series ?? [] as s (s.key)}
              <tr class="border-t border-border">
                <td class="truncate py-1 pr-2">{label(b.by, s.key)}</td>
                <td class="pr-2 text-right font-mono text-xs text-muted">{compactTokens(s.tokens)}</td>
                <td class="text-right font-mono text-xs">{usd(s.costUsd)}</td>
              </tr>
            {:else}
              <tr><td class="py-1 text-sm text-muted">No usage in this range.</td></tr>
            {/each}
          </tbody>
        </table>
      </section>
    {/each}
  </div>
</div>
