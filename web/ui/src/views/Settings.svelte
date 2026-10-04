<script lang="ts">
  import Tabs from '../lib/ui/Tabs.svelte'
  import Toast from '../lib/ui/Toast.svelte'
  import ClientsPanel from '../lib/ClientsPanel.svelte'
  import Models from './settings/Models.svelte'
  import Providers from './settings/Providers.svelte'
  import Secrets from './settings/Secrets.svelte'
  import Credentials from './settings/Credentials.svelte'
  import Repos from './settings/Repos.svelte'
  import type { SettingsTab } from '../lib/routes'

  let { tab, budgetTick = 0, onNavigate }: { tab: SettingsTab; budgetTick?: number; onNavigate: (hash: string) => void } = $props()

  let toast = $state('')
  const TABS = [
    { value: 'models', label: 'Models' },
    { value: 'providers', label: 'Providers' },
    { value: 'secrets', label: 'Secrets' },
    { value: 'credentials', label: 'Credentials' },
    { value: 'repos', label: 'Repos' },
    { value: 'tokens', label: 'Tokens' },
  ]
</script>

<div class="mx-auto flex max-w-5xl flex-col gap-4 p-6">
  <h1 class="text-lg font-semibold">Settings</h1>
  <Tabs label="Settings" tabs={TABS} value={tab} onchange={(t) => onNavigate(`#settings/${t}`)} />
  {#if tab === 'models'}
    <Models {budgetTick} onToast={(t) => (toast = t)} />
  {:else if tab === 'providers'}
    <Providers onToast={(t) => (toast = t)} />
  {:else if tab === 'secrets'}
    <Secrets onToast={(t) => (toast = t)} />
  {:else if tab === 'credentials'}
    <Credentials onToast={(t) => (toast = t)} />
  {:else if tab === 'repos'}
    <Repos onToast={(t) => (toast = t)} />
  {:else}
    <ClientsPanel />
  {/if}
</div>

<Toast bind:text={toast} />
