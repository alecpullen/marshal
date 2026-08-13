<script lang="ts">
  import Card from './ui/Card.svelte'
  import Button from './ui/Button.svelte'
  import DiffView from './DiffView.svelte'
  import { APIError, discardAgent, getDiff, listAgents, mergeAgent, type MergeResult } from './api'
  import { diffTotals, mergeRefusalMessage } from './diff'

  let {
    agentId,
    onDone,
  }: {
    agentId: string
    /** Called after a successful merge or discard, so the caller can refresh. */
    onDone: () => void
  } = $props()

  let branch = $state('')
  let isolated = $state(false)
  let notice = $state<string | null>(null)
  let needsCommitMessage = $state(false)
  let commitMessage = $state('')
  let confirmingDiscard = $state(false)
  let busy = $state(false)
  let totals = $state({ files: 0, added: 0, removed: 0 })

  // Resolve this agent's own isolation state; Chat has only the session id.
  $effect(() => {
    listAgents()
      .then((agents) => {
        const me = agents.find((a) => a.id === agentId)
        isolated = me?.isolated ?? false
        branch = me?.branch ?? ''
      })
      .catch(() => {})
  })

  // Totals back the discard confirmation, so what you approve is what you saw.
  $effect(() => {
    if (!isolated) return
    getDiff(agentId)
      .then((r) => (totals = diffTotals(r.files ?? [])))
      .catch(() => {})
  })

  function applyRefusal(res: MergeResult) {
    notice = mergeRefusalMessage(res)
    // A dirty worktree is the one refusal the user can clear from here.
    needsCommitMessage = res.reason === 'dirty'
  }

  async function doMerge() {
    busy = true
    notice = null
    try {
      const res = await mergeAgent(agentId, commitMessage.trim() || undefined)
      if (res.merged) {
        onDone()
        return
      }
      applyRefusal(res)
    } catch (e) {
      // A refusal arrives as 409; APIError carries the MergeResult body.
      if (e instanceof APIError && e.status === 409) {
        applyRefusal(e.body as MergeResult)
      } else {
        notice = e instanceof Error ? e.message : String(e)
      }
    } finally {
      busy = false
    }
  }

  async function doDiscard() {
    busy = true
    notice = null
    try {
      await discardAgent(agentId)
      confirmingDiscard = false
      onDone()
    } catch (e) {
      notice = e instanceof Error ? e.message : String(e)
    } finally {
      busy = false
    }
  }
</script>

{#if isolated}
  <div class="flex flex-col gap-3">
    <DiffView {agentId} />

    {#if notice}
      <div class="rounded-md border border-attention bg-attention/10 p-3 text-sm">{notice}</div>
    {/if}

    {#if needsCommitMessage}
      <label class="flex flex-col gap-1 text-sm">
        Commit message for the agent's uncommitted work
        <input
          bind:value={commitMessage}
          placeholder="agent work"
          class="min-h-11 rounded-md border border-border bg-bg p-2 text-sm"
        />
      </label>
    {/if}

    {#if confirmingDiscard}
      <Card class="border-danger">
        <div class="text-sm">
          Discard {totals.files} changed file{totals.files === 1 ? '' : 's'} and delete branch
          <code>{branch}</code>? This cannot be undone.
        </div>
        <div class="mt-2 flex gap-2">
          <Button variant="danger" disabled={busy} onclick={doDiscard}>Discard</Button>
          <Button variant="ghost" disabled={busy} onclick={() => (confirmingDiscard = false)}>Cancel</Button>
        </div>
      </Card>
    {:else}
      <div class="flex flex-col gap-2 sm:flex-row">
        <Button disabled={busy} onclick={doMerge}>
          {needsCommitMessage ? 'Commit and merge' : 'Merge'}
        </Button>
        <Button variant="ghost" disabled={busy} onclick={() => (confirmingDiscard = true)}>Discard</Button>
      </div>
    {/if}
  </div>
{/if}
