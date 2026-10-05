<script lang="ts">
  import Button from '../ui/Button.svelte'
  import Tag from '../ui/Tag.svelte'
  import { createWebhookSecret, errMessage, listSecrets } from '../api'

  let { repoId }: { repoId: string } = $props()

  let isSet = $state<boolean | null>(null)
  let secret = $state('')
  let busy = $state(false)
  let error = $state('')
  let copied = $state('')
  // Replacing breaks the forge's configured webhook until the new secret is pasted, so it takes a second click.
  let armed = $state(false)

  const payloadUrl = $derived(`${location.origin}/hooks/forge/${encodeURIComponent(repoId)}`)

  async function load() {
    const id = repoId
    secret = ''
    armed = false
    isSet = null
    if (!id) return
    try {
      const refs = await listSecrets('hooks/')
      if (id === repoId) isSet = refs.some((r) => r.replace(/^vault:/, '') === `hooks/${id}`)
    } catch {
      // The secrets backend may be off; Create secret then reports why.
      isSet = null
    }
  }
  $effect(() => {
    void repoId
    void load()
  })

  async function create() {
    if (isSet && !secret && !armed) {
      armed = true
      return
    }
    armed = false
    busy = true
    error = ''
    try {
      secret = await createWebhookSecret(repoId)
      isSet = true
    } catch (e) {
      error = errMessage(e)
    } finally {
      busy = false
    }
  }

  async function copy(what: string, text: string) {
    try {
      await navigator.clipboard.writeText(text)
      copied = what
    } catch {
      // The value is on screen to copy by hand.
    }
  }
</script>

<div class="flex flex-col gap-2 text-sm" data-testid="webhook-setup">
  <div class="flex flex-wrap items-center gap-2">
    <span class="text-xs">Webhook</span>
    {#if !repoId}
      <span class="text-muted">Pick a repo to set up its webhook.</span>
    {:else}
      <Tag tone={isSet ? 'ok' : 'neutral'}>{isSet === null ? 'secret unknown' : isSet ? 'secret set' : 'no secret'}</Tag>
      <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" disabled={busy} onclick={create}>{armed ? 'Replace? Click again' : isSet ? 'Replace secret' : 'Create secret'}</Button>
    {/if}
  </div>
  {#if error}<p class="text-err" role="alert">{error}</p>{/if}
  {#if secret}
    <div class="flex flex-col gap-1 rounded-md border border-attention bg-attention/10 p-2" role="status">
      <p>Copy the secret now. It is not shown again.</p>
      <div class="flex items-center gap-2"><code class="min-w-0 flex-1 font-mono text-xs break-all" data-testid="webhook-secret">{secret}</code><Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => copy('secret', secret)}>{copied === 'secret' ? 'Copied' : 'Copy'}</Button></div>
    </div>
  {/if}
  {#if repoId}
    <div class="flex items-center gap-2">
      <span class="text-xs text-muted">Payload URL</span>
      <code class="min-w-0 flex-1 truncate font-mono text-xs" title={payloadUrl}>{payloadUrl}</code>
      <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => copy('url', payloadUrl)}>{copied === 'url' ? 'Copied' : 'Copy'}</Button>
    </div>
    <p class="text-xs text-muted">Content type JSON; the forge signs deliveries with the secret. Without a secret the bridge polls instead.</p>
  {/if}
</div>
