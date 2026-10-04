<script lang="ts">
  import Modal from '../ui/Modal.svelte'
  import Button from '../ui/Button.svelte'
  import type { DecisionOutcome } from './decision'

  let { outcome, onClose }: { outcome: DecisionOutcome | null; onClose: () => void } = $props()

  let copied = $state(false)

  // A draft toast clears itself; the patch modal waits for the person.
  $effect(() => {
    if (outcome?.kind !== 'draft') return
    const t = setTimeout(onClose, 8000)
    return () => clearTimeout(t)
  })
  $effect(() => {
    void outcome
    copied = false
  })

  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text)
      copied = true
    } catch {
      copied = false
    }
  }
</script>

{#if outcome?.kind === 'draft'}
  <div class="fixed right-4 bottom-4 z-50 flex max-w-sm items-center gap-3 rounded-md border border-border bg-raise p-3 text-sm shadow-lg" role="status" data-testid="decision-toast">
    <span class="min-w-0 flex-1">Added to {outcome.workspace} draft</span>
    <a class="text-accent hover:underline" href="#workspaces/{encodeURIComponent(outcome.workspace)}/edit" onclick={onClose}>Open designer</a>
    <button type="button" class="cursor-pointer text-muted hover:text-fg" aria-label="Dismiss" onclick={onClose}>✕</button>
  </div>
{:else if outcome?.kind === 'patch'}
  <Modal title="Add {outcome.host} to the workspace" description="This workspace comes from the repo, which Marshal does not write to. Apply this patch to its template file." onDismiss={onClose}>
    <pre class="max-h-80 overflow-auto rounded border border-border bg-bg p-3 font-mono text-xs" data-testid="patch-text">{outcome.patch}</pre>
    {#snippet footer()}
      <Button onclick={() => copy(outcome.patch)}>{copied ? 'Copied' : 'Copy'}</Button>
      <Button variant="ghost" onclick={onClose}>Close</Button>
    {/snippet}
  </Modal>
{/if}
