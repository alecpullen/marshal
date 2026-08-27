<script lang="ts">
  import Card from './ui/Card.svelte'
  import Button from './ui/Button.svelte'
  import Badge from './ui/Badge.svelte'
  import { pendingSummary } from './pending'
  import { approvePending, denyPending, type PendingSubmission } from './api'

  let { pending, onResolved }: { pending: PendingSubmission[]; onResolved: () => void } = $props()

  let expanded = $state<Set<string>>(new Set())
  let error = $state<string | null>(null)

  function toggle(id: string) {
    const next = new Set(expanded)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    expanded = next
  }

  async function approve(id: string) {
    error = null
    try {
      await approvePending(id)
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    } finally {
      onResolved()
    }
  }

  async function deny(id: string) {
    error = null
    try {
      await denyPending(id)
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    } finally {
      onResolved()
    }
  }
</script>

{#if error}
  <Card class="border-danger">
    <div class="flex items-center justify-between gap-3">
      <span class="text-sm text-danger">{error}</span>
      <Button variant="ghost" onclick={() => (error = null)}>Dismiss</Button>
    </div>
  </Card>
{/if}

{#if pending.length > 0}
  <Card class="border-attention">
    <div class="mb-3 text-sm font-medium text-attention">
      {pending.length} submission{pending.length === 1 ? '' : 's'} awaiting confirmation
    </div>
    <ul class="flex flex-col gap-3">
      {#each pending as p (p.id)}
        <li class="flex flex-col gap-2 border-t border-border pt-3 first:border-0 first:pt-0">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0 flex-1">
              <div class="truncate text-sm font-medium">{p.title}</div>
              <div class="mt-1 flex flex-wrap gap-2 text-xs text-muted">
                <Badge tone="muted">{p.origin}</Badge>
                {#if p.clientId}
                  <Badge tone="muted">{p.clientId}</Badge>
                {/if}
                <Badge tone="muted">{p.repoId}</Badge>
                <span>{pendingSummary(p)}</span>
              </div>
            </div>
            <div class="flex shrink-0 gap-2">
              <Button onclick={() => approve(p.id)}>Approve</Button>
              <Button variant="danger" onclick={() => deny(p.id)}>Deny</Button>
            </div>
          </div>
          {#if p.plan}
            <button
              class="text-left text-xs text-accent hover:underline"
              onclick={() => toggle(p.id)}
            >
              {expanded.has(p.id) ? 'Hide plan' : 'Read plan'}
            </button>
            {#if expanded.has(p.id)}
              <pre class="max-h-80 overflow-auto rounded-md border border-border bg-bg p-3 text-xs text-muted whitespace-pre-wrap">{p.plan}</pre>
            {/if}
          {/if}
        </li>
      {/each}
    </ul>
  </Card>
{/if}
