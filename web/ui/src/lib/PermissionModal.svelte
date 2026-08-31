<script lang="ts">
  import type { PendingPermission } from './store.js'
  import type { Decision } from './api.js'
  import Modal from './ui/Modal.svelte'
  import Button from './ui/Button.svelte'

  interface Props {
    permission: PendingPermission
    onResolve: (d: Decision) => void | Promise<void>
    onDeny: () => void | Promise<void>
  }

  let { permission, onResolve, onDeny }: Props = $props()
  let edit = $state('')

  async function approve() {
    await onResolve({ approved: true, edited: edit || undefined })
  }
</script>

<Modal title="Permission request" onDismiss={onDeny}>
  <p class="text-sm">Tool: <strong class="font-medium">{permission.toolName}</strong></p>

  {#if permission.command}
    <pre
      class="overflow-x-auto rounded-md border border-border bg-bg p-3 font-mono text-xs whitespace-pre-wrap">{permission.command}</pre>
  {/if}

  {#if permission.diff}
    <pre
      class="max-h-64 overflow-auto rounded-md border border-border bg-bg p-3 font-mono text-xs whitespace-pre-wrap">{permission.diff}</pre>
  {/if}

  <label class="flex flex-col gap-1 text-sm">
    Edit before approve
    <input
      type="text"
      bind:value={edit}
      placeholder="Leave empty to approve as-is"
      class="min-h-11 rounded-md border border-border bg-bg px-3 py-2 text-sm"
    />
  </label>

  {#snippet footer()}
    <Button variant="ghost" onclick={onDeny}>Deny</Button>
    <Button onclick={approve}>Approve</Button>
  {/snippet}
</Modal>
