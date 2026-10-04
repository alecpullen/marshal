<script lang="ts">
  import Button from '../ui/Button.svelte'
  import CommentThread from './CommentThread.svelte'
  import { isViewed, setViewed, toSplit, type FileDiff, type Hunk, type Line } from './unified'
  import type { ReviewComment } from '../api'
  import type { StackState } from '../stack'

  let {
    agentId,
    file,
    view,
    comments = [],
    stack,
    changedSince = {},
    onComment,
    onResolve = () => {},
    showViewed = true,
    badges,
    onviewed,
  }: {
    agentId: string
    file: FileDiff
    view: 'unified' | 'split'
    comments?: ReviewComment[]
    stack?: StackState
    /** Comment ids whose file changed since they were posted. */
    changedSince?: Record<string, boolean>
    /** Absent when the diff is read-only (the by-step view). */
    onComment?: (c: { path: string; line: number; side: 'old' | 'new'; quote: string; body: string }) => Promise<void> | void
    onResolve?: (id: string) => void
    showViewed?: boolean
    /** A badge to show above a hunk, with an optional link. */
    badges?: (h: Hunk) => { text: string; href?: string } | undefined
    onviewed?: () => void
  } = $props()

  let viewed = $state(false)
  // A changed diff resets the mark, which is stored against the hash.
  $effect(() => {
    viewed = showViewed && isViewed(agentId, file)
  })
  const counts = $derived(
    file.hunks.reduce(
      (a, h) => {
        for (const l of h.lines) if (l.kind === 'add') a.add++
        else if (l.kind === 'del') a.del++
        return a
      },
      { add: 0, del: 0 },
    ),
  )

  function toggleViewed(on: boolean) {
    viewed = on
    setViewed(agentId, file, on)
    onviewed?.()
  }

  const sideOf = (l: Line): 'old' | 'new' => (l.kind === 'del' ? 'old' : 'new')
  const numOf = (l: Line) => (l.kind === 'del' ? l.old : l.new) ?? 0
  const keyOf = (l: Line) => `${sideOf(l)}:${numOf(l)}`

  let composer = $state<{ key: string; hunk: Hunk; line: Line } | null>(null)
  let draft = $state('')
  let sending = $state(false)
  let error = $state('')

  function open(h: Hunk, l: Line) {
    if (!onComment) return
    composer = composer?.key === keyOf(l) ? null : { key: keyOf(l), hunk: h, line: l }
    draft = ''
    error = ''
  }

  // The clicked line and up to five lines before it, on the same side.
  function quoteFor(h: Hunk, l: Line): string {
    const idx = h.lines.indexOf(l)
    const side = sideOf(l)
    const picked = h.lines.slice(0, idx + 1).filter((x) => (side === 'old' ? x.kind !== 'add' : x.kind !== 'del'))
    return picked.slice(-6).map((x) => x.text).join('\n')
  }

  async function send() {
    if (!composer || !draft.trim() || !onComment) return
    sending = true
    error = ''
    try {
      await onComment({ path: file.path, line: numOf(composer.line), side: sideOf(composer.line), quote: quoteFor(composer.hunk, composer.line), body: draft.trim() })
      composer = null
      draft = ''
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    } finally {
      sending = false
    }
  }

  const threadsFor = (key: string) => comments.filter((c) => `${c.side}:${c.line}` === key)
  const rowBg = { add: 'bg-ok/10', del: 'bg-err/10', ctx: '' }
  const sign = { add: '+', del: '−', ctx: ' ' }
</script>

