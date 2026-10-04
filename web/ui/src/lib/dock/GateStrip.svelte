<script lang="ts">
  import { onMount } from 'svelte'
  import Button from '../ui/Button.svelte'
  import { getGate, runGate, errMessage, type GateRecord } from '../api'
  import { ago, gateState, pickGate } from './gate'

  let { agentId, gate = undefined }: { agentId: string; gate?: GateRecord } = $props()

  // The fleet stream's copy omits the verify output; `full` is what GET …/gate or a run returned.
  let full = $state<GateRecord | null>(null)
  let running = $state(false)
  let open = $state(false)
  let error = $state('')
  const record = $derived(pickGate(gate, full))
  const kind = $derived(gateState(record))

  onMount(() => {
    if (!gate) getGate(agentId).then((g) => (full = g)).catch(() => {})
  })

  async function run() {
    running = true
    error = ''
    try {
      full = await runGate(agentId)
    } catch (e) {
      error = errMessage(e)
    } finally {
      running = false
    }
  }

  async function toggle() {
    open = !open
    if (open && record?.result && !record.result.output) {
      try {
        full = (await getGate(agentId)) ?? full
      } catch {
        // The strip still shows the failing command.
      }
    }
  }
</script>

<div class="flex flex-col gap-1 border-b border-border px-3 py-2 text-xs" data-testid="gate-strip">
  <div class="flex items-center gap-2">
    {#if kind === 'passed'}
      <span class="text-ok">✓ gate passed</span>
    {:else if kind === 'failed'}
      <button type="button" class="text-err hover:underline" onclick={toggle}>✗ gate failed{record?.result?.failedCommand ? `: ${record.result.failedCommand}` : ''}</button>
    {:else if kind === 'skipped'}
      <span class="text-warn">skipped — proves nothing</span>
    {:else}
      <span class="text-muted">gate not run</span>
    {/if}
    {#if record?.at}<span class="text-muted">{ago(record.at)}</span>{/if}
    <Button variant="ghost" class="ml-auto min-h-0 px-2 py-0.5 text-xs" disabled={running} onclick={run}>
      {#if running}<span class="animate-spin">◌</span> Running…{:else}Run gate{/if}
    </Button>
  </div>
  {#if error}<div class="text-err">{error}</div>{/if}
  {#if open && kind === 'failed'}
    <pre class="max-h-60 overflow-auto rounded bg-bg p-2 font-mono whitespace-pre-wrap">{record?.result?.output ? record.result.output.split('\n').slice(-60).join('\n') : 'loading…'}</pre>
  {/if}
</div>
