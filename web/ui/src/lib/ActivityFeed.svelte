<script lang="ts">
  import { onMount } from 'svelte'
  import { describeAuditEvent, type AuditEvent } from './audit'
  import { listAudit } from './api'

  /** Show only the most recent `limit` events; 0 shows all. `filter` keeps one event type. */
  let { limit = 0, filter = '' }: { limit?: number; filter?: string } = $props()

  let events = $state<AuditEvent[]>([])
  let loading = $state(true)
  const shown = $derived(filter ? events.filter((e) => e.event === filter) : events)

  async function refresh() {
    try {
      events = await listAudit()
    } catch {
      // ignore — the feed is best-effort. A 401 here has already cleared
      // the stored token via the api wrapper, so the next poll re-prompts
      // instead of hammering a dead session.
    } finally {
      loading = false
    }
  }

  onMount(() => {
    refresh()
    const interval = setInterval(refresh, 5000)
    return () => clearInterval(interval)
  })
</script>

<div class="flex flex-col gap-1">
  <h2 class="text-xs tracking-wide text-muted uppercase">Activity</h2>
  {#if loading}
    <p class="text-sm text-muted">Loading…</p>
  {:else if shown.length === 0}
    <p class="text-sm text-muted">No recent activity.</p>
  {:else}
    {#each (limit > 0 ? [...shown].reverse().slice(0, limit) : [...shown].reverse()) as e (e.ts + e.event + (e.agentId ?? ''))}
      <div class="truncate text-sm text-muted">
        <span class="text-fg">{new Date(e.ts).toLocaleTimeString()}</span>
        {describeAuditEvent(e)}
      </div>
    {/each}
  {/if}
</div>
