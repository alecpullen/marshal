<script lang="ts">
  import { listIssues, spawnFromIssue, type Issue } from './api'
  import Button from './ui/Button.svelte'

  let { repoId = '' }: { repoId?: string } = $props()

  let repoInput = $state(repoId)
  let issues = $state<Issue[]>([])
  let loading = $state(false)
  let error = $state<string | null>(null)
  let urlInput = $state('')

  async function load() {
    const id = repoInput.trim()
    if (!id) return
    loading = true
    error = null
    try {
      issues = await listIssues(id)
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    } finally {
      loading = false
    }
  }

  async function spawn(number: number) {
    const id = repoInput.trim()
    if (!id) return
    try {
      await spawnFromIssue(id, number)
      issues = issues.filter((i) => i.number !== number)
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    }
  }

  function spawnFromUrl() {
    const match = urlInput.match(/\/issues\/(\d+)/)
    if (match) {
      spawn(parseInt(match[1], 10))
      urlInput = ''
    } else {
      error = 'Could not find an issue number in that URL.'
    }
  }
</script>

<div class="flex flex-col gap-3 rounded-md border border-border bg-surface p-4">
  <div class="flex items-center justify-between gap-2">
    <h3 class="text-sm font-semibold">Issue intake</h3>
    <div class="flex gap-2">
      <input
        bind:value={repoInput}
        placeholder="Repo ID"
        class="min-h-11 w-40 rounded-md border border-border bg-bg p-2 text-sm"
      />
      <Button variant="ghost" onclick={load} disabled={loading || !repoInput.trim()}>
        {loading ? 'Loading…' : 'List issues'}
      </Button>
    </div>
  </div>

  {#if error}
    <div class="rounded-md border border-danger bg-danger/10 p-2 text-sm">{error}</div>
  {/if}

  <div class="flex gap-2">
    <input
      bind:value={urlInput}
      placeholder="Paste an issue URL…"
      class="min-h-11 flex-1 rounded-md border border-border bg-bg p-2 text-sm"
      onkeydown={(e) => e.key === 'Enter' && spawnFromUrl()}
    />
    <Button variant="ghost" onclick={spawnFromUrl} disabled={!repoInput.trim()}>Spawn</Button>
  </div>

  {#if issues.length > 0}
    <ul class="flex flex-col gap-2">
      {#each issues as issue (issue.number)}
        <li class="flex items-start justify-between gap-3 rounded-md border border-border bg-bg p-3">
          <div class="flex flex-col gap-1">
            <span class="text-sm font-medium">#{issue.number} {issue.title}</span>
            {#if issue.labels.length > 0}
              <div class="flex flex-wrap gap-1">
                {#each issue.labels as label}
                  <span class="rounded bg-surface px-2 py-0.5 text-xs text-muted">{label}</span>
                {/each}
              </div>
            {/if}
          </div>
          <Button variant="ghost" onclick={() => spawn(issue.number)}>Spawn</Button>
        </li>
      {/each}
    </ul>
  {:else if !loading && repoInput.trim()}
    <p class="text-sm text-muted">No open issues, or the repo has no forge configured.</p>
  {/if}
</div>
