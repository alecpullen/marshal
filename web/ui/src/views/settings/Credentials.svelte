<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../../lib/ui/Card.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Tag from '../../lib/ui/Tag.svelte'
  import { deleteCredential, errMessage, listCredentials, putCredential, type CredentialInput, type CredentialKind, type CredentialRow } from '../../lib/api'

  let { onToast }: { onToast: (t: string) => void } = $props()

  let rows = $state<CredentialRow[]>([])
  let error = $state('')
  let loaded = $state(false)
  let busy = $state(false)
  let removing = $state<string | null>(null)

  let adding = $state(false)
  let id = $state('')
  let kind = $state<CredentialKind>('pat')
  let envVar = $state('')
  let keyPath = $state('')
  let ref = $state('')
  let user = $state('')

  async function load() {
    try {
      rows = await listCredentials()
      error = ''
    } catch (e) {
      error = errMessage(e)
    } finally {
      loaded = true
    }
  }
  onMount(() => void load())

  const detail = (c: CredentialRow) => (c.kind === 'pat' ? c.envVar : c.kind === 'ssh' ? c.keyPath : c.kind === 'vault' ? c.ref : '') || '—'

  function reset() {
    adding = false
    id = envVar = keyPath = ref = user = ''
    kind = 'pat'
  }

  async function add() {
    // Only the field that belongs to the kind goes out.
    const body: CredentialInput = { id: id.trim(), kind }
    if (kind === 'pat') body.envVar = envVar.trim()
    if (kind === 'ssh') body.keyPath = keyPath.trim()
    if (kind === 'vault') body.ref = ref.trim().startsWith('vault:') ? ref.trim() : `vault:${ref.trim()}`
    if (kind !== 'none' && user.trim()) body.user = user.trim()
    busy = true
    error = ''
    try {
      await putCredential(body)
      onToast(`Added credential ${body.id}`)
      reset()
      await load()
    } catch (e) {
      error = errMessage(e)
    } finally {
      busy = false
    }
  }

  async function remove(cid: string) {
    removing = null
    error = ''
    try {
      await deleteCredential(cid)
      onToast(`Deleted credential ${cid}`)
      await load()
    } catch (e) {
      // A credential a repo uses is refused with the repo named; show that as is.
      error = errMessage(e)
    }
  }

  const ready = $derived(
    !!id.trim() && (kind === 'none' || (kind === 'pat' && !!envVar.trim()) || (kind === 'ssh' && !!keyPath.trim()) || (kind === 'vault' && !!ref.trim())),
  )
</script>

<div class="flex flex-col gap-4">
  <div class="flex items-center justify-between">
    <p class="text-sm text-muted">Credentials say how a repo is reached. Their secrets live in the secrets backend, not here.</p>
    <Button onclick={() => (adding = true)} disabled={adding}>Add credential</Button>
  </div>

  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}

  {#if adding}
    <Card class="flex flex-col gap-3" data-testid="add-credential">
      <label class="flex flex-col gap-1 text-xs">Id
        <input aria-label="Credential id" bind:value={id} class="rounded border border-border bg-bg p-2 font-mono text-sm" />
      </label>
      <label class="flex flex-col gap-1 text-xs">Kind
        <select aria-label="Kind" bind:value={kind} class="rounded border border-border bg-bg p-2 text-sm">
          <option value="pat">Personal access token (env var)</option>
          <option value="ssh">SSH key (file)</option>
          <option value="vault">Vault secret</option>
          <option value="none">None</option>
        </select>
      </label>
      {#if kind === 'pat'}
        <label class="flex flex-col gap-1 text-xs">Environment variable
          <input aria-label="Environment variable" placeholder="GITHUB_TOKEN" bind:value={envVar} class="rounded border border-border bg-bg p-2 font-mono text-sm" />
        </label>
      {:else if kind === 'ssh'}
        <label class="flex flex-col gap-1 text-xs">Key path
          <input aria-label="Key path" placeholder="/home/u/.ssh/id_ed25519" bind:value={keyPath} class="rounded border border-border bg-bg p-2 font-mono text-sm" />
        </label>
      {:else if kind === 'vault'}
        <label class="flex flex-col gap-1 text-xs">Secret ref
          <input aria-label="Secret ref" placeholder="vault:git/github" bind:value={ref} class="rounded border border-border bg-bg p-2 font-mono text-sm" />
        </label>
      {/if}
      {#if kind !== 'none'}
        <label class="flex flex-col gap-1 text-xs">User (optional)
          <input aria-label="User" bind:value={user} class="rounded border border-border bg-bg p-2 text-sm" />
        </label>
      {/if}
      <div class="flex gap-2">
        <Button disabled={busy || !ready} onclick={add}>Save credential</Button>
        <Button variant="ghost" onclick={reset}>Cancel</Button>
      </div>
    </Card>
  {/if}

  <Card class="overflow-x-auto p-0">
    <table class="w-full text-left text-sm" data-testid="credentials-table">
      <thead class="text-xs text-muted">
        <tr>
          <th scope="col" class="px-3 py-2 font-medium">Id</th>
          <th scope="col" class="px-3 py-2 font-medium">Kind</th>
          <th scope="col" class="px-3 py-2 font-medium">Ref or env var</th>
          <th scope="col" class="px-3 py-2 font-medium">User</th>
          <th scope="col" class="px-3 py-2 font-medium">State</th>
          <th scope="col" class="px-3 py-2"></th>
        </tr>
      </thead>
      <tbody>
        {#each rows as c (c.id)}
          <tr class="border-t border-border" data-testid="credential-row">
            <td class="px-3 py-2 font-mono">{c.id}</td>
            <td class="px-3 py-2"><Tag tone="neutral">{c.kind}</Tag></td>
            <td class="px-3 py-2 font-mono text-xs">{detail(c)}</td>
            <td class="px-3 py-2">{c.user || '—'}</td>
            <td class="px-3 py-2"><Tag tone={c.set ? 'ok' : 'warn'}>{c.set ? 'set' : 'not set'}</Tag></td>
            <td class="px-3 py-2 text-right">
              {#if removing === c.id}
                <Button variant="danger" class="min-h-8 px-2 py-1 text-xs" onclick={() => remove(c.id)}>Confirm delete</Button>
                <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => (removing = null)}>Cancel</Button>
              {:else}
                <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => (removing = c.id)}>Delete</Button>
              {/if}
            </td>
          </tr>
        {:else}
          <tr><td colspan="6" class="px-3 py-4 text-sm text-muted">{loaded ? 'No credentials yet.' : 'Loading…'}</td></tr>
        {/each}
      </tbody>
    </table>
  </Card>
</div>
