<script lang="ts">
  import { glyph } from '../glyphs'
  import type { WireTask } from '../stack'
  import { compactDuration, plural } from './format'

  let {
    task,
    folded,
    live,
    now,
    onToggle,
  }: { task: WireTask; folded: boolean; live: boolean; now: number; onToggle?: () => void } = $props()

  const mark = $derived(
    task.unresolvedFailure ? glyph.Error : task.status === 'completed' ? glyph.OK : task.status === 'in_progress' || live ? glyph.Running : glyph.Ambient,
  )
  const tone = $derived(task.unresolvedFailure ? 'text-err' : task.status === 'completed' ? 'text-ok' : 'text-accent')
  const index = $derived(task.index && task.total ? `${task.index}/${task.total}` : '')
  const elapsed = $derived.by(() => {
    if (task.workMs) return compactDuration(task.workMs)
    if (live && task.startedAt) return compactDuration(Math.max(0, now - task.startedAt))
    return ''
  })
  const meta = $derived(
    [plural(task.steps, 'step'), task.edits > 0 ? `${glyph.Edit} ${plural(task.edits, 'edit')}` : plural(task.tools, 'tool'), elapsed]
      .filter(Boolean)
      .join(' · '),
  )
</script>

<button
  class="flex w-full cursor-pointer items-baseline gap-2 border-t border-line py-1.5 text-left text-sm {task.dropped ? 'opacity-60' : ''}"
  onclick={onToggle}
  aria-expanded={!folded}
>
  <span class={tone}>{mark}</span>
  {#if index}<span class="font-mono text-xs text-muted">{index}</span>{/if}
  <span class="min-w-0 truncate font-medium">{task.content}</span>
  {#if task.dropped}<span class="text-xs text-muted">dropped</span>{/if}
  <span class="ml-auto shrink-0 font-mono text-xs text-muted">{meta}</span>
  {#if folded}<span class="text-muted">{glyph.DisclosureCollapsed}</span>{/if}
</button>
