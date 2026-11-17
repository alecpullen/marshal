<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../ui/Card.svelte'
  import Button from '../ui/Button.svelte'
  import { getPreviewEnvironments, errMessage } from '../api'
  import type { FactoryCheck } from '../factory'
  import type { PreviewReport } from '../preview'
  import { checkState, deploymentSegments, displayText, environmentSegments, isHttpUrl } from './reviewModel'

  /**
   * Preview and test environments for one item's revision (design: CI-first
   * review, step 3). The read is item-local and revision-aware: it fires once
   * from the mount path and never from a parent's store update, so a factory
   * poll costs no forge request. Refresh is the only other read, and the
   * sequence number keeps the newest response whichever order they resolve in.
   *
   * A link is followed only through the D10 validator, and every unstyled
   * provider string is bounded before display.
   */
  export interface PreviewCardProps {
    repoId: string
    branch: string
    prHead?: string
    statusLinks: FactoryCheck[]
  }

  let { repoId, branch, prHead = '', statusLinks }: PreviewCardProps = $props()

  let preview = $state<PreviewReport | null>(null)
  let loading = $state(false)
  let error = $state('')
  let request = 0
  let alive = true

  async function loadPreview() {
    if (!repoId) return
    const seq = ++request
    loading = true
    error = ''
    try {
      const report = (await getPreviewEnvironments(repoId, branch)) ?? null
      if (!alive || seq !== request) return
      preview = report
    } catch (e) {
      if (!alive || seq !== request) return
      error = errMessage(e)
    }
    if (alive && seq === request) loading = false
  }

  // The mount read is the only automatic one: props and store updates redraw
  // this card, they never re-read the forge.
  onMount(() => {
    void loadPreview()
    return () => { alive = false }
  })

  const deployments = $derived((preview?.deployments ?? []).map((deployment) => deploymentSegments(deployment, prHead)))
  const environments = $derived((preview?.environments ?? []).map(environmentSegments))
  const gaps = $derived(preview?.gaps ?? [])
  const empty = $derived(!deployments.length && !environments.length && !gaps.length)
</script>

<Card class="grid min-w-0 gap-2" data-testid="factory-preview">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <h2 class="font-semibold">Preview and test environments</h2>
    <Button disabled={loading} onclick={() => void loadPreview()}>Refresh</Button>
  </div>
  {#if loading}
    <p class="text-sm text-muted">Reading preview environments…</p>
  {:else if error}
    <p class="text-sm">Preview and test environments could not be read.</p>
    <p class="break-words text-sm text-muted">{error}</p>
  {:else if preview?.status === 'unavailable'}
    <p class="text-sm">Preview and test environments could not be read.</p>
    {#if preview.error}<p class="break-words text-sm text-muted">{preview.error}</p>{/if}
  {:else if preview}
    {#each gaps as gap}<p class="break-words text-sm"><code class="text-xs">{gap.key}</code> — {gap.state} — {gap.detail}</p>{/each}
    {#if empty}<p class="text-sm text-muted">No preview deployments or test environments are configured for this revision.</p>{/if}
    {#if deployments.length}
      <ul class="grid gap-1">
        {#each deployments as segments}
          <li class="break-words text-sm">
            {#each segments as segment, i}
              {#if i}<span class="text-muted"> — </span>{/if}
              {#if segment.kind === 'code'}
                <code class="text-xs">{segment.text}</code>
              {:else if segment.kind === 'badge'}
                <span class={segment.warning ? 'text-attention' : 'text-muted'} data-testid="head-badge">{segment.label} <code class="text-xs">{segment.sha}</code></span>
              {:else if segment.kind === 'link'}
                {#if isHttpUrl(segment.url)}<a class="break-all text-accent underline" href={segment.url} target="_blank" rel="noopener noreferrer">{segment.url}</a>{:else}<span class="break-all">{displayText(segment.url)}</span>{/if}
                {#if segment.logs} (logs: {#if isHttpUrl(segment.logs)}<a class="break-all text-accent underline" href={segment.logs} target="_blank" rel="noopener noreferrer">{segment.logs}</a>{:else}<span class="break-all">{displayText(segment.logs)}</span>{/if}){/if}
              {:else}
                {segment.text}
              {/if}
            {/each}
          </li>
        {/each}
      </ul>
    {/if}
    {#if environments.length}
      <p class="text-sm">Configured environments:</p>
      <ul class="grid gap-1">
        {#each environments as segments}
          <li class="break-words text-sm">
            {#each segments as segment, i}
              {#if i}<span class="text-muted"> — </span>{/if}
              {#if segment.kind === 'code'}
                <code class="text-xs">{segment.text}</code>
              {:else if segment.kind === 'link'}
                {#if isHttpUrl(segment.url)}<a class="break-all text-accent underline" href={segment.url} target="_blank" rel="noopener noreferrer">{segment.url}</a>{:else}<span class="break-all">{displayText(segment.url)}</span>{/if}
              {:else if segment.kind === 'text'}
                {segment.text}
              {/if}
            {/each}
          </li>
        {/each}
      </ul>
    {/if}
    {#if statusLinks.length}
      <p class="text-sm">Links reported by commit statuses:</p>
      <ul class="grid gap-1">
        {#each statusLinks as check}
          <li class="break-words text-sm">
            <code class="text-xs">{check.key.name}</code> (<code class="text-xs">{checkState(check)}</code>):
            {#if isHttpUrl(check.url)}<a class="break-all text-accent underline" href={check.url} target="_blank" rel="noopener noreferrer">{check.url}</a>{:else}<span class="break-all">{displayText(check.url)}</span>{/if}
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</Card>
