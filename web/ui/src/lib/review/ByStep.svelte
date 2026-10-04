<script lang="ts">
  import { onMount } from 'svelte'
  import Tag from '../ui/Tag.svelte'
  import FileDiffView from './FileDiff.svelte'
  import { getStepDiffs, errMessage, type StepDiff } from '../api'
  import { hunkKey, parseUnified, rewrittenBy, type FileDiff } from './unified'
  import type { StackState } from '../stack'

  let {
    agentId,
    sessionId,
    stack,
    reloadKey = 0,
    onUnsupported,
  }: {
    agentId: string
    sessionId: string
    stack: StackState
    /** Bumped when the agent's work changed, to refetch. */
    reloadKey?: number
    onUnsupported: () => void
  } = $props()

  interface ParsedStep extends StepDiff {
    parsed: FileDiff[]
  }
  let steps = $state<ParsedStep[]>([])
  let loaded = $state(false)
  let error = $state('')

  async function load() {
    try {
      const r = await getStepDiffs(sessionId)
      if (r === 'unsupported') {
        onUnsupported()
        return
      }
      steps = r.map((s) => ({ ...s, parsed: parseUnified(s.diff) }))
      error = ''
    } catch (e) {
      error = errMessage(e)
    } finally {
      loaded = true
    }
  }
  // The first run is the initial load; later bumps of reloadKey refetch.
  let first = true
  $effect(() => {
    void reloadKey
    if (first) {
      first = false
      return
    }
    void load()
  })
  onMount(load)

  const rewritten = $derived(rewrittenBy(steps.map((s) => ({ stepNode: s.stepNode, files: s.parsed }))))
  const headlineOf = (id: string) => steps.find((s) => s.stepNode === id)?.headline ?? id

  // Tasks in order of first appearance, then steps within each.
  const groups = $derived.by(() => {
    const out: { taskNode: string; steps: ParsedStep[] }[] = []
    for (const s of steps) {
      const key = s.taskNode ?? ''
      let g = out.find((x) => x.taskNode === key)
      if (!g) out.push((g = { taskNode: key, steps: [] }))
      g.steps.push(s)
    }
    return out
  })
  const taskOf = (id: string) => (id ? stack.nodes.get(id)?.task : undefined)
  const stepOf = (id: string) => stack.nodes.get(id)?.step
</script>

{#if error}
  <div class="rounded border border-danger p-3 text-sm">{error}</div>
{:else if !loaded}
  <div class="text-sm text-muted">Loading…</div>
{:else if groups.length === 0}
  <div class="text-sm text-muted">No step has changed files yet.</div>
{:else}
  <div class="flex flex-col gap-6" data-testid="by-step">
    {#each groups as g (g.taskNode)}
      {@const t = taskOf(g.taskNode)}
      <section class="flex flex-col gap-3">
        <h2 class="text-sm font-semibold">
          {#if t}<span class="text-muted">{t.index ?? '·'}/{t.total ?? '·'}</span> {t.content}{:else}No task{/if}
        </h2>
        {#each g.steps as s (s.stepNode)}
          {@const st = stepOf(s.stepNode)}
          <div class="flex flex-col gap-2 border-l-2 border-border pl-3">
            <div class="flex items-center gap-2 text-sm">
              {#if st?.owner || st?.role}<Tag role={st.role}>{st.owner || st.role}</Tag>{/if}
              <span class="min-w-0 flex-1 truncate">{s.headline}</span>
              <a class="text-xs text-info hover:underline" href={`#chat/${agentId}?node=${encodeURIComponent(s.stepNode)}`}>Open step</a>
            </div>
            {#each s.parsed as f (f.path)}
              <FileDiffView
                {agentId}
                file={f}
                view="unified"
                showViewed={false}
                badges={(h) => {
                  const later = rewritten.get(hunkKey(s.stepNode, f.path, h.newStart))
                  return later ? { text: `rewritten in ${headlineOf(later)}`, href: `#chat/${agentId}?node=${encodeURIComponent(later)}` } : undefined
                }}
              />
            {/each}
          </div>
        {/each}
      </section>
    {/each}
  </div>
{/if}
