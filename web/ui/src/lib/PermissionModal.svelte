<script lang="ts">
  import type { PendingPermission } from './store.js'
  import type { Decision } from './api.js'
  import { accessLabel, parseAccessState, type ModeAccessChoice } from './interaction.js'
  import Modal from './ui/Modal.svelte'
  import Button from './ui/Button.svelte'

  /*
    One decision dialog for both kinds of request.

    An ordinary tool approval is exact-only: it allows the presented action
    and grants no standing capability, so it never shows a scope editor. A
    typed capability request offers exactly the missing bundle — Act, System,
    or both — and never Auto. The answer carries the original identity and the
    committed tuple it was presented with, so a stale presentation cannot be
    replayed into a different grant.
  */
  interface Props {
    permission: PendingPermission
    onResolve: (d: Decision) => void | Promise<void>
    onDeny: () => void | Promise<void>
  }

  let { permission, onResolve, onDeny }: Props = $props()
  let edit = $state('')

  const capability = $derived(permission.modeAccess ?? null)
  const current = $derived(capability ? parseAccessState(capability.current) : null)
  const reason = $derived(permission.reason ?? capability?.reason ?? '')
  const requested = $derived.by(() => {
    if (!capability) return ''
    const parts: string[] = []
    if (capability.missingEditing) parts.push('Act')
    if (capability.missingSystem) parts.push('System')
    return parts.join(' + ')
  })

  async function approve() {
    await onResolve({ approved: true, edited: edit || undefined })
  }

  async function allowCapability() {
    if (!capability) return
    const choice: ModeAccessChoice = {
      requestId: capability.requestId,
      sessionId: capability.sessionId,
      current: capability.current,
    }
    // Agent-requested Act is normal Act, never Auto. A System-only approval
    // carries no mode, so it preserves the committed mode (and Auto).
    if (capability.missingEditing) choice.mode = 'act'
    await onResolve({ approved: true, modeAccess: choice })
  }
</script>

<Modal title="Permission request" onDismiss={onDeny}>
  {#if reason}
    <p class="text-sm text-muted">{reason}</p>
  {/if}

  <p class="text-sm">Tool: <strong class="font-medium">{permission.toolName}</strong></p>

  {#if permission.command}
    <pre
      class="overflow-x-auto rounded-md border border-border bg-bg p-3 font-mono text-xs whitespace-pre-wrap">{permission.command}</pre>
  {/if}

  {#if permission.diff}
    <pre
      class="max-h-64 overflow-auto rounded-md border border-border bg-bg p-3 font-mono text-xs whitespace-pre-wrap">{permission.diff}</pre>
  {/if}

  {#if capability}
    <div class="flex flex-col gap-1 rounded-md border border-border bg-bg p-3 text-sm" data-testid="capability-scope">
      <div>Current: <strong class="font-medium">{current ? accessLabel(current) : 'unknown'}</strong></div>
      <div>Requested: <strong class="font-medium">{requested || 'no new capability'}</strong></div>
    </div>
  {:else}
    <p class="text-xs text-muted">
      This approval covers exactly this action; it grants no standing capability.
    </p>
    <label class="flex flex-col gap-1 text-sm">
      Edit before approve
      <input
        type="text"
        bind:value={edit}
        placeholder="Leave empty to approve as-is"
        class="min-h-11 rounded-md border border-border bg-bg px-3 py-2 text-sm"
      />
    </label>
  {/if}

  {#snippet footer()}
    <Button variant="ghost" onclick={onDeny}>Deny</Button>
    {#if capability}
      <Button onclick={allowCapability}>Allow {requested || 'nothing'}</Button>
    {:else}
      <Button onclick={approve}>Approve</Button>
    {/if}
  {/snippet}
</Modal>
