<script lang="ts">
  import type { Snippet } from 'svelte'
  import DockStrip from './DockStrip.svelte'
  import type { DockAction, DockState, DockTab } from './dock'

  let {
    dock,
    selectedLabel = '',
    gateFailed = false,
    onAction,
    children,
  }: {
    dock: DockState
    /** The selected node's label, shown in select mode. */
    selectedLabel?: string
    gateFailed?: boolean
    onAction: (a: DockAction) => void
    children?: Snippet
  } = $props()

  const tabs: { id: DockTab; label: string }[] = [
    { id: 'inspect', label: 'Inspect' },
    { id: 'changes', label: 'Changes' },
    { id: 'files', label: 'Files' },
    { id: 'terminal', label: 'Terminal' },
    { id: 'preview', label: 'Preview' },
  ]

  // Dragging the left edge resizes; the width is clamped by the reducer.
  function startDrag(e: PointerEvent) {
    e.preventDefault()
    const startX = e.clientX
    const startW = dock.width
    const move = (ev: PointerEvent) => onAction({ type: 'resize', px: startW + (startX - ev.clientX) })
    const up = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
  }
</script>

{#if dock.size === 'collapsed'}
  <DockStrip {dock} {gateFailed} {onAction} />
{:else}
  <aside
    class="relative flex h-full min-h-0 shrink-0 flex-col border-l border-border bg-surface"
    style={dock.size === 'expanded' ? 'width: 65%' : `width: ${dock.width}px`}
    data-testid="dock"
    data-size={dock.size}
  >
    {#if dock.size === 'docked'}
      <div
        class="absolute inset-y-0 -left-1 z-10 w-2 cursor-col-resize"
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize dock"
        onpointerdown={startDrag}
      ></div>
    {/if}

    <div class="flex items-center gap-2 border-b border-border px-3 py-2 text-xs">
      {#if dock.mode === 'follow'}
        <span class="text-ok">●</span><span class="text-sub">following agent</span>
      {:else}
        <span class="text-violet">◆</span><span class="min-w-0 truncate text-sub" title={selectedLabel}>{selectedLabel || dock.selected}</span>
        <button
          type="button"
          class="ml-1 shrink-0 rounded border border-border px-1.5 py-0.5 text-muted hover:text-fg"
          onclick={() => onAction({ type: 'backToLive' })}>Back to live</button
        >
      {/if}
      <span class="ml-auto flex shrink-0 items-center gap-1">
        <button
          type="button"
          class="rounded px-1.5 py-0.5 hover:bg-hover {dock.pinned ? 'text-accent' : 'text-muted'}"
          aria-pressed={dock.pinned}
          title="Pin this tab (⌘P)"
          onclick={() => onAction({ type: 'togglePin' })}>{dock.pinned ? 'Pinned' : 'Pin'}</button
        >
        <button
          type="button"
          class="rounded px-1.5 py-0.5 text-muted hover:bg-hover hover:text-fg"
          title="Cycle dock size (⌘J)"
          aria-label="Dock size"
          onclick={() => onAction({ type: 'cycleSize' })}>{dock.size === 'docked' ? '⇤' : '⇥'}</button
        >
      </span>
    </div>

    <div class="flex items-center gap-1 border-b border-border px-2 py-1 text-xs" role="tablist">
      {#each tabs as t (t.id)}
        <button
          type="button"
          role="tab"
          aria-selected={dock.tab === t.id}
          class="relative rounded px-2 py-1 {dock.tab === t.id ? 'bg-raise text-fg' : 'text-muted hover:text-fg'}"
          onclick={() => onAction({ type: 'openTab', tab: t.id })}
        >
          {t.label}
          {#if t.id === 'changes' && dock.unseenChanges}<span class="ml-1 inline-block size-1.5 rounded-full bg-accent align-middle"></span>{/if}
          {#if t.id === 'terminal' && dock.unseenTerminal}<span class="ml-1 inline-block size-1.5 rounded-full bg-accent align-middle" data-testid="terminal-unread" title="New output"></span>{/if}
        </button>
      {/each}
    </div>

    <div class="min-h-0 flex-1 overflow-y-auto" role="tabpanel">
      {@render children?.()}
    </div>
  </aside>
{/if}
