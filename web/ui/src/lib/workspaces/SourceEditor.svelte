<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import type { WSDiag } from '../api'
  import { diagByLine, lineOfOffset } from './sourceEditor'

  /*
    A dependency-free editor: a transparent textarea laid over a mirror that
    paints the highlights, so the caret and selection are the browser's own.
    Both share a font and line height; scroll is copied from the textarea to
    the mirror and the gutter.
  */
  let {
    value,
    diagnostics = [],
    highlight,
    flash = [],
    onChange,
    onCursorLine,
  }: {
    value: string
    diagnostics?: WSDiag[]
    /** 1-based inclusive line range to tint. */
    highlight?: { start: number; end: number }
    /** Lines to flash briefly after a layer edit rewrote them. */
    flash?: number[]
    onChange: (value: string) => void
    onCursorLine?: (line: number) => void
  } = $props()

  const DEBOUNCE_MS = 400

  // svelte-ignore state_referenced_locally
  let text = $state(value)
  let ta = $state<HTMLTextAreaElement | null>(null)
  let mirror = $state<HTMLElement | null>(null)
  let gutter = $state<HTMLElement | null>(null)
  let cursorLine = $state(1)
  let timer: ReturnType<typeof setTimeout> | undefined

  // A server rewrite (layer patch, parse) replaces the text; typing alone never changes the prop.
  $effect(() => {
    const v = value
    if (v !== untrack(() => text)) {
      clearTimeout(timer)
      text = v
    }
  })

  const lines = $derived(text.split('\n'))
  const byLine = $derived(diagByLine(diagnostics))
  const flashSet = $derived(new Set(flash))
  const cursorDiags = $derived(byLine.get(cursorLine) ?? [])

  function input() {
    clearTimeout(timer)
    timer = setTimeout(() => onChange(text), DEBOUNCE_MS)
  }

  function reportCursor() {
    if (!ta) return
    const line = lineOfOffset(ta.value, ta.selectionStart)
    if (line === cursorLine) return
    cursorLine = line
    onCursorLine?.(line)
  }

  function sync() {
    if (!ta) return
    if (mirror) {
      mirror.scrollTop = ta.scrollTop
      mirror.scrollLeft = ta.scrollLeft
    }
    if (gutter) gutter.scrollTop = ta.scrollTop
  }

  onMount(() => {
    const onSel = () => {
      if (document.activeElement === ta) reportCursor()
    }
    document.addEventListener('selectionchange', onSel)
    return () => {
      document.removeEventListener('selectionchange', onSel)
      clearTimeout(timer)
    }
  })

  const sevClass = (d: WSDiag[]) => (d[0].severity === 'error' ? 'text-err' : 'text-warn')
  const inHighlight = (n: number) => !!highlight && n >= highlight.start && n <= highlight.end
</script>

<div class="flex flex-col gap-1">
  <div class="flex h-80 overflow-hidden rounded-md border border-border bg-bg font-mono text-xs leading-5" data-testid="source-editor">
    <div bind:this={gutter} class="w-12 shrink-0 overflow-hidden border-r border-border bg-surface py-2 text-right text-muted select-none" aria-hidden="true">
      {#each lines as _, i (i)}
        {@const ds = byLine.get(i + 1)}
        <div class="flex h-5 items-center justify-end gap-1 pr-2" title={ds?.map((d) => d.message).join('\n')} data-testid={ds ? 'gutter-diag' : undefined} data-line={i + 1}>
          {#if ds}<span class={sevClass(ds)}>●</span>{/if}{i + 1}
        </div>
      {/each}
    </div>
    <div class="relative min-w-0 flex-1">
      <div bind:this={mirror} class="pointer-events-none absolute inset-0 overflow-hidden py-2" aria-hidden="true" data-testid="mirror">
        {#each lines as l, i (i)}
          <div
            class="h-5 px-2 whitespace-pre {inHighlight(i + 1) ? 'bg-accent/10' : ''} {flashSet.has(i + 1) ? 'bg-ok/25' : ''} {byLine.has(i + 1) ? 'underline decoration-wavy ' + (byLine.get(i + 1)![0].severity === 'error' ? 'decoration-err' : 'decoration-warn') : ''}"
            data-line={i + 1}
            data-highlight={inHighlight(i + 1) ? '' : undefined}
            data-flash={flashSet.has(i + 1) ? '' : undefined}
          >{l || ' '}</div>
        {/each}
      </div>
      <textarea
        bind:this={ta}
        bind:value={text}
        class="absolute inset-0 h-full w-full resize-none overflow-auto bg-transparent px-2 py-2 whitespace-pre text-transparent caret-fg outline-none selection:bg-accent/30"
        spellcheck="false"
        wrap="off"
        aria-label="Workspace source"
        oninput={input}
        onscroll={sync}
        onclick={reportCursor}
        onkeyup={reportCursor}
      ></textarea>
    </div>
  </div>
  <div class="min-h-5 text-xs" data-testid="cursor-diag">
    {#each cursorDiags as d (d.message)}
      <span class={d.severity === 'error' ? 'text-err' : 'text-warn'}>Line {d.line}: {d.message}</span>
    {/each}
  </div>
</div>
