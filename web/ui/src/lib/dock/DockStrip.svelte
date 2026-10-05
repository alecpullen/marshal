<script lang="ts">
  import { glyph } from '../glyphs'
  import type { DockAction, DockState, DockTab } from './dock'

  let {
    dock,
    gateFailed = false,
    onAction,
  }: { dock: DockState; gateFailed?: boolean; onAction: (a: DockAction) => void } = $props()

  const tabs: { id: DockTab; label: string; icon: string }[] = [
    { id: 'inspect', label: 'Inspect', icon: '◎' },
    { id: 'changes', label: 'Changes', icon: glyph.Edit },
    { id: 'files', label: 'Files', icon: glyph.File },
    { id: 'terminal', label: 'Terminal', icon: glyph.Shell },
    { id: 'preview', label: 'Preview', icon: '◫' },
  ]
</script>

<div class="flex h-full w-[46px] flex-col items-center gap-1 border-l border-border bg-surface py-2" data-testid="dock-strip">
  {#each tabs as t (t.id)}
    <button
      type="button"
      class="relative flex size-9 items-center justify-center rounded text-sm text-muted hover:bg-hover hover:text-fg"
      title={t.label}
      aria-label={t.label}
      onclick={() => onAction({ type: 'openTab', tab: t.id })}
    >
      {t.icon}
      {#if t.id === 'changes' && gateFailed}
        <span class="absolute top-1.5 right-1.5 size-2 rounded-full bg-err" data-testid="gate-dot" title="Gate failed"></span>
      {:else if t.id === 'changes' && dock.unseenChanges}
        <span class="absolute top-1.5 right-1.5 size-2 rounded-full bg-accent" data-testid="unseen-dot" title="New changes"></span>
      {:else if t.id === 'terminal' && dock.unseenTerminal}
        <span class="absolute top-1.5 right-1.5 size-2 rounded-full bg-accent" data-testid="terminal-unread" title="New output"></span>
      {/if}
    </button>
  {/each}
</div>
