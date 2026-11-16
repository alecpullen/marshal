<script lang="ts">
  import { cn } from '../utils'

  let {
    tabs,
    value,
    onchange,
    label = '',
  }: { tabs: { value: string; label: string }[]; value: string; onchange: (value: string) => void; label?: string } = $props()

  /*
    The element declares the ARIA tabs pattern, so it honours the pattern's
    keyboard model: ArrowLeft/ArrowRight move the selection (wrapping at the
    ends), Home and End jump, and focus follows the newly selected tab. Every
    tab stays in the normal tab order — no roving tabindex — because views
    outside this branch count on it.
  */
  function onKeydown(e: KeyboardEvent) {
    const current = tabs.findIndex((t) => t.value === value)
    if (current < 0) return
    let next: number
    if (e.key === 'ArrowRight') next = (current + 1) % tabs.length
    else if (e.key === 'ArrowLeft') next = (current - 1 + tabs.length) % tabs.length
    else if (e.key === 'Home') next = 0
    else if (e.key === 'End') next = tabs.length - 1
    else return
    e.preventDefault()
    const tab = tabs[next]
    if (tab.value !== value) onchange(tab.value)
    ;(e.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('[role="tab"]')[next]?.focus()
  }
</script>

<!-- The tablist itself never takes focus — its tab buttons do, and they keep
     their places in the normal tab order, so no roving tabindex on either. -->
<!-- svelte-ignore a11y_interactive_supports_focus -->
<div class="flex flex-wrap gap-1 border-b border-border text-sm" role="tablist" aria-label={label} onkeydown={onKeydown}>
  {#each tabs as t (t.value)}
    <button
      type="button"
      role="tab"
      aria-selected={t.value === value}
      class={cn('px-3 py-1.5', t.value === value ? 'border-b-2 border-accent text-fg' : 'text-muted hover:text-fg')}
      onclick={() => onchange(t.value)}
    >
      {t.label}
    </button>
  {/each}
</div>
