<script lang="ts">
  import Button from './ui/Button.svelte'
  import Badge from './ui/Badge.svelte'
  import type { GateResult as GateResultData } from './api'

  let {
    result,
    onOverride,
  }: {
    /** The gate outcome the server reported for this push. */
    result: GateResultData
    /** Called with the required reason when the user chooses to override. */
    onOverride: (reason: string) => void
  } = $props()

  let reason = $state('')

  const passed = $derived(result.ok && !result.skipped)
  const failed = $derived(!result.ok && !result.skipped)
  const skipped = $derived(result.skipped)

  function requestOverride() {
    const r = reason.trim()
    if (!r) return
    onOverride(r)
  }
</script>

{#if passed}
  <div class="flex items-center gap-2">
    <Badge tone="running">Gate passed</Badge>
    <span class="text-sm text-muted">Build and tests are green.</span>
  </div>
{:else if failed}
  <div class="flex flex-col gap-3">
    <div class="flex items-center gap-2">
      <Badge tone="danger">Gate failed</Badge>
      <span class="text-sm text-muted">Command <code class="font-mono">{result.failedCommand}</code> did not pass.</span>
    </div>
    {#if result.output}
      <pre class="max-h-48 overflow-auto rounded-md border border-border bg-bg p-3 text-xs text-muted">{result.output}</pre>
    {/if}
    <div class="flex flex-col gap-2">
      <label class="flex flex-col gap-1 text-sm">
        Reason for overriding the failed gate
        <input
          bind:value={reason}
          placeholder="e.g. flaky test, unrelated to this change"
          class="min-h-11 rounded-md border border-border bg-bg p-2 text-sm"
        />
      </label>
      <div>
        <Button variant="danger" disabled={!reason.trim()} onclick={requestOverride}>Override and push</Button>
      </div>
    </div>
  </div>
{:else if skipped}
  <div class="flex flex-col gap-3">
    <div class="flex items-center gap-2">
      <Badge tone="attention">Gate skipped</Badge>
      <span class="text-sm text-muted">No build or test command could be resolved for this project.</span>
    </div>
    <div class="flex flex-col gap-2">
      <label class="flex flex-col gap-1 text-sm">
        Reason for pushing without a gate
        <input
          bind:value={reason}
          placeholder="e.g. no test harness configured"
          class="min-h-11 rounded-md border border-border bg-bg p-2 text-sm"
        />
      </label>
      <div>
        <Button variant="danger" disabled={!reason.trim()} onclick={requestOverride}>Override and push</Button>
      </div>
    </div>
  </div>
{/if}