{#snippet extras(keys: string[])}
  {#each keys as key (key)}
    {#each threadsFor(key) as c (c.id)}
      {#if stack}<CommentThread comment={c} {agentId} {stack} changed={!!changedSince[c.id]} {onResolve} />{/if}
    {/each}
    {#if composer && composer.key === key}
      <div class="flex flex-col gap-2 border-y border-border bg-surface p-2" data-testid="composer">
        <textarea
          bind:value={draft}
          rows="3"
          placeholder="Comment for the agent"
          class="w-full rounded border border-border bg-bg p-2 text-sm"
          onkeydown={(e) => {
            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) void send()
          }}
        ></textarea>
        {#if error}<div class="text-xs text-err">{error}</div>{/if}
        <div class="flex gap-2">
          <Button class="min-h-0 px-3 py-1 text-xs" disabled={sending || !draft.trim()} onclick={send}>Send to agent</Button>
          <Button variant="ghost" class="min-h-0 px-3 py-1 text-xs" onclick={() => (composer = null)}>Cancel</Button>
        </div>
      </div>
    {/if}
  {/each}
{/snippet}

<section class="rounded-md border border-border bg-bg" data-testid="file-diff" data-path={file.path} id={`file-${encodeURIComponent(file.path)}`}>
  <header class="flex items-center gap-3 border-b border-border bg-surface px-3 py-1.5 text-xs">
    <span class="min-w-0 flex-1 truncate font-mono">{file.path}</span>
    <span class="text-ok">+{counts.add}</span><span class="text-err">−{counts.del}</span>
    {#if showViewed}
      <label class="flex items-center gap-1 text-muted">
        <input type="checkbox" checked={viewed} onchange={(e) => toggleViewed(e.currentTarget.checked)} aria-label="Viewed" /> Viewed
      </label>
    {/if}
  </header>
  {#if !viewed}
    <div class="overflow-x-auto font-mono text-xs">
      {#each file.hunks as h (h.newStart + ':' + h.oldStart)}
        <div class="flex items-center gap-2 bg-raise px-3 py-0.5 text-info">
          <span>@@ -{h.oldStart},{h.oldLines} +{h.newStart},{h.newLines} @@</span>
          {#if badges?.(h)}
            {@const b = badges(h)!}
            {#if b.href}<a class="rounded bg-warn/15 px-1.5 text-warn hover:underline" href={b.href}>{b.text}</a>{:else}<span class="rounded bg-warn/15 px-1.5 text-warn">{b.text}</span>{/if}
          {/if}
        </div>
        {#if view === 'unified'}
          {#each h.lines as l, i (i)}
            <div class="flex {rowBg[l.kind]}" data-kind={l.kind}>
              <button type="button" class="w-10 shrink-0 px-1 text-right text-dim hover:text-accent" aria-label={`Comment on old line ${l.old ?? ''}`} disabled={!onComment || l.kind !== 'del'} onclick={() => open(h, l)}>{l.old ?? ''}</button>
              <button type="button" class="w-10 shrink-0 px-1 text-right text-dim hover:text-accent" aria-label={`Comment on new line ${l.new ?? ''}`} disabled={!onComment || l.kind === 'del'} onclick={() => open(h, l)}>{l.new ?? ''}</button>
              <span class="w-4 shrink-0 text-center text-muted">{sign[l.kind]}</span>
              <span class="whitespace-pre">{l.text || ' '}</span>
            </div>
            {@render extras([keyOf(l)])}
          {/each}
        {:else}
          {#each toSplit(h) as row, i (i)}
            <div class="grid grid-cols-2" data-testid="split-row">
              {#each [row.left, row.right] as l, side (side)}
                <div class="flex border-l border-border {l ? rowBg[l.kind === 'ctx' ? 'ctx' : side === 0 ? 'del' : 'add'] : 'bg-surface/50'}">
                  {#if l}
                    <button type="button" class="w-10 shrink-0 px-1 text-right text-dim hover:text-accent" aria-label={`Comment on ${side === 0 ? 'old' : 'new'} line ${side === 0 ? l.old : l.new}`} disabled={!onComment} onclick={() => open(h, l)}>{side === 0 ? l.old : l.new}</button>
                    <span class="whitespace-pre px-1">{l.text || ' '}</span>
                  {/if}
                </div>
              {/each}
            </div>
            {@render extras([row.right ? keyOf(row.right) : '', row.left && row.left.kind === 'del' ? keyOf(row.left) : ''].filter(Boolean))}
          {/each}
        {/if}
      {/each}
    </div>
  {:else}
    <button type="button" class="w-full px-3 py-1 text-left text-xs text-muted" onclick={() => toggleViewed(false)}>Viewed — click to expand</button>
  {/if}
</section>
