<script lang="ts">
  import Tabs from '../lib/ui/Tabs.svelte'
  import CostTab from '../lib/usage/CostTab.svelte'
  import ActivityFeed from '../lib/ActivityFeed.svelte'
  import DiskPanel from '../lib/DiskPanel.svelte'
  import { AUDIT_EVENTS } from '../lib/audit'
  import type { UsageTab } from '../lib/routes'
  import type { AgentRow } from '../lib/fleet'

  let { tab, agents = [], onNavigate }: { tab: UsageTab; agents?: AgentRow[]; onNavigate: (hash: string) => void } = $props()

  let eventFilter = $state('')
  const TABS = [
    { value: 'cost', label: 'Cost' },
    { value: 'audit', label: 'Audit' },
    { value: 'disk', label: 'Disk' },
  ]
</script>

<div class="mx-auto flex max-w-5xl flex-col gap-4 p-6">
  <h1 class="text-lg font-semibold">Usage</h1>
  <Tabs label="Usage" tabs={TABS} value={tab} onchange={(t) => onNavigate(`#usage?tab=${t}`)} />

  {#if tab === 'cost'}
    <CostTab {agents} />
  {:else if tab === 'audit'}
    <label class="flex w-fit items-center gap-2 text-xs text-muted">Event
      <select aria-label="Event type" class="rounded border border-border bg-bg px-2 py-1.5 text-sm text-fg" bind:value={eventFilter}>
        <option value="">All events</option>
        {#each AUDIT_EVENTS as e (e)}<option value={e}>{e}</option>{/each}
      </select>
    </label>
    <ActivityFeed filter={eventFilter} />
  {:else}
    <DiskPanel />
  {/if}
</div>
