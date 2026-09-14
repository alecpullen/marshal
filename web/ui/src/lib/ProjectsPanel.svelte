<script lang="ts">
  import Card from './ui/Card.svelte'
  import Button from './ui/Button.svelte'
  import Badge from './ui/Badge.svelte'
  import { listProjects, addProject, removeProject, type ProjectStatus } from './api'

  let projects = $state<ProjectStatus[]>([])
  let loading = $state(false)
  let error = $state<string | null>(null)
  let newRoot = $state('')
  /* The two-step remove is component state keyed by root: one row at a
     time can be arming a confirmation, and clicking Cancel (or a different
     row's Remove) must disarm without any server round-trip. */
  let confirmingRoot = $state<string | null>(null)

  function shortName(root: string): string {
    return root.split('/').filter(Boolean).pop() ?? root
  }

  async function refresh() {
    loading = true
    error = null
    try {
      projects = await listProjects()
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    } finally {
      loading = false
    }
  }

  async function add() {
    const root = newRoot.trim()
    if (!root) return
    error = null
    try {
      /* addProject returns the fresh list, so the rows replace in one
         round-trip rather than a second GET that could race a spawn. */
      projects = await addProject(root)
      newRoot = ''
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    }
  }

  /* Removing a project that still has live agents is the bridge's
     concern — the panel just adopts the list it gets back. */
  async function remove(root: string) {
    error = null
    confirmingRoot = null
    try {
      projects = await removeProject(root)
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    }
  }

  // Load on mount
  $effect(() => {
    refresh()
  })
</script>

<Card>
  <div class="mb-4 flex items-center justify-between">
    <h2 class="text-sm font-semibold">Projects</h2>
    <Button variant="ghost" onclick={refresh} disabled={loading}>Refresh</Button>
  </div>

  {#if error}
    <div class="mb-3 rounded-md border border-danger bg-danger/10 p-3 text-sm text-danger">{error}</div>
  {/if}

  <div class="mb-4 flex flex-col gap-2 border-t border-border pt-3">
    <input
      class="rounded-md border border-border bg-bg px-3 py-2 text-sm"
      placeholder="Repository root (e.g. /home/u/project)"
      bind:value={newRoot}
    />
    <Button onclick={add} disabled={!newRoot.trim()}>Add project</Button>
  </div>

  {#if projects.length === 0 && !loading}
    <p class="text-sm text-muted">No projects registered.</p>
  {/if}

  <ul class="flex flex-col gap-2">
    {#each projects as p (p.root)}
      <li class="flex items-center justify-between gap-3 border-t border-border pt-2 first:border-0 first:pt-0">
        <div class="min-w-0">
          <div class="truncate text-sm font-medium" title={p.root}>{shortName(p.root)}</div>
          <div class="mt-1 flex flex-wrap gap-2 text-xs text-muted">
            {#if !p.available}
              <span class="text-danger">! {p.error ?? 'unavailable'}</span>
            {/if}
            {#if p.trust}
              <Badge tone={p.trust === 'trusted' ? 'running' : p.trust === 'untrusted' ? 'attention' : 'muted'}>{p.trust}</Badge>
            {/if}
            {#if p.orphanWorktrees && p.orphanWorktrees.length > 0}
              <Badge tone="muted">{p.orphanWorktrees.length} orphan worktree{p.orphanWorktrees.length === 1 ? '' : 's'}</Badge>
            {/if}
          </div>
        </div>
        {#if confirmingRoot === p.root}
          <div class="flex shrink-0 items-center gap-2">
            <Button variant="danger" onclick={() => remove(p.root)}>Confirm remove</Button>
            <Button variant="ghost" onclick={() => (confirmingRoot = null)}>Cancel</Button>
          </div>
        {:else}
          <Button variant="ghost" onclick={() => (confirmingRoot = p.root)}>Remove</Button>
        {/if}
      </li>
    {/each}
  </ul>
</Card>
