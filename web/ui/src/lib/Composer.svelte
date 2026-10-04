<script lang="ts">
  import { BudgetError, budgetMessage } from './api'

  interface Props {
    busy: boolean
    onSend: (text: string) => void | Promise<void>
    onCancel: () => void | Promise<void>
  }

  let { busy, onSend, onCancel }: Props = $props()
  let text = $state('')
  let sending = $state(false)
  let notice = $state('')

  async function submit() {
    const t = text.trim()
    if (!t || sending) return
    sending = true
    notice = ''
    try {
      await onSend(t)
      text = ''
    } catch (e) {
      // A budget stop is a decision to show; the text stays so it can be resent.
      if (e instanceof BudgetError) notice = budgetMessage(e)
      else throw e
    } finally {
      sending = false
    }
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      submit()
    }
  }
</script>

{#if notice}
  <div class="px-4 py-1 text-xs text-warn" role="alert">{notice}</div>
{/if}
<div class="composer">
  <textarea bind:value={text} placeholder={busy ? 'Steer the turn…' : 'Ask Marshal…'} onkeydown={keydown} rows="2"></textarea>
  {#if busy}
    <button onclick={onCancel} class="cancel">Cancel</button>
  {/if}
  <button onclick={submit} disabled={!text.trim() || sending}>
    {busy ? 'Steer' : 'Send'}
  </button>
</div>

<style>
  .composer {
    display: flex;
    gap: 0.5rem;
    padding: 0.75rem 1rem;
    border-top: 1px solid var(--color-border);
    background: var(--color-surface);
  }
  textarea {
    background: var(--color-bg);
    color: var(--color-fg);
    flex: 1;
    resize: none;
    padding: 0.6rem;
    border: 1px solid var(--color-border);
    border-radius: 8px;
    font: inherit;
  }
  button {
    padding: 0 1rem;
    border: 1px solid var(--color-accent);
    border-radius: 8px;
    background: var(--color-accent);
    color: var(--color-bg);
    font: inherit;
    cursor: pointer;
  }
  button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .cancel {
    background: var(--color-surface);
    color: var(--color-fg);
    border-color: var(--color-border);
  }
</style>