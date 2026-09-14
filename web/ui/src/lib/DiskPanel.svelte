<script lang="ts">
  import Card from './ui/Card.svelte'
  import Button from './ui/Button.svelte'
  import { getDiskUsage, pruneDisk, APIError, errMessage, type DiskStatus, type PruneResult } from './api'

  let disk = $state<DiskStatus | null>(null)
  let loading = $state(false)
  let error = $state<string | null>(null)
  /* A 503 from the bridge means no fleet is running: disk tracking is
     simply absent, so it renders as a muted note rather than an error. */
  let fleetMissing = $state(false)
  /* The two-step prune is component state: the first click only arms,
     and Cancel disarms without a server round-trip. */
  let confirming = $state(false)
  let pruning = $state(false)
  let result = $state<PruneResult | null>(null)

  const units = ['B', 'KB', 'MB', 'GB']

  function formatBytes(n: number): string {
    if (!Number.isFinite(n)) return '—'
    if (n < 1024) return `${Math.round(n)} B`
    let v = n
    let u = 0
    do {
      v /= 1024
      u++
    } while (v >= 1024 && u < units.length - 1)
    return `${v.toFixed(1)} ${units[u]}`
  }

  /* measuredAt arrives as RFC3339, but the panel never trusts it: a
     non-parseable string renders raw, like SessionsPanel's updated. */
  function formatMeasured(raw: string): string {
    const d = new Date(raw)
    if (isNaN(d.getTime())) return raw
    return d.toLocaleString()
  }

  const budgetBytes = $derived(disk && disk.budgetMB > 0 ? disk.budgetMB * 1024 * 1024 : 0)
  const pct = $derived(disk && budgetBytes > 0 ? Math.min(100, (disk.total / budgetBytes) * 100) : 0)

  /* The bar tone follows the share of the budget: running under 75%,
     attention from 75% to 99%, danger at 100% and above. */
  const barTone = $derived(budgetBytes > 0 ? (pct >= 100 ? 'bg-danger' : pct >= 75 ? 'bg-attention' : 'bg-running') : '')

  async function refresh() {
    loading = true
    error = null
    try {
      disk = await getDiskUsage()
      fleetMissing = false
    } catch (e) {
      if (e instanceof APIError && e.status === 503) {
        fleetMissing = true
      } else {
        error = errMessage(e)
      }
    } finally {
      loading = false
    }
  }

  async function prune() {
    error = null
    confirming = false
    pruning = true
    try {
      result = await pruneDisk()
      // Refetch so the bar reflects the post-prune total.
      await refresh()
    } catch (e) {
      error = errMessage(e)
    } finally {
      pruning = false
    }
  }

  function armPrune() {
    confirming = true
    // A stale result would sit next to a freshly armed confirmation.
    result = null
  }

  // Load on mount
  $effect(() => {
    refresh()
  })
</script>

<Card>
  <div class="mb-4 flex items-center justify-between">
    <h2 class="text-sm font-semibold">Disk</h2>
    <Button variant="ghost" onclick={refresh} disabled={loading}>Refresh</Button>
  </div>

  {#if error}
    <div class="mb-3 rounded-md border border-danger bg-danger/10 p-3 text-sm text-danger">{error}</div>
  {/if}

  {#if fleetMissing}
    <p class="text-sm text-muted">Disk tracking requires fleet mode.</p>
  {:else if disk}
    {#if result}
      <div class="mb-3 rounded-md border border-running bg-running/10 p-3" data-testid="prune-result">
        <p class="text-sm font-medium text-running">Reclaimed {formatBytes(result.reclaimed)}</p>
        {#if result.warning}
          <p class="mt-1 text-xs text-attention">{result.warning}</p>
        {/if}
      </div>
    {/if}

    {#if budgetBytes > 0}
      <div class="mb-3">
        <div class="mb-1 flex items-center justify-between text-xs text-muted">
          <span><span class="tabular-nums">{formatBytes(disk.total)}</span> of {formatBytes(budgetBytes)}</span>
          <span class="tabular-nums">{pct.toFixed(0)}%</span>
        </div>
        <div class="h-2 overflow-hidden rounded-full bg-border" data-testid="disk-bar">
          <div class="h-2 rounded-full {barTone}" style="width: {pct}%"></div>
        </div>
      </div>
    {:else}
      <p class="mb-2 text-sm text-muted">No budget set.</p>
    {/if}

    <div class="mb-3 flex flex-wrap gap-3 text-xs text-muted">
      {#if budgetBytes === 0}
        <span>total <span class="tabular-nums">{formatBytes(disk.total)}</span></span>
      {/if}
      <span>repos <span class="tabular-nums">{formatBytes(disk.repos)}</span></span>
      <span>work <span class="tabular-nums">{formatBytes(disk.work)}</span></span>
    </div>

    {#if disk.measuredAt}
      <p class="mb-4 text-xs text-muted">measured {formatMeasured(disk.measuredAt)}</p>
    {/if}

    <div class="flex items-center gap-2 border-t border-border pt-3">
      {#if confirming}
        <Button variant="danger" onclick={prune} disabled={pruning}>Confirm prune</Button>
        <Button variant="ghost" onclick={() => (confirming = false)}>Cancel</Button>
      {:else}
        <Button variant="ghost" onclick={armPrune} disabled={loading || pruning}>Prune</Button>
      {/if}
    </div>
  {/if}
</Card>
