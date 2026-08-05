<script lang="ts">
  import type { PendingPermission } from './store.js'
  import type { Decision } from './api.js'

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

<div class="overlay">
  <div class="modal">
    <h2>Permission request</h2>
    <p>Tool: <strong>{permission.toolName}</strong></p>
    {#if permission.command}
      <pre class="command">{permission.command}</pre>
    {/if}
    {#if permission.diff}
      <pre class="diff">{permission.diff}</pre>
    {/if}

    <label>
      Edit before approve
      <input type="text" bind:value={edit} placeholder="Leave empty to approve as-is" />
    </label>

    <div class="actions">
      <button class="deny" onclick={onDeny}>Deny</button>
      <button class="approve" onclick={approve}>Approve</button>
    </div>
  </div>
</div>

<style>
  .overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.4);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 100;
  }
  .modal {
    background: white;
    padding: 1.5rem;
    border-radius: 12px;
    max-width: 560px;
    width: 90%;
    max-height: 80vh;
    overflow: auto;
  }
  h2 {
    margin-top: 0;
  }
  pre {
    background: #f3f4f6;
    padding: 0.75rem;
    border-radius: 6px;
    white-space: pre-wrap;
    font-size: 0.85rem;
  }
  label {
    display: block;
    margin: 1rem 0;
  }
  input {
    width: 100%;
    margin-top: 0.25rem;
    padding: 0.5rem;
    border: 1px solid #d1d5db;
    border-radius: 6px;
    font: inherit;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
  }
  button {
    padding: 0.5rem 1rem;
    border-radius: 6px;
    border: 1px solid #d1d5db;
    background: white;
    cursor: pointer;
    font: inherit;
  }
  .approve {
    background: #22c55e;
    border-color: #22c55e;
    color: white;
  }
  .deny {
    background: #ef4444;
    border-color: #ef4444;
    color: white;
  }
</style>