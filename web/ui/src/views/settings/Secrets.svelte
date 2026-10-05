<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../../lib/ui/Card.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Tag from '../../lib/ui/Tag.svelte'
  import { deleteSecret, errMessage, getSecretsStatus, listSecrets, putSecret, type SecretsStatus } from '../../lib/api'

  let { onToast }: { onToast: (t: string) => void } = $props()

  /*
    Values never come back from the bridge, and this page never keeps one
    past the call that sends it: the password input is cleared whether the
    save worked or not.
  */
  let status = $state<SecretsStatus | null>(null)
  let refs = $state<string[]>([])
  let prefix = $state('')
  let error = $state('')
  let loaded = $state(false)
  let busy = $state(false)

  let newRef = $state('')
  let newValue = $state('')
  // Overwriting an existing ref: one row at a time.
  let editing = $state<string | null>(null)
  let editValue = $state('')
  let removing = $state<string | null>(null)

  const readOnly = $derived(status?.backend === 'env')
  const normalize = (r: string) => (r.trim().startsWith('vault:') ? r.trim() : `vault:${r.trim()}`)

  async function load() {
    try {
      status = await getSecretsStatus()
      refs = await listSecrets(prefix.trim() || undefined)
      error = ''
    } catch (e) {
      error = errMessage(e)
    } finally {
      loaded = true
    }
  }
  onMount(() => void load())

  async function set(ref: string, value: string) {
    busy = true
    error = ''
    try {
      await putSecret(ref, value)
      onToast(`Stored ${ref}`)
      await load()
    } catch (e) {
      error = errMessage(e)
    } finally {
      busy = false
    }
  }

  async function add() {
    const ref = normalize(newRef)
    const value = newValue
    newValue = ''
    await set(ref, value)
    if (!error) newRef = ''
  }
  async function overwrite(ref: string) {
    const value = editValue
    editValue = ''
    editing = null
    await set(ref, value)
  }

  async function remove(ref: string) {
    removing = null
    error = ''
    try {
      await deleteSecret(ref)
      onToast(`Deleted ${ref}`)
      await load()
    } catch (e) {
      error = errMessage(e)
    }
  }
</script>

<div class="flex flex-col gap-4">
  <Card class="flex flex-wrap items-center gap-3" data-testid="secrets-status">
    <span class="text-sm font-medium">Backend</span>
    {#if status}
      <Tag tone="neutral">{status.backend}</Tag>
      <Tag tone={status.healthy ? 'ok' : 'err'}>{status.healthy ? 'healthy' : 'unreachable'}</Tag>
      {#if status.error}<span class="text-xs text-err">{status.error}</span>{/if}
    {:else if loaded}
      <span class="text-sm text-muted">Unknown</span>
    {/if}
    <span class="flex-1"></span>
    <p class="text-xs text-muted">Values are write-only: they are stored and injected by the proxy, never shown.</p>
  </Card>

  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}

  <Card class="flex flex-col gap-3">
    <h2 class="text-sm font-semibold">Set a secret</h2>
    {#if readOnly}
      <p class="text-sm text-muted" data-testid="env-note">Configure the local or OpenBao backend to store secrets</p>
    {/if}
    <form aria-label="Set a secret" class="flex flex-wrap items-end gap-2" onsubmit={(e) => { e.preventDefault(); void add() }}>
      <label class="flex min-w-48 flex-1 flex-col gap-1 text-xs">Ref
        <input aria-label="Secret ref" placeholder="vault:git/github" bind:value={newRef} disabled={readOnly} class="rounded border border-border bg-bg p-2 font-mono text-sm" />
      </label>
      <label class="flex min-w-48 flex-1 flex-col gap-1 text-xs">Value
        <input type="password" autocomplete="off" aria-label="Secret value" bind:value={newValue} disabled={readOnly} class="rounded border border-border bg-bg p-2 text-sm" />
      </label>
      <Button type="submit" disabled={readOnly || busy || !newRef.trim() || !newValue}>Set</Button>
    </form>
  </Card>

  <Card class="flex flex-col gap-3">
    <div class="flex items-center gap-2">
      <h2 class="text-sm font-semibold">Stored refs</h2>
      <span class="flex-1"></span>
      <form class="flex gap-2" onsubmit={(e) => { e.preventDefault(); void load() }}>
        <input aria-label="Filter by prefix" placeholder="prefix, e.g. git/" bind:value={prefix} class="rounded border border-border bg-bg p-1.5 text-sm" />
        <Button type="submit" variant="ghost" class="min-h-8 px-2 py-1 text-xs">Filter</Button>
      </form>
    </div>
    <ul class="flex flex-col gap-2" data-testid="secret-list">
      {#each refs as ref (ref)}
        <li class="flex flex-col gap-2 border-t border-border pt-2 first:border-0 first:pt-0" data-testid="secret-row">
          <div class="flex items-center gap-2">
            <span class="min-w-0 flex-1 truncate font-mono text-sm">{ref}</span>
            <span class="text-xs text-muted">value hidden</span>
            <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" disabled={readOnly} onclick={() => { editing = ref; editValue = '' }}>Set</Button>
            {#if removing === ref}
              <Button variant="danger" class="min-h-8 px-2 py-1 text-xs" onclick={() => remove(ref)}>Confirm delete</Button>
              <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => (removing = null)}>Cancel</Button>
            {:else}
              <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => (removing = ref)}>Delete</Button>
            {/if}
          </div>
          {#if editing === ref}
            <form class="flex gap-2" onsubmit={(e) => { e.preventDefault(); void overwrite(ref) }}>
              <input type="password" autocomplete="off" aria-label="New value for {ref}" bind:value={editValue} class="min-w-0 flex-1 rounded border border-border bg-bg p-2 text-sm" />
              <Button type="submit" disabled={busy || !editValue}>Save</Button>
              <Button variant="ghost" type="button" onclick={() => { editing = null; editValue = '' }}>Cancel</Button>
            </form>
          {/if}
        </li>
      {:else}
        {#if loaded}<li class="text-sm text-muted">No secrets stored{prefix.trim() ? ' under that prefix' : ''}.</li>{/if}
      {/each}
    </ul>
  </Card>
</div>
