<script lang="ts">
  import { tick } from 'svelte'
  import DiffLines from '../DiffLines.svelte'
  import GateStrip from './GateStrip.svelte'
  import { diffTotals } from '../diff'
  import { APIError, errMessage, getDiff, getStepDiffs, type DiffFile, type GateRecord, type StepDiff } from '../api'
  import type { StackState } from '../stack'
  import { editCalls, latestEdit } from './nodes'
  import type { DockState } from './dock'

  let {
    agentId,
    sessionId,
    stack,
    dock,
    gate = undefined,
    changedFiles = 0,
  }: {
    agentId: string
    sessionId: string
    stack: StackState
    dock: DockState
    gate?: GateRecord
    changedFiles?: number
  } = $props()

  const edit = $derived(latestEdit(stack))
  // Changes whenever the agent makes another edit call.
  const editKey = $derived(edit ? `${edit.id}:${editCalls(edit).length}` : '')
  const editPath = $derived(edit ? editCalls(edit).at(-1)?.files?.[0] : undefined)

  let files = $state<DiffFile[]>([])
  let open = $state<Record<string, string>>({})
  let notIsolated = $state(false)
  let error = $state('')
  let rootEl = $state<HTMLElement | null>(null)

  async function loadFiles() {
    error = ''
    try {
      files = (await getDiff(agentId)).files ?? []
      notIsolated = false
    } catch (e) {
      // The bridge refuses a diff for an agent that works in the live checkout.
      if (e instanceof APIError && /isolated/i.test(errMessage(e))) notIsolated = true
      else error = errMessage(e)
    }
  }

  async function openFile(path: string, reveal = false) {
    try {
      const res = await getDiff(agentId, path)
      open = { ...open, [path]: res.diff ?? '' }
      if (reveal) {
        await tick()
        rootEl?.querySelector(`[data-path="${CSS.escape(path)}"]`)?.scrollIntoView?.({ block: 'nearest' })
      }
    } catch (e) {
      error = errMessage(e)
    }
  }

  async function toggle(path: string) {
    if (open[path] !== undefined) {
      const next = { ...open }
      delete next[path]
      open = next
    } else await openFile(path)
  }

  // Follow mode: reload the list and show the file just edited.
  $effect(() => {
    if (dock.mode !== 'follow') return
    editKey
    const path = editPath
    void loadFiles().then(() => {
      if (path) void openFile(path, true)
    })
  })

  let stepDiffs = $state<StepDiff[] | 'unsupported' | null>(null)
  $effect(() => {
    if (dock.mode !== 'select') return
    editKey
    getStepDiffs(sessionId)
      .then((r) => (stepDiffs = r))
      .catch((e) => (error = errMessage(e)))
  })

  // A tool selection filters to its step.
  const selectedStep = $derived.by(() => {
    const n = dock.selected ? stack.nodes.get(dock.selected) : undefined
    if (!n) return undefined
    return n.kind === 'tool' && n.parent ? stack.nodes.get(n.parent) : n
  })
  const shown = $derived.by(() => {
    if (!Array.isArray(stepDiffs) || !selectedStep) return []
    if (selectedStep.kind === 'step') return stepDiffs.filter((d) => d.stepNode === selectedStep.id)
    if (selectedStep.kind === 'task') return stepDiffs.filter((d) => d.taskNode === selectedStep.id)
    return []
  })

  const totals = $derived(diffTotals(files))
</script>

<div bind:this={rootEl} data-testid="changes">
  <GateStrip {agentId} {gate} />

  {#if error}<div class="m-3 rounded-md border border-danger bg-danger/10 p-2 text-xs">{error}</div>{/if}

  {#if dock.mode === 'select'}
    {#if stepDiffs === 'unsupported'}
      <p class="p-3 text-xs text-muted">Per-step changes need a newer agent.</p>
    {:else if shown.length === 0}
      <p class="p-3 text-xs text-muted">{stepDiffs === null ? 'Loading…' : 'No file changes for this selection.'}</p>
    {:else}
      <div class="flex flex-col gap-3 p-3">
        {#each shown as d (d.stepNode)}
          <section data-testid="step-diff">
            <h4 class="mb-1 text-xs font-medium">{d.headline}</h4>
            <div class="mb-1 font-mono text-[11px] text-muted">{d.files.join(', ')}</div>
            <DiffLines diff={d.diff} />
          </section>
        {/each}
      </div>
    {/if}
  {:else if notIsolated}
    <p class="p-3 text-xs text-muted">
      Changes are tracked for isolated agents.
      {#if changedFiles > 0}<span class="text-sub">{changedFiles} changed file{changedFiles === 1 ? '' : 's'} so far.</span>{/if}
    </p>
  {:else}
    <div class="flex items-center justify-between px-3 py-2 text-xs">
      <span>
        {totals.files} file{totals.files === 1 ? '' : 's'}
        <span class="text-running">+{totals.added}</span>
        <span class="text-danger">-{totals.removed}</span>
      </span>
      <button type="button" class="text-muted hover:text-fg" onclick={loadFiles}>Reload</button>
    </div>
    {#if files.length === 0}<p class="px-3 pb-3 text-xs text-muted">No changes yet.</p>{/if}
    <ul class="flex flex-col gap-1 px-2 pb-3">
      {#each files as f (f.path)}
        <li data-path={f.path}>
          <button
            type="button"
            class="flex w-full items-center justify-between gap-3 rounded px-2 py-1.5 text-left text-xs hover:bg-hover {f.path === editPath ? 'bg-raise' : ''}"
            onclick={() => toggle(f.path)}
          >
            <span class="min-w-0 truncate font-mono">{f.path}</span>
            <span class="shrink-0"><span class="text-running">+{f.added}</span> <span class="text-danger">-{f.removed}</span></span>
          </button>
          {#if open[f.path] !== undefined}
            {#if open[f.path]}<DiffLines diff={open[f.path]} />{:else}<pre class="rounded bg-bg p-2 text-xs">(no textual diff)</pre>{/if}
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</div>
