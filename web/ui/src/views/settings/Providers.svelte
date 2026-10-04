<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../../lib/ui/Card.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Tag from '../../lib/ui/Tag.svelte'
  import { getModels, setProviders, setProviderKey, probeProvider, errMessage, type ProbeResult, type ProviderView } from '../../lib/api'
  import { PROVIDER_TEMPLATES, templateById, uniqueName } from '../../lib/models/providerTemplates'

  let { onToast }: { onToast: (t: string) => void } = $props()

  let providers = $state<Record<string, ProviderView>>({})
  let error = $state('')
  let loaded = $state(false)
  let probes = $state<Record<string, ProbeResult | 'pending'>>({})
  let keyFor = $state<string | null>(null)
  let keyValue = $state('')
  let removing = $state<string | null>(null)
  let busy = $state(false)

  // Add provider form.
  let adding = $state(false)
  let templateId = $state(PROVIDER_TEMPLATES[0].id)
  let newName = $state('')
  let newUrl = $state('')

  const names = $derived(Object.keys(providers).sort())

  async function load() {
    try {
      providers = (await getModels()).providers
      error = ''
    } catch (e) {
      error = errMessage(e)
    } finally {
      loaded = true
    }
  }
  onMount(() => void load())

  function pickTemplate(id: string) {
    templateId = id
    const t = templateById(id)
    newName = uniqueName(id.replace(/_/g, '-'), names)
    newUrl = t?.baseUrl ?? ''
  }
  function startAdd() {
    adding = true
    pickTemplate(templateId)
  }

  async function add() {
    const t = templateById(templateId)
    const name = newName.trim()
    if (!t || !name) return
    busy = true
    error = ''
    try {
      // Only the new provider goes out; the others keep their stored values.
      await setProviders({ [name]: { type: t.type, baseUrl: newUrl.trim(), template: t.id, toolCalling: t.toolCalling ?? false, ...(t.auth ? { auth: t.auth } : {}) } })
      adding = false
      onToast(`Added ${name}. Applies to agents started from now`)
      await load()
    } catch (e) {
      error = errMessage(e)
    } finally {
      busy = false
    }
  }

  async function probe(name: string) {
    probes = { ...probes, [name]: 'pending' }
    try {
      probes = { ...probes, [name]: await probeProvider(name) }
    } catch (e) {
      probes = { ...probes, [name]: { models: [], error: errMessage(e) } }
    }
  }

  async function saveKey(name: string) {
    busy = true
    error = ''
    try {
      await setProviderKey(name, keyValue)
      onToast(`Saved the key for ${name}. Applies to agents started from now`)
      keyFor = null
      await load()
    } catch (e) {
      error = errMessage(e)
    } finally {
      // The secret does not outlive the call, whether it worked or not.
      keyValue = ''
      busy = false
    }
  }

  async function remove(name: string) {
    removing = null
    error = ''
    try {
      await setProviders({ [name]: null })
      onToast(`Removed ${name}. Applies to agents started from now`)
      await load()
    } catch (e) {
      error = errMessage(e)
    }
  }

  const keyLabel = (p: ProviderView) =>
    p.auth === 'oauth' ? 'browser sign-in' : p.keySource === 'config' ? 'key in config' : p.keySource === 'env' ? `key from ${p.apiKeyEnv || 'env'}` : 'no key'
  const dot = (p: ProbeResult | 'pending' | undefined) =>
    p === 'pending' ? 'bg-warn' : !p ? 'bg-dim' : p.error ? 'bg-err' : 'bg-ok'
  const dotLabel = (p: ProbeResult | 'pending' | undefined) =>
    p === 'pending' ? 'checking' : !p ? 'not probed' : p.error ? 'unreachable' : 'healthy'
</script>

<div class="flex flex-col gap-4">
  <div class="flex items-center justify-between">
    <p class="text-sm text-muted">Providers are saved to your user config and shared by every agent.</p>
    <Button onclick={startAdd} disabled={adding}>Add provider</Button>
  </div>

  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}

  {#if adding}
    <Card class="flex flex-col gap-3" data-testid="add-provider">
      <label class="flex flex-col gap-1 text-xs">Template
        <select aria-label="Template" class="rounded border border-border bg-bg p-2 text-sm" value={templateId} onchange={(e) => pickTemplate(e.currentTarget.value)}>
          {#each PROVIDER_TEMPLATES as t (t.id)}<option value={t.id}>{t.label}</option>{/each}
        </select>
      </label>
      <label class="flex flex-col gap-1 text-xs">Name
        <input aria-label="Provider name" bind:value={newName} class="rounded border border-border bg-bg p-2 text-sm" />
      </label>
      <label class="flex flex-col gap-1 text-xs">Base URL
        <input aria-label="Base URL" bind:value={newUrl} class="rounded border border-border bg-bg p-2 text-sm" />
      </label>
      <div class="flex gap-2">
        <Button disabled={busy || !newName.trim()} onclick={add}>Save provider</Button>
        <Button variant="ghost" onclick={() => (adding = false)}>Cancel</Button>
      </div>
    </Card>
  {/if}

  <div class="grid gap-3 sm:grid-cols-2">
    {#each names as name (name)}
      {@const p = providers[name]}
      {@const pr = probes[name]}
      <Card class="flex flex-col gap-2" data-testid="provider-card">
        <div class="flex items-center gap-2">
          <span class="size-2 shrink-0 rounded-full {dot(pr)}" role="img" aria-label={dotLabel(pr)}></span>
          <span class="font-mono text-sm font-medium">{name}</span>
          <span class="flex-1"></span>
          <Tag tone="neutral">{p.type}</Tag>
        </div>
        <p class="truncate font-mono text-xs text-muted">{p.baseUrl || 'no base URL'}</p>
        <div class="flex items-center gap-2 text-xs">
          <Tag tone={p.hasKey || p.auth === 'oauth' ? 'ok' : 'warn'}>{keyLabel(p)}</Tag>
          {#if pr && pr !== 'pending'}
            {#if pr.error}<span class="text-err">{pr.error}</span>{:else}<span class="text-muted">{pr.models.length} models</span>{/if}
          {/if}
        </div>

        {#if keyFor === name}
          <form class="flex gap-2" onsubmit={(e) => { e.preventDefault(); void saveKey(name) }}>
            <input
              type="password"
              autocomplete="off"
              aria-label="API key for {name}"
              bind:value={keyValue}
              class="min-w-0 flex-1 rounded border border-border bg-bg p-2 text-sm"
            />
            <Button type="submit" disabled={busy || !keyValue}>Save key</Button>
            <Button variant="ghost" type="button" onclick={() => { keyFor = null; keyValue = '' }}>Cancel</Button>
          </form>
        {/if}

        <div class="flex flex-wrap items-center gap-2">
          <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => probe(name)} disabled={pr === 'pending'}>Probe</Button>
          {#if p.auth !== 'oauth'}
            <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => { keyFor = name; keyValue = '' }}>Set key</Button>
          {/if}
          {#if removing === name}
            <Button variant="danger" class="min-h-8 px-2 py-1 text-xs" onclick={() => remove(name)}>Confirm remove</Button>
            <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => (removing = null)}>Cancel</Button>
          {:else}
            <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => (removing = name)}>Remove</Button>
          {/if}
        </div>
      </Card>
    {:else}
      {#if loaded}<p class="text-sm text-muted">No providers yet. Add one to start routing models.</p>{/if}
    {/each}
  </div>
</div>
