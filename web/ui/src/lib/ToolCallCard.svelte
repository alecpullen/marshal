<script lang="ts">
  import type { ToolCall } from './store.js'

  interface Props {
    toolCall: ToolCall
  }

  let { toolCall }: Props = $props()
  let open = $state(false)
</script>

<div class="card {toolCall.status}">
  <button class="summary" onclick={() => (open = !open)}>
    <span class="name">{toolCall.name}</span>
    <span class="status">{toolCall.status}</span>
  </button>
  {#if open}
    <div class="body">
      <pre class="args">{JSON.stringify(toolCall.args, null, 2)}</pre>
      {#if toolCall.output}
        <div class="output">{toolCall.output}</div>
      {/if}
      {#if toolCall.error}
        <div class="error">{toolCall.error}</div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .card {
    border: 1px solid #e5e5e5;
    border-radius: 8px;
    overflow: hidden;
    background: #fafafa;
  }
  .summary {
    width: 100%;
    display: flex;
    justify-content: space-between;
    padding: 0.5rem 0.75rem;
    background: transparent;
    border: none;
    font: inherit;
    cursor: pointer;
  }
  .name {
    font-weight: 500;
  }
  .status {
    text-transform: uppercase;
    font-size: 0.75rem;
    color: #666;
  }
  .body {
    padding: 0.75rem;
    border-top: 1px solid #e5e5e5;
  }
  .args {
    margin: 0 0 0.5rem;
    white-space: pre-wrap;
    font-size: 0.85rem;
  }
  .output {
    white-space: pre-wrap;
    font-family: monospace;
    font-size: 0.85rem;
  }
  .error {
    color: #b91c1c;
    margin-top: 0.5rem;
  }
</style>