<script lang="ts">
  import { onMount } from 'svelte'
  import Tabs from '../lib/ui/Tabs.svelte'
  import Toast from '../lib/ui/Toast.svelte'
  import SkillsTab from '../lib/library/SkillsTab.svelte'
  import PluginsTab from '../lib/library/PluginsTab.svelte'
  import RecipesTab from '../lib/library/RecipesTab.svelte'
  import MemoryTab from '../lib/library/MemoryTab.svelte'
  import ClientsPanel from '../lib/ClientsPanel.svelte'
  import { listProjects, type ProjectStatus } from '../lib/api'
  import { formatLibraryRoute, type LibraryRoute, type LibraryTab } from '../lib/routes'

  let { route, onNavigate }: { route: LibraryRoute; onNavigate: (hash: string) => void } = $props()

  let projects = $state<ProjectStatus[]>([])
  let toast = $state('')

  onMount(async () => {
    try {
      projects = await listProjects()
    } catch {
      projects = []
    }
  })

  const TABS = [
    { value: 'skills', label: 'Skills' },
    { value: 'plugins', label: 'Plugins' },
    { value: 'mcp', label: 'MCP' },
    { value: 'memory', label: 'Memory' },
    { value: 'recipes', label: 'Recipes' },
  ]
</script>

<div class="mx-auto flex max-w-5xl flex-col gap-4 p-6">
  <h1 class="text-lg font-semibold">Library</h1>
  <Tabs label="Library" tabs={TABS} value={route.tab} onchange={(t) => onNavigate(formatLibraryRoute({ tab: t as LibraryTab, project: route.project }))} />

  <!-- Keyed so a tab switch builds a fresh tab; each one owns its own load. -->
  {#key route.tab}
    {#if route.tab === 'skills'}
      <SkillsTab {projects} project={route.project} onToast={(t) => (toast = t)} />
    {:else if route.tab === 'plugins'}
      <PluginsTab {projects} project={route.project} onToast={(t) => (toast = t)} />
    {:else if route.tab === 'memory'}
      <MemoryTab {projects} project={route.project} onProject={(root) => onNavigate(formatLibraryRoute({ tab: 'memory', project: root }))} onToast={(t) => (toast = t)} />
    {:else if route.tab === 'recipes'}
      <RecipesTab {projects} onToast={(t) => (toast = t)} {onNavigate} />
    {:else}
      <ClientsPanel />
    {/if}
  {/key}
</div>

<Toast bind:text={toast} />
