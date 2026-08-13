<script lang="ts">
  import { onMount } from 'svelte'
  import Card from './ui/Card.svelte'
  import Button from './ui/Button.svelte'
  import { getDiff, type DiffFile } from './api'
  import { diffTotals } from './diff'

  let { agentId }: { agentId: string } = $props()

  let files = $state<DiffFile[]>([])
  let open = $state<Record<string, string>>({})
  let error = $state<string | null>(null)

  onMount(load)

  async function load() {
    error = null
    try {
      files = (await getDiff(agentId)).files ?? []
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    }
  }

  async function toggle(path: string) {
    if (open[path] !== undefined) {
      const next = { ...open }
      delete next[path]
      open = next
      return
    }
    try {
      const res = await getDiff(agentId, path)
      open = { ...open, [path]: res.diff ?? '' }
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    }
  }

  const totals = $derived(diffTotals(files))
</script>

<Card class="flex flex-col gap-2">
  <div class="flex items-center justify-between">
    <span class="text-sm font-medium">
      {totals.files} file{totals.files === 1 ? '' : 's'}
      <span class="text-running">+{totals.added}</span>
      <span class="text-danger">-{totals.removed}</span>
    </span>
    <Button variant="ghost" onclick={load}>Reload</Button>
  </div>

  {#if error}
    <div class="rounded-md border border-danger bg-danger/10 p-2 text-sm">{error}</div>
  {/if}

  {#if files.length === 0}
    <p class="text-sm text-muted">No changes yet.</p>
  {/if}

  <ul class="flex flex-col gap-1">
    {#each files as f (f.path)}
      <li>
        <button
          class="flex w-full items-center justify-between gap-3 rounded px-2 py-2 text-left text-sm hover:bg-bg/40"
          onclick={() => toggle(f.path)}
        >
          <span class="min-w-0 truncate font-mono">{f.path}</span>
          <span class="shrink-0 text-xs">
            <span class="text-running">+{f.added}</span> <span class="text-danger">-{f.removed}</span>
          </span>
        </button>
        {#if open[f.path] !== undefined}
          <pre class="overflow-x-auto rounded bg-bg p-2 text-xs">{open[f.path] || '(no textual diff)'}</pre>
        {/if}
      </li>
    {/each}
  </ul>
</Card>
