<script lang="ts">
  import Card from './ui/Card.svelte'
  import Button from './ui/Button.svelte'
  import DiffView from './DiffView.svelte'
  import GateResult from './GateResult.svelte'
  import { APIError, discardAgent, exitAgent, getDiff, listAgents, mergeAgent, patchUrl, type ExitResult, type MergeResult } from './api'
  import { diffTotals, mergeRefusalMessage } from './diff'
  import { exitDestination } from './exit'

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
  let sourceKind = $state('')
  let readOnly = $state(false)
  let notice = $state<string | null>(null)
  let needsCommitMessage = $state(false)
  let commitMessage = $state('')
  let confirmingDiscard = $state(false)
  let busy = $state(false)
  let totals = $state({ files: 0, added: 0, removed: 0 })
  let pushResult = $state<ExitResult | null>(null)

  // The destination follows the agent's source and is never a user choice.
  const destination = $derived(exitDestination({ sourceKind, readOnly }))

  // Resolve this agent's own isolation state; Chat has only the session id.
  $effect(() => {
    listAgents()
      .then((agents) => {
        const me = agents.find((a) => a.id === agentId)
        isolated = me?.isolated ?? false
        branch = me?.branch ?? ''
        sourceKind = me?.sourceKind ?? ''
        readOnly = me?.readOnly ?? false
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

  async function doPush(override?: { reason: string }) {
    busy = true
    notice = null
    try {
      pushResult = await exitAgent(agentId, {
        commitMessage: commitMessage.trim() || 'agent work',
        ...(override ? { override } : {}),
      })
    } catch (e) {
      notice = e instanceof Error ? e.message : String(e)
    } finally {
      busy = false
    }
  }
</script>

{#if destination === 'merge'}
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
{:else if destination === 'push'}
  <div class="flex flex-col gap-3">
    {#if pushResult?.verify}
      <GateResult result={pushResult.verify} onOverride={(reason) => doPush({ reason })} />
    {/if}

    {#if pushResult?.blocked}
      <div class="rounded-md border border-attention bg-attention/10 p-3 text-sm">Push was blocked.</div>
    {/if}

    {#if branch}
      <div class="text-sm text-muted">
        Branch <code class="font-mono">{branch}</code>
      </div>
    {/if}

    {#if pushResult?.prUrl}
      <a
        href={pushResult.prUrl}
        target="_blank"
        rel="noreferrer"
        class="inline-flex min-h-11 items-center justify-center gap-2 rounded-md bg-accent px-3 py-2 text-sm font-medium text-bg transition hover:opacity-90"
      >
        Open pull request
      </a>
    {/if}

    {#if notice}
      <div class="rounded-md border border-attention bg-attention/10 p-3 text-sm">{notice}</div>
    {/if}

    <div>
      <Button disabled={busy} onclick={() => doPush()}>Push</Button>
    </div>
  </div>
{:else if destination === 'patch'}
  <div class="flex flex-col gap-3">
    <div class="text-sm text-muted">
      This agent is read-only. Download its changes as a patch to apply elsewhere.
    </div>
    <div>
      <a
        href={patchUrl(agentId)}
        class="inline-flex min-h-11 items-center justify-center gap-2 rounded-md bg-accent px-3 py-2 text-sm font-medium text-bg transition hover:opacity-90"
      >
        Download patch
      </a>
    </div>
  </div>
{/if}
