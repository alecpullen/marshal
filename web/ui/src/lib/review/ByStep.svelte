<script lang="ts">
  import { onMount } from 'svelte'
  import Tag from '../ui/Tag.svelte'
  import FileDiffView from './FileDiff.svelte'
  import { getStepDiffs, errMessage, type StepDiff } from '../api'
  import { hunkKey, parseUnified, rewrittenBy, type FileDiff } from './unified'
  import { orderedNodes } from '../dock/nodes'
  import type { StackState, WireTodoLabel } from '../stack'

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

  /*
    The todo divider in force at each beat. The wire labels only the beat
    where work moves on, so a step belongs to the newest label at or before
    it in display order. Each transition gets its own group key, so a todo
    the agent returns to later does not merge back into its first group.
  */
  const labelAt = $derived.by(() => {
    const out = new Map<string, { key: string; label: WireTodoLabel }>()
    let n = 0
    for (const root of stack.roots) {
      // The label resets per turn on the wire, so it does here too: a turn
      // that never labels a beat must not inherit the previous turn's todo.
      let cur: { key: string; label: WireTodoLabel } | undefined
      for (const node of orderedNodes({ nodes: stack.nodes, roots: [root] })) {
        if (node.kind !== 'step' || !node.step) continue
        if (node.step.todo) cur = { key: `todo:${n++}:${node.step.todo.id}`, label: node.step.todo }
        if (cur) out.set(node.id, cur)
      }
    }
    return out
  })

  // Steps in order of first appearance, grouped under the todo they ran for.
  const groups = $derived.by(() => {
    const out: { key: string; label?: WireTodoLabel; steps: ParsedStep[] }[] = []
    for (const s of steps) {
      const at = labelAt.get(s.stepNode)
      const key = at?.key ?? ''
      let g = out.find((x) => x.key === key)
      if (!g) out.push((g = { key, label: at?.label, steps: [] }))
      g.steps.push(s)
    }
    return out
  })
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
    {#each groups as g (g.key)}
      <section class="flex flex-col gap-3">
        <h2 class="text-sm font-semibold">
          {#if g.label}<span class="text-muted">{g.label.index}/{g.label.total}</span> {g.label.content}{:else}<span class="text-muted">No todo label</span>{/if}
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
