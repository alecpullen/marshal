<script lang="ts">
  import Card from './ui/Card.svelte'
  import Button from './ui/Button.svelte'
  import Badge from './ui/Badge.svelte'
  import { listClients, createClient, deleteClient, type MCPClient, type CreateClientResult } from './api'

  let clients = $state<MCPClient[]>([])
  let loading = $state(false)
  let error = $state<string | null>(null)
  let newName = $state('')
  let newAutonomous = $state(false)
  let created = $state<CreateClientResult | null>(null)
  let copied = $state(false)

  async function refresh() {
    loading = true
    error = null
    try {
      clients = await listClients()
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    } finally {
      loading = false
    }
  }

  async function create() {
    error = null
    try {
      created = await createClient({ name: newName, autonomous: newAutonomous })
      newName = ''
      newAutonomous = false
      await refresh()
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    }
  }

  async function remove(id: string) {
    error = null
    try {
      await deleteClient(id)
      await refresh()
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    }
  }

  async function copyToken() {
    if (!created?.token) return
    try {
      await navigator.clipboard.writeText(created.token)
      copied = true
      setTimeout(() => (copied = false), 2000)
    } catch {
      // clipboard may be unavailable
    }
  }

  // Load on mount
  $effect(() => {
    refresh()
  })
</script>

<Card>
  <div class="mb-4 flex items-center justify-between">
    <h2 class="text-sm font-semibold">MCP Clients</h2>
    <Button variant="ghost" onclick={refresh} disabled={loading}>Refresh</Button>
  </div>

  {#if error}
    <div class="mb-3 rounded-md border border-danger bg-danger/10 p-3 text-sm text-danger">{error}</div>
  {/if}

  {#if created}
    <div class="mb-4 rounded-md border border-attention bg-attention/10 p-3">
      <div class="text-sm font-medium text-attention">Token for {created.name} — shown once</div>
      <div class="mt-2 flex items-center gap-2">
        <code class="flex-1 break-all rounded bg-bg p-2 text-xs">{created.token}</code>
        <Button variant="ghost" onclick={copyToken}>{copied ? 'Copied' : 'Copy'}</Button>
      </div>
      <p class="mt-2 text-xs text-muted">
        This token will not be shown again. Store it now — you will need it to configure your MCP client.
      </p>
      <Button variant="ghost" class="mt-2" onclick={() => (created = null)}>Dismiss</Button>
    </div>
  {/if}

  <div class="mb-4 flex flex-col gap-2 border-t border-border pt-3">
    <input
      class="rounded-md border border-border bg-bg px-3 py-2 text-sm"
      placeholder="Client name (e.g. claude-code)"
      bind:value={newName}
    />
    <label class="flex items-center gap-2 text-sm">
      <input type="checkbox" bind:checked={newAutonomous} />
      Autonomous (skip confirmation)
    </label>
    <Button onclick={create} disabled={!newName.trim()}>Create client</Button>
  </div>

  {#if clients.length === 0 && !loading}
    <p class="text-sm text-muted">No MCP clients registered.</p>
  {/if}

  <ul class="flex flex-col gap-2">
    {#each clients as c (c.id)}
      <li class="flex items-center justify-between gap-3 border-t border-border pt-2 first:border-0 first:pt-0">
        <div class="min-w-0">
          <div class="truncate text-sm font-medium">{c.name}</div>
          <div class="mt-1 flex flex-wrap gap-2 text-xs text-muted">
            {#if c.autonomous}
              <Badge tone="running">autonomous</Badge>
            {/if}
            {#if c.maxConcurrent > 0}
              <Badge tone="muted">max {c.maxConcurrent} concurrent</Badge>
            {/if}
            {#if c.maxPerDay > 0}
              <Badge tone="muted">max {c.maxPerDay}/day</Badge>
            {/if}
            {#if c.allowedRepos && c.allowedRepos.length > 0}
              <Badge tone="muted">{c.allowedRepos.length} repos</Badge>
            {/if}
          </div>
        </div>
        <Button variant="ghost" onclick={() => remove(c.id)}>Delete</Button>
      </li>
    {/each}
  </ul>
</Card>
