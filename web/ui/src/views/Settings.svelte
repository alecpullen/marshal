<script lang="ts">
  import Tabs from '../lib/ui/Tabs.svelte'
  import Toast from '../lib/ui/Toast.svelte'
  import ClientsPanel from '../lib/ClientsPanel.svelte'
  import Models from './settings/Models.svelte'
  import Notifications from './settings/Notifications.svelte'
  import StatusLinks from './settings/StatusLinks.svelte'
  import Providers from './settings/Providers.svelte'
  import type { SettingsTab } from '../lib/routes'

  let { tab, budgetTick = 0, onNavigate }: { tab: SettingsTab; budgetTick?: number; onNavigate: (hash: string) => void } = $props()

  let toast = $state('')
  const TABS = [
    { value: 'models', label: 'Models' },
    { value: 'providers', label: 'Providers' },
    { value: 'tokens', label: 'Tokens' },
    { value: 'notifications', label: 'Notifications' },
    { value: 'status-links', label: 'Status links' },
  ]
</script>

<div class="mx-auto flex max-w-5xl flex-col gap-4 p-6">
  <h1 class="text-lg font-semibold">Settings</h1>
  <Tabs label="Settings" tabs={TABS} value={tab} onchange={(t) => onNavigate(`#settings/${t}`)} />
  {#if tab === 'models'}
    <Models {budgetTick} onToast={(t) => (toast = t)} />
  {:else if tab === 'providers'}
    <Providers onToast={(t) => (toast = t)} />
  {:else if tab === 'notifications'}
    <Notifications onToast={(t) => (toast = t)} />
  {:else if tab === 'status-links'}
    <StatusLinks onToast={(t) => (toast = t)} />
  {:else}
    <ClientsPanel />
  {/if}
</div>

<Toast bind:text={toast} />
