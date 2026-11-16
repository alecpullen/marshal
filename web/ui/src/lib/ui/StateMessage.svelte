<script lang="ts">
  import Button from './Button.svelte'

  /**
   * The shared empty/blocked/error message (design C5): a title, an optional
   * body, and at most one way forward.
   *
   * The role is tied to the tone rather than chosen per call site, so a
   * failure is always announced and an empty list never interrupts. `status`
   * is a polite live region; `alert` is assertive, and only a genuine failure
   * earns it.
   */
  export interface StateMessageProps {
    tone?: 'neutral' | 'info' | 'success' | 'warn' | 'error'
    title: string
    body?: string
    actionLabel?: string
    onAction?: () => void
  }

  let { tone = 'neutral', title, body, actionLabel, onAction }: StateMessageProps = $props()

  const role = $derived(tone === 'error' ? 'alert' : 'status')

  const tones = {
    neutral: 'border-border',
    info: 'border-info/40',
    success: 'border-ok/40',
    warn: 'border-warn/40',
    error: 'border-danger/50',
  }

  const marks = {
    neutral: '',
    info: 'text-info',
    success: 'text-ok',
    warn: 'text-warn',
    error: 'text-danger',
  }
</script>

<div class="flex flex-col items-start gap-2 rounded-lg border bg-surface p-4 {tones[tone]}" role={role} data-tone={tone}>
  <p class="text-sm font-medium {marks[tone]}">{title}</p>
  {#if body}<p class="text-sm text-muted">{body}</p>{/if}
  {#if actionLabel && onAction}
    <Button variant="ghost" onclick={onAction}>{actionLabel}</Button>
  {/if}
</div>
