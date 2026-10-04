<script lang="ts">
  import { tick } from 'svelte'
  import { createFileTree, type DirEntry } from './fileTree'
  import { errMessage, readFile, type FileView } from '../api'
  import type { StackState } from '../stack'
  import { nodeFile } from './nodes'
  import type { DockState } from './dock'

  export interface OpenRequest {
    path: string
    line?: number
    /** Bumped for every request so the same file can be opened again. */
    seq: number
  }

  let {
    agentId,
    stack,
    dock,
    request = undefined,
  }: { agentId: string; stack: StackState; dock: DockState; request?: OpenRequest } = $props()

  // One tree per agent for the life of this tab.
  // svelte-ignore state_referenced_locally
  const tree = createFileTree(agentId)

  let current = $state('')
  let view = $state<FileView | 'unsupported' | null>(null)
  let error = $state('')
  let line = $state<number | undefined>(undefined)
  let viewerEl = $state<HTMLElement | null>(null)

  async function open(path: string, at?: number) {
    current = path
    line = at
    view = null
    error = ''
    void tree.reveal(path)
    try {
      view = await readFile(agentId, path)
    } catch (e) {
      error = errMessage(e)
      return
    }
    if (at) {
      await tick()
      viewerEl?.querySelector(`[data-line="${at}"]`)?.scrollIntoView?.({ block: 'center' })
    }
  }

  let lastSeq = -1
  $effect(() => {
    if (request && request.seq !== lastSeq) {
      lastSeq = request.seq
      void open(request.path, request.line)
    }
  })

  // In select mode a node that points at a file shows it.
  let lastSelected: string | undefined
  $effect(() => {
    if (dock.mode !== 'select' || !dock.selected || dock.selected === lastSelected) return
    lastSelected = dock.selected
    const n = stack.nodes.get(dock.selected)
    const f = n && nodeFile(n)
    if (f) void open(f.path, f.line)
  })

  $effect(() => {
    void tree.reveal('') // the root, opened once
  })

  const lines = $derived(view && view !== 'unsupported' && !view.binary ? view.content.split('\n') : [])
  const join = (dir: string, name: string) => (dir ? `${dir}/${name}` : name)
</script>

{#snippet dir(path: string, depth: number)}
  {@const st = $tree.dirs.get(path)}
  {#if st === 'loading'}
    <div class="px-2 py-0.5 text-muted" style="padding-left: {depth * 12 + 8}px">…</div>
  {:else if st instanceof Error}
    <div class="px-2 py-0.5 text-err" style="padding-left: {depth * 12 + 8}px">{st.message}</div>
  {:else if st === 'unsupported'}
    <div class="px-2 py-0.5 text-muted">Needs a newer agent.</div>
  {:else if Array.isArray(st)}
    {#each [...st].sort((a: DirEntry, b: DirEntry) => Number(b.dir) - Number(a.dir) || a.name.localeCompare(b.name)) as e (e.name)}
      {@const p = join(path, e.name)}
      <button
        type="button"
        class="flex w-full items-center gap-1 truncate rounded py-0.5 pr-2 text-left hover:bg-hover {p === current ? 'bg-raise text-fg' : 'text-sub'}"
        style="padding-left: {depth * 12 + 8}px"
        onclick={() => (e.dir ? tree.toggle(p) : open(p))}
      >
        <span class="w-3 shrink-0 text-muted">{e.dir ? ($tree.open.has(p) ? '▿' : '▹') : ''}</span>
        <span class="truncate font-mono">{e.name}</span>
      </button>
      {#if e.dir && $tree.open.has(p)}{@render dir(p, depth + 1)}{/if}
    {/each}
  {/if}
{/snippet}

<div class="flex h-full min-h-0 flex-col text-xs" data-testid="files">
  <div class="max-h-[40%] shrink-0 overflow-y-auto border-b border-border py-1" role="tree">{@render dir('', 0)}</div>
  <div class="min-h-0 flex-1 overflow-auto" bind:this={viewerEl}>
    {#if error}
      <p class="p-3 text-err">{error}</p>
    {:else if !current}
      <p class="p-3 text-muted">Pick a file to view it.</p>
    {:else if view === null}
      <p class="p-3 text-muted">Loading…</p>
    {:else if view === 'unsupported'}
      <p class="p-3 text-muted">Needs a newer agent.</p>
    {:else if view.binary}
      <p class="p-3 text-muted">{current} is a binary file ({view.size} bytes).</p>
    {:else}
      <div class="border-b border-border px-3 py-1 font-mono text-muted">{current}</div>
      {#if view.truncated}<p class="px-3 py-1 text-warn">Showing the first part of a large file.</p>{/if}
      <pre class="font-mono">{#each lines as l, i (i)}<span class="flex whitespace-pre {line === i + 1 ? 'bg-accent/15' : ''}" data-line={i + 1}><span class="w-10 shrink-0 pr-2 text-right text-dim select-none">{i + 1}</span>{l}</span>{/each}</pre>
    {/if}
  </div>
</div>
