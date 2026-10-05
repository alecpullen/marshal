<script lang="ts">
  import Button from './ui/Button.svelte'
  import { createStatusLink, errMessage } from './api'

  let { agentId }: { agentId: string } = $props()

  const TTLS = [
    { hours: 24, label: '1 day' },
    { hours: 168, label: '7 days' },
    { hours: 720, label: '30 days' },
  ]

  let open = $state(false)
  let ttl = $state(168)
  let url = $state('')
  let error = $state('')
  let busy = $state(false)
  let copied = $state(false)

  async function create() {
    busy = true
    error = ''
    try {
      const r = await createStatusLink(agentId, ttl)
      // The token is in this URL and is never shown again.
      url = `${location.origin}${r.url}`
    } catch (e) {
      error = errMessage(e)
    } finally {
      busy = false
    }
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(url)
      copied = true
      setTimeout(() => (copied = false), 1500)
    } catch {
      error = 'Could not copy the link.'
    }
  }

  function toggle() {
    open = !open
    if (!open) {
      // Closing forgets the link, so the token is not kept around.
      url = ''
      error = ''
    }
  }
</script>

<div class="relative">
  <button type="button" class="rounded border border-border px-2 py-1 text-xs text-muted hover:text-fg" aria-expanded={open} onclick={toggle}>Share status</button>
  {#if open}
    <div class="absolute right-0 z-20 mt-1 flex w-72 flex-col gap-2 rounded-lg border border-border bg-surface p-3 text-sm shadow-xl" role="dialog" aria-label="Share status">
      {#if error}<p class="text-xs text-err" role="alert">{error}</p>{/if}
      {#if url}
        <input readonly value={url} aria-label="Status link" class="rounded border border-border bg-bg p-1.5 font-mono text-xs" onfocus={(e) => e.currentTarget.select()} />
        <div class="flex items-center gap-2">
          <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={copy}>{copied ? 'Copied' : 'Copy'}</Button>
          <span class="text-xs text-muted">Shown only once. Anyone with the link sees progress, nothing else.</span>
        </div>
      {:else}
        <label class="flex flex-col gap-1 text-xs">Expires after
          <select bind:value={ttl} class="rounded border border-border bg-bg p-1.5 text-sm">
            {#each TTLS as t (t.hours)}<option value={t.hours}>{t.label}</option>{/each}
          </select>
        </label>
        <Button disabled={busy} onclick={create}>Create</Button>
      {/if}
    </div>
  {/if}
</div>
