<script lang="ts">
  import Button from '../ui/Button.svelte'
  import { renderMarkdown } from '../markdown'
  import type { ReviewComment } from '../api'
  import type { StackState } from '../stack'

  let {
    comment,
    agentId,
    stack,
    changed = false,
    onResolve,
  }: {
    comment: ReviewComment
    agentId: string
    stack: StackState
    /** The file's diff differs from when the comment was posted. */
    changed?: boolean
    onResolve: (id: string) => void
  } = $props()

  const sentMs = $derived(Date.parse(comment.sentAt))
  // The agent's answer is the first final message after the comment was sent.
  const reply = $derived.by(() => {
    if (Number.isNaN(sentMs)) return undefined
    let best: { id: string; at: number; content: string } | undefined
    for (const n of stack.nodes.values()) {
      const m = n.message
      if (n.kind !== 'final' || !m || m.at === undefined || m.at <= sentMs) continue
      if (!best || m.at < best.at) best = { id: n.id, at: m.at, content: m.content }
    }
    return best
  })
  const resolved = $derived(!!comment.resolvedAt && !comment.resolvedAt.startsWith('0001'))
  let open = $state(false)
  const when = (iso: string) => {
    const d = new Date(iso)
    return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }
</script>

{#if resolved && !open}
  <button type="button" class="flex w-full items-center gap-2 border-y border-border bg-surface px-3 py-1 text-left text-xs text-muted" data-testid="thread-resolved" onclick={() => (open = true)}>
    <span class="text-ok">✓ resolved</span><span class="truncate">{comment.body}</span>
  </button>
{:else}
  <div class="flex flex-col gap-2 border-y border-border bg-surface px-3 py-2 text-sm" data-testid="comment-thread">
    <div class="whitespace-pre-wrap">{comment.body}</div>
    <div class="text-xs text-muted">Sent to agent · {when(comment.sentAt)}</div>
    {#if reply}
      <div class="rounded border border-border bg-bg p-2">
        <div class="prose-sm text-sm">{@html renderMarkdown(reply.content)}</div>
        <a class="text-xs text-info hover:underline" href={`#chat/${agentId}?node=${encodeURIComponent(reply.id)}`}>Open in session</a>
      </div>
      {#if changed}<div class="text-xs text-info">File changed since this comment</div>{/if}
    {:else}
      <div class="text-xs text-muted">Waiting for the agent…</div>
    {/if}
    <div>
      {#if resolved}
        <Button variant="ghost" class="min-h-0 px-2 py-0.5 text-xs" onclick={() => (open = false)}>Collapse</Button>
      {:else}
        <Button variant="ghost" class="min-h-0 px-2 py-0.5 text-xs" onclick={() => onResolve(comment.id)}>Resolve</Button>
      {/if}
    </div>
  </div>
{/if}
