<script lang="ts">
  import { onMount } from 'svelte'
  import Button from '../ui/Button.svelte'
  import Modal from '../ui/Modal.svelte'
  import GateResult from '../GateResult.svelte'
  import { APIError, discardAgent, ensureToken, errMessage, exitAgent, getCommitDraft, mergeAgent, patchUrl, type ExitResult, type MergeResult } from '../api'
  import { mergeRefusalMessage } from '../diff'
  import { exitDestination } from '../exit'
  import type { AgentRow } from '../fleet'

  let { agent, onDone }: { agent: AgentRow; onDone: (outcome: string) => void } = $props()

  let message = $state('')
  let busy = $state(false)
  let notice = $state('')
  let blocked = $state<ExitResult | null>(null)
  let confirming = $state(false)
  const dest = $derived(exitDestination({ sourceKind: agent.sourceKind ?? '', readOnly: agent.readOnly ?? false }))

  onMount(async () => {
    try {
      const d = await getCommitDraft(agent.id)
      if (d !== 'unsupported' && !message) message = d
    } catch {
      // The textarea just stays empty.
    }
  })

  async function merge() {
    busy = true
    notice = ''
    try {
      const r = await mergeAgent(agent.id, message.trim() || undefined)
      if (r.merged) onDone('Merged')
      else notice = mergeRefusalMessage(r)
    } catch (e) {
      notice = e instanceof APIError && e.status === 409 ? mergeRefusalMessage(e.body as MergeResult) : errMessage(e)
    } finally {
      busy = false
    }
  }

  /** Called by the page's gate section, which owns the override form while a push is the destination. */
  export function pushWithOverride(reason: string) {
    return push({ reason })
  }

  async function push(override?: { reason: string }) {
    busy = true
    notice = ''
    try {
      const r = await exitAgent(agent.id, { commitMessage: message.trim() || 'agent work', ...(override ? { override } : {}) })
      if (r.blocked) {
        blocked = r
        notice = 'Push was blocked by the gate.'
      } else {
        blocked = null
        onDone(r.prUrl ? `Pull request: ${r.prUrl}` : 'Pushed')
      }
    } catch (e) {
      notice = errMessage(e)
    } finally {
      busy = false
    }
  }

  // The patch route needs the bearer token, which a plain link cannot send.
  async function downloadPatch() {
    busy = true
    notice = ''
    try {
      const res = await fetch(patchUrl(agent.id), { headers: { Authorization: `Bearer ${ensureToken()}` } })
      if (!res.ok) throw new Error(`Download failed: ${res.status}`)
      const url = URL.createObjectURL(await res.blob())
      const a = document.createElement('a')
      a.href = url
      a.download = `marshal-${agent.id}.patch`
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
    } catch (e) {
      notice = errMessage(e)
    } finally {
      busy = false
    }
  }

  async function discard() {
    busy = true
    try {
      await discardAgent(agent.id)
      confirming = false
      onDone('Discarded')
    } catch (e) {
      confirming = false
      notice = errMessage(e)
    } finally {
      busy = false
    }
  }
</script>

<div class="flex flex-col gap-3" data-testid="ship-panel">
  <h3 class="text-xs tracking-wide text-muted uppercase">Ship</h3>
  <textarea bind:value={message} rows="4" placeholder="Commit message" aria-label="Commit message" class="w-full rounded border border-border bg-bg p-2 text-sm"></textarea>

  {#if dest === 'push'}
    <label class="flex items-center gap-2 text-sm text-muted"><input type="checkbox" checked disabled /> Open a pull request <span class="text-xs">PR is created on push</span></label>
  {/if}
  <label class="flex items-center gap-2 text-sm text-dim" title="Coming in W5"><input type="checkbox" disabled /> Request review bot</label>

  {#if blocked?.verify}
    <GateResult result={blocked.verify} onOverride={(reason) => push({ reason })} />
  {/if}
  {#if notice}<div class="rounded-md border border-attention bg-attention/10 p-2 text-sm">{notice}</div>{/if}

  <div class="flex flex-wrap gap-2">
    {#if dest === 'merge'}
      <Button disabled={busy} onclick={merge}>Merge locally</Button>
    {:else if dest === 'push'}
      <Button disabled={busy} onclick={() => push()}>Push &amp; open PR</Button>
    {:else}
      <Button disabled={busy} onclick={downloadPatch}>Download patch</Button>
    {/if}
    <Button variant="ghost" disabled={busy} onclick={() => (confirming = true)}>Discard</Button>
  </div>
</div>

{#if confirming}
  <Modal title="Discard this agent's work?" description="The branch and its changes are deleted. This cannot be undone." onDismiss={() => {
      confirming = false
    }}>
    {#snippet footer()}
      <Button variant="ghost" onclick={() => (confirming = false)}>Cancel</Button>
      <Button variant="danger" disabled={busy} onclick={discard}>Discard</Button>
    {/snippet}
    {#snippet children()}{/snippet}
  </Modal>
{/if}
