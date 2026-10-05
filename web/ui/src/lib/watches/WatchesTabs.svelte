<script lang="ts">
  import Tabs from '../ui/Tabs.svelte'
  import type { WatchesTab } from '../routes'

  let { tab, onNavigate }: { tab: WatchesTab; onNavigate: (hash: string) => void } = $props()
</script>

<Tabs
  label="Watches and schedules"
  tabs={[
    { value: 'watches', label: 'Watches' },
    { value: 'schedules', label: 'Schedules' },
  ]}
  value={tab}
  onchange={(t) => onNavigate(t === 'schedules' ? '#watches/schedules' : '#watches')}
/>
