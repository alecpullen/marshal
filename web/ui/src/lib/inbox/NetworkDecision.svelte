<script lang="ts">
  import Button from '../ui/Button.svelte'
  import { errMessage, type NetDecisionKind } from '../api'
  import type { NetworkDecisionItem } from '../fleet'
  import type { DecideFn } from './decision'

  let {
    item,
    agentName = '',
    compact = false,
    onDecide,
  }: {
    item: NetworkDecisionItem
    /** The agent's display name; its id when empty. */
    agentName?: string
    /** Stacked buttons for a Live wall tile. */
    compact?: boolean
    onDecide: DecideFn
  } = $props()

  let busy = $state(false)
  let error = $state('')

  async function decide(d: NetDecisionKind) {
    busy = true
    error = ''
    try {
      await onDecide(item, d)
    } catch (e) {
      // The item stays so the person can retry, or pick another answer.
      error = errMessage(e)
      busy = false
    }
  }
</script>

<div class="flex min-w-0 flex-1 flex-wrap items-center gap-2" data-testid="network-decision">
  <div class="min-w-0 flex-1">
    <div class="truncate text-sm font-medium">{agentName || item.agentId} tried to reach <span class="font-mono">{item.host}</span></div>
    <div class="truncate text-xs text-muted">{item.workspace ? `workspace ${item.workspace}` : 'no workspace'}</div>
    {#if error}<div class="text-xs text-err" role="alert">{error}</div>{/if}
  </div>
  <div class="flex flex-wrap items-center gap-2 {compact ? 'w-full' : ''}">
    <Button variant="danger" disabled={busy} onclick={() => decide('block')}>Block</Button>
    <Button variant="ghost" disabled={busy} onclick={() => decide('allow-agent')}>Allow for this agent</Button>
    <Button disabled={busy} onclick={() => decide('add-to-workspace')}>Add to workspace</Button>
  </div>
</div>
