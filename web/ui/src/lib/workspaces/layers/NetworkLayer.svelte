<script lang="ts">
  import ChipList from '../ChipList.svelte'
  import LayerCard from '../LayerCard.svelte'
  import Segmented from '../../ui/Segmented.svelte'
  import type { NetRow, WSNetwork } from '../../api'
  import type { LayerProps } from './types'

  let {
    doc,
    diags,
    selected,
    onSelect,
    onPatch,
    hosts,
    published,
  }: LayerProps & {
    /** Per-host usage from the network aggregates; empty when none is available. */
    hosts: NetRow[]
    /** The published version's network, to mark hosts added to the draft since (from the inspector or by hand). */
    published: WSNetwork | null
  } = $props()

  const seen = (h: string) => hosts.find((r) => r.host === h)?.requests
  const added = (h: string) => published !== null && !published.egress.includes(h)
  const MODES = [
    { value: 'open', label: 'Open' },
    { value: 'allowlist', label: 'Allowlist' },
    { value: 'off', label: 'Off' },
  ]
</script>

<LayerCard layer={7} title="Network" {selected} {diags} {onSelect}>
  <Segmented label="Network mode" options={MODES} value={doc.network.mode || 'allowlist'} onchange={(mode) => onPatch(7, { ...doc.network, mode })} />
  <ChipList items={doc.network.egress} label="egress hosts" placeholder="host or *.suffix" onChange={(egress) => onPatch(7, { ...doc.network, egress })} />
  {#if doc.network.egress.length}
    <ul class="flex flex-col gap-0.5 text-xs" data-testid="egress-rows">
      {#each doc.network.egress as h (h)}
        <li class="flex items-center gap-2" data-host={h} data-added={added(h) ? '' : undefined}>
          <span class="font-mono">{h}</span>
          {#if seen(h) !== undefined}<span class="text-muted">seen {seen(h)}×</span>{/if}
          {#if added(h)}<span class="rounded bg-warn/15 px-1 text-warn">new in draft</span>{/if}
        </li>
      {/each}
    </ul>
  {/if}
</LayerCard>
