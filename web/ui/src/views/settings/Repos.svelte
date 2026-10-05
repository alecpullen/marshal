<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../../lib/ui/Card.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Tag from '../../lib/ui/Tag.svelte'
  import { errMessage, listCredentials, listRepos, registerRepo, removeRepo, type CredentialRow, type RepoRow } from '../../lib/api'

  let { onToast }: { onToast: (t: string) => void } = $props()

  let repos = $state<RepoRow[]>([])
  let credentials = $state<CredentialRow[]>([])
  let error = $state('')
  let loaded = $state(false)
  let busy = $state(false)
  let removing = $state<string | null>(null)

  let id = $state('')
  let url = $state('')
  let branch = $state('')
  let forge = $state('')
  let apiBase = $state('')
  let credRef = $state('')
  let watch = $state(false)
  let watchLabel = $state('')

  async function load() {
    try {
      repos = await listRepos()
      error = ''
    } catch (e) {
      error = errMessage(e)
    } finally {
      loaded = true
    }
    // The credential picker is optional context.
    credentials = await listCredentials().catch(() => [])
  }
  onMount(() => void load())

  async function add() {
    const body: RepoRow = {
      id: id.trim(),
      url: url.trim(),
      ...(branch.trim() ? { branch: branch.trim() } : {}),
      ...(forge ? { forge } : {}),
      ...(apiBase.trim() ? { apiBase: apiBase.trim() } : {}),
      ...(credRef ? { credRef } : {}),
      watch,
      ...(watch && watchLabel.trim() ? { watchLabel: watchLabel.trim() } : {}),
    }
    busy = true
    error = ''
    try {
      await registerRepo(body)
      onToast(`Registered ${body.id}`)
      id = url = branch = forge = apiBase = credRef = watchLabel = ''
      watch = false
      await load()
    } catch (e) {
      error = errMessage(e)
    } finally {
      busy = false
    }
  }

  async function remove(rid: string) {
    removing = null
    error = ''
    try {
      await removeRepo(rid)
      onToast(`Removed ${rid}`)
      await load()
    } catch (e) {
      error = errMessage(e)
    }
  }

  const field = 'rounded border border-border bg-bg p-2 text-sm'
</script>

<div class="flex flex-col gap-4">
  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}

  <Card class="flex flex-col gap-3" data-testid="add-repo">
    <h2 class="text-sm font-semibold">Register a repo</h2>
    <form class="grid gap-3 sm:grid-cols-2" onsubmit={(e) => { e.preventDefault(); void add() }}>
      <label class="flex flex-col gap-1 text-xs">Id
        <input aria-label="Repo id" bind:value={id} class="{field} font-mono" />
      </label>
      <label class="flex flex-col gap-1 text-xs">URL
        <input aria-label="Repo URL" placeholder="https://github.com/org/repo.git" bind:value={url} class="{field} font-mono" />
      </label>
      <label class="flex flex-col gap-1 text-xs">Branch
        <input aria-label="Branch" placeholder="default branch" bind:value={branch} class="{field} font-mono" />
      </label>
      <label class="flex flex-col gap-1 text-xs">Forge
        <select aria-label="Forge" bind:value={forge} class={field}>
          <option value="">None</option>
          <option value="github">github</option>
          <option value="gitea">gitea</option>
        </select>
      </label>
      <label class="flex flex-col gap-1 text-xs">API base
        <input aria-label="API base" placeholder="forge default" bind:value={apiBase} class="{field} font-mono" />
      </label>
      <label class="flex flex-col gap-1 text-xs">Credential
        <select aria-label="Credential" bind:value={credRef} class={field}>
          <option value="">None</option>
          {#each credentials as c (c.id)}<option value={c.id}>{c.id} ({c.kind})</option>{/each}
        </select>
      </label>
      <label class="flex items-center gap-2 text-sm">
        <input type="checkbox" aria-label="Watch issues" bind:checked={watch} />
        Watch for labelled issues
      </label>
      <label class="flex flex-col gap-1 text-xs">Watch label
        <input aria-label="Watch label" bind:value={watchLabel} disabled={!watch} class={field} />
      </label>
      <div class="sm:col-span-2">
        <Button type="submit" disabled={busy || !id.trim() || !url.trim()}>Register repo</Button>
      </div>
    </form>
  </Card>

  <Card class="overflow-x-auto p-0">
    <table class="w-full text-left text-sm" data-testid="repos-table">
      <thead class="text-xs text-muted">
        <tr>
          <th scope="col" class="px-3 py-2 font-medium">Id</th>
          <th scope="col" class="px-3 py-2 font-medium">URL</th>
          <th scope="col" class="px-3 py-2 font-medium">Branch</th>
          <th scope="col" class="px-3 py-2 font-medium">Forge</th>
          <th scope="col" class="px-3 py-2 font-medium">Credential</th>
          <th scope="col" class="px-3 py-2 font-medium">Watch</th>
          <th scope="col" class="px-3 py-2"></th>
        </tr>
      </thead>
      <tbody>
        {#each repos as r (r.id)}
          <tr class="border-t border-border" data-testid="repo-row">
            <td class="px-3 py-2 font-mono">{r.id}</td>
            <td class="max-w-64 truncate px-3 py-2 font-mono text-xs" title={r.url}>{r.url}</td>
            <td class="px-3 py-2 font-mono text-xs">{r.branch || '—'}</td>
            <td class="px-3 py-2">{r.forge || '—'}</td>
            <td class="px-3 py-2 font-mono text-xs">{r.credRef || '—'}</td>
            <td class="px-3 py-2">{#if r.watch}<Tag tone="info">{r.watchLabel || 'all issues'}</Tag>{:else}—{/if}</td>
            <td class="px-3 py-2 text-right">
              {#if removing === r.id}
                <Button variant="danger" class="min-h-8 px-2 py-1 text-xs" onclick={() => remove(r.id)}>Confirm remove</Button>
                <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => (removing = null)}>Cancel</Button>
              {:else}
                <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => (removing = r.id)}>Remove</Button>
              {/if}
            </td>
          </tr>
        {:else}
          <tr><td colspan="7" class="px-3 py-4 text-sm text-muted">{loaded ? 'No repos registered.' : 'Loading…'}</td></tr>
        {/each}
      </tbody>
    </table>
  </Card>
</div>
