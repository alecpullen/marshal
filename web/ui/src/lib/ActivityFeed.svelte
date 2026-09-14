<script lang="ts">
  import { onMount } from 'svelte'
  import { describeAuditEvent, type AuditEvent } from './audit'
  import { listAudit } from './api'

  let events = $state<AuditEvent[]>([])
  let loading = $state(true)

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
  {:else if events.length === 0}
    <p class="text-sm text-muted">No recent activity.</p>
  {:else}
    {#each [...events].reverse() as e (e.ts + e.event + (e.agentId ?? ''))}
      <div class="truncate text-sm text-muted">
        <span class="text-fg">{new Date(e.ts).toLocaleTimeString()}</span>
        {describeAuditEvent(e)}
      </div>
    {/each}
  {/if}
</div>
