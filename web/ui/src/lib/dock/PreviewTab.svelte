<script lang="ts">
  import { onMount } from 'svelte'
  import Button from '../ui/Button.svelte'
  import { errMessage, getWorkspace, openPreview } from '../api'

  /*
    Previews are served from a separate origin (the bridge's --preview-addr
    listener), so the page in the frame cannot reach this app's storage or
    its API. allow-same-origin is therefore safe: it lets the previewed app
    keep its own cookies, including the one that authorises the preview.
  */
  let {
    agentId,
    workspace,
  }: {
    agentId: string
    workspace?: { name: string; version: number; source: 'studio' | 'repo' }
  } = $props()

  let ports = $state<number[]>([])
  let loaded = $state(false)
  let error = $state('')
  let opening = $state<number | null>(null)
  let current = $state<{ port: number; url: string } | null>(null)
  let frameKey = $state(0)
  let copied = $state(false)

  onMount(async () => {
    if (workspace?.source === 'studio') {
      try {
        const w = await getWorkspace(workspace.name, workspace.version)
        ports = w.doc.preview?.ports ?? []
      } catch (e) {
        error = errMessage(e)
      }
    }
    loaded = true
  })

  async function open(port: number) {
    opening = port
    error = ''
    try {
      const r = await openPreview(agentId, port)
      current = { port, url: r.url }
      frameKey++
    } catch (e) {
      error = errMessage(e)
    } finally {
      opening = null
    }
  }

  async function copy() {
    if (!current) return
    try {
      await navigator.clipboard.writeText(current.url)
      copied = true
      setTimeout(() => (copied = false), 1500)
    } catch {
      error = 'Could not copy the link.'
    }
  }
</script>

<div class="flex h-full min-h-0 flex-col gap-2 p-3 text-sm" data-testid="preview-tab">
  {#if error}<p class="text-xs text-err" role="alert">{error}</p>{/if}

  {#if loaded && ports.length === 0}
    <p class="text-xs text-muted" data-testid="preview-empty">
      Declare <code class="font-mono">[preview] ports</code> in the workspace to preview what the agent serves.
      {#if workspace?.source === 'studio'}
        <a class="text-accent underline" href="#workspaces/{encodeURIComponent(workspace.name)}/edit">Open the designer</a>
      {/if}
    </p>
  {:else}
    <div class="flex flex-wrap items-center gap-2" role="group" aria-label="Declared ports">
      {#each ports as p (p)}
        <Button variant={current?.port === p ? 'default' : 'ghost'} class="min-h-8 px-2 py-1 text-xs" disabled={opening !== null} onclick={() => open(p)}>
          Open :{p}
        </Button>
      {/each}
    </div>
  {/if}

  {#if current}
    <div class="flex items-center gap-1 text-xs">
      <span class="min-w-0 truncate font-mono text-muted" title={current.url}>:{current.port}</span>
      <button type="button" class="ml-auto rounded border border-border px-1.5 py-0.5 text-muted hover:text-fg" onclick={() => frameKey++}>Reload</button>
      <a class="rounded border border-border px-1.5 py-0.5 text-muted hover:text-fg" href={current.url} target="_blank" rel="noopener noreferrer">Open in new tab</a>
      <button type="button" class="rounded border border-border px-1.5 py-0.5 text-muted hover:text-fg" onclick={copy}>{copied ? 'Copied' : 'Copy link'}</button>
    </div>
    {#key frameKey}
      <iframe
        title="Preview of port {current.port}"
        src={current.url}
        sandbox="allow-scripts allow-forms allow-same-origin"
        class="min-h-64 w-full flex-1 rounded border border-border bg-white"
      ></iframe>
    {/key}
  {/if}
</div>
