<script lang="ts">
  import Button from '../ui/Button.svelte'
  import Modal from '../ui/Modal.svelte'
  import Tag from '../ui/Tag.svelte'
  import FindingCard from './FindingCard.svelte'
  import { APIError, discardReviewDraft, editReviewDraft, errMessage, postReviewDraft, sendDraftToAuthor, type Finding, type RepoRow, type ReviewDraft } from '../api'
  import { forgeFileUrl, groupBySeverity, severityTone } from './model'

  let { draft, repo, onChange }: { draft: ReviewDraft; repo?: RepoRow; onChange: (d: ReviewDraft | null) => void } = $props()

  let editing = $state(false)
  let findings = $state<Finding[]>([])
  let summary = $state('')
  let confirming = $state(false)
  let sending = $state(false)
  let selected = $state<string[]>([])
  let busy = $state(false)
  let notice = $state('')
  let error = $state('')

  const isDraft = $derived(draft.status === 'draft')
  // A failed run has nothing to post or edit, but it can still be cleared.
  const isFailed = $derived(draft.status === 'failed')
  // Grouped from the saved draft even while editing, so changing a severity does not move the card under the cursor.
  const groups = $derived(groupBySeverity(draft.findings))
  const fileUrl = (f: Finding) => (f.path ? forgeFileUrl(repo, draft.headSha, f.path, f.line) : null)
  const evidence = (f: Finding) => (f.stepNode && draft.agentId ? `#chat/${encodeURIComponent(draft.agentId)}?node=${encodeURIComponent(f.stepNode)}` : null)

  function startEdit() {
    findings = draft.findings.map((f) => ({ ...f }))
    summary = draft.summary
    editing = true
    sending = false
  }
  // Edits are kept per finding id, so grouping by severity can reorder the cards without losing a field.
  const idx = (id: string) => findings.findIndex((f) => f.id === id)

  async function run(action: () => Promise<void>) {
    busy = true
    error = ''
    notice = ''
    try {
      await action()
    } catch (e) {
      error = errMessage(e)
    } finally {
      busy = false
    }
  }

  const save = () =>
    run(async () => {
      onChange(await editReviewDraft(draft.id, { findings, summary }))
      editing = false
    })
  const post = () =>
    run(async () => {
      confirming = false
      onChange(await postReviewDraft(draft.id))
    })
  const discard = () =>
    run(async () => {
      await discardReviewDraft(draft.id)
      onChange(null)
    })
  const send = () =>
    run(async () => {
      try {
        const { sent, skipped } = await sendDraftToAuthor(draft.id, selected)
        notice = `Sent ${sent} finding${sent === 1 ? '' : 's'} to the author agent${skipped ? ` (${skipped} skipped)` : ''}.`
        sending = false
        selected = []
      } catch (e) {
        if (e instanceof APIError && e.status === 404) error = 'No Marshal agent owns this PR'
        else throw e
      }
    })
  const toggle = (id: string, on: boolean) => (selected = on ? [...new Set([...selected, id])] : selected.filter((x) => x !== id))
</script>

<div class="flex flex-col gap-3" data-testid="draft-view">
  <div class="flex flex-wrap items-center gap-2">
    <h3 class="text-sm font-semibold">Review of PR #{draft.number}{draft.title ? `: ${draft.title}` : ''}</h3>
    <Tag tone={draft.status === 'posted' ? 'ok' : draft.status === 'draft' ? 'accent' : 'neutral'}>{draft.status}</Tag>
    {#if draft.reviewUrl}<a class="text-sm text-accent hover:underline" href={draft.reviewUrl} target="_blank" rel="noopener noreferrer">View review</a>{/if}
    {#if draft.agentId}<a class="text-sm text-accent hover:underline" href="#chat/{encodeURIComponent(draft.agentId)}">Reviewer session</a>{/if}
  </div>
  {#if draft.error}<p class="text-sm text-err" role="alert">{draft.error}</p>{/if}
  {#if error}<p class="rounded-md border border-attention bg-attention/10 p-2 text-sm" role="alert">{error}</p>{/if}
  {#if notice}<p class="text-sm" role="status">{notice}</p>{/if}

  {#if editing}
    <textarea aria-label="Summary" rows="3" bind:value={summary} class="w-full rounded border border-border bg-bg p-2 text-sm"></textarea>
  {:else if draft.summary}
    <p class="text-sm whitespace-pre-wrap">{draft.summary}</p>
  {/if}

  {#each groups as g (g.severity)}
    <section class="flex flex-col gap-2" aria-label="{g.severity} findings">
      <h4 class="text-xs tracking-wide text-muted uppercase"><Tag tone={severityTone(g.severity)}>{g.severity}</Tag> {g.findings.length}</h4>
      {#each g.findings as f (f.id)}
        {#if editing}
          <FindingCard bind:finding={findings[idx(f.id)]} {editing} fileUrl={fileUrl(f)} evidence={evidence(f)} />
        {:else}
          <FindingCard finding={f} fileUrl={fileUrl(f)} evidence={evidence(f)} selectable={sending} selected={selected.includes(f.id)} onSelect={(on) => toggle(f.id, on)} />
        {/if}
      {/each}
    </section>
  {:else}
    <p class="text-sm text-muted">No findings.</p>
  {/each}

  {#if isFailed}
    <div><Button variant="ghost" disabled={busy} onclick={discard}>Discard</Button></div>
  {:else if isDraft}
    <div class="flex flex-wrap gap-2">
      {#if editing}
        <Button disabled={busy} onclick={save}>Save edits</Button>
        <Button variant="ghost" disabled={busy} onclick={() => (editing = false)}>Cancel</Button>
      {:else if sending}
        <Button disabled={busy || selected.length === 0} onclick={send}>Send {selected.length || ''} to author</Button>
        <Button variant="ghost" disabled={busy} onclick={() => (sending = false)}>Cancel</Button>
      {:else}
        <Button disabled={busy} onclick={() => (confirming = true)}>Post</Button>
        <Button variant="ghost" disabled={busy} onclick={startEdit}>Edit</Button>
        <Button variant="ghost" disabled={busy || draft.findings.length === 0} onclick={() => { sending = true; selected = draft.findings.map((f) => f.id) }}>Send to author agent</Button>
        <Button variant="ghost" disabled={busy} onclick={discard}>Discard</Button>
      {/if}
    </div>
  {/if}
</div>

{#if confirming}
  <Modal title="Post this review?" description="The summary and findings are posted to PR #{draft.number} on the forge as you." onDismiss={() => { confirming = false }}>
    {#snippet footer()}
      <Button variant="ghost" onclick={() => (confirming = false)}>Cancel</Button>
      <Button disabled={busy} onclick={post}>Post review</Button>
    {/snippet}
    {#snippet children()}{/snippet}
  </Modal>
{/if}
