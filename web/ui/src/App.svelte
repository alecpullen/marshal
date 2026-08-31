<script lang="ts">
  import { onMount } from 'svelte'
  import Dashboard from './views/Dashboard.svelte'
  import NewAgent from './views/NewAgent.svelte'
  import Chat from './views/Chat.svelte'
  import Sidebar from './lib/Sidebar.svelte'
  import PendingList from './lib/PendingList.svelte'
  import ClientsPanel from './lib/ClientsPanel.svelte'
  import ActivityFeed from './lib/ActivityFeed.svelte'
  import { connectFleetSSE } from './lib/sse'
  import { createFleetStore } from './lib/fleet'
  import { listPending, listClients, type PendingSubmission, type MCPClient } from './lib/api'

  let hash = $state('#')

  /*
    Collapsed is a rail that keeps its place in the layout rather than an
    overlay, so it works at any width without a backdrop or focus trap and
    can never cover the content. The default follows the viewport: wide
    screens have room for the index, narrow ones do not.
  */
  let navOpen = $state(true)

  const NAV_KEY = 'marshal:navOpen'

  function toggleNav() {
    navOpen = !navOpen
    try {
      localStorage.setItem(NAV_KEY, navOpen ? '1' : '0')
    } catch {
      // Private mode or blocked site data: the preference is a
      // convenience, not state the app depends on.
    }
  }

  /*
    The fleet store lives here rather than in Dashboard because the sidebar
    needs it on every route, including the chat view. It also means one SSE
    connection instead of two: the shell and the dashboard each opened
    their own before.
  */
  const { state: fleet, actions } = createFleetStore()

  let pending = $state<PendingSubmission[]>([])
  let clients = $state<MCPClient[]>([])

  async function refreshPending() {
    try {
      pending = await listPending()
    } catch {
      pending = []
    }
  }

  async function refreshClients() {
    try {
      clients = await listClients()
    } catch {
      clients = []
    }
  }

  function navigate(next: string) {
    window.location.hash = next
  }

  onMount(() => {
    try {
      const stored = localStorage.getItem(NAV_KEY)
      navOpen = stored === null ? window.innerWidth >= 1024 : stored === '1'
    } catch {
      navOpen = window.innerWidth >= 1024
    }

    const update = () => (hash = window.location.hash || '#')
    window.addEventListener('hashchange', update)
    update()

    actions.refresh()
    refreshPending()
    refreshClients()

    const ctl = new AbortController()
    const disconnect = connectFleetSSE({
      onDelta: (d) => {
        actions.applyDelta(d)
        if (d.kind === 'project_removed' && hash !== '#') navigate('#')
        // A pending delta carries only the kind; refetch to pick up the
        // payload the attention list needs to render a decision.
        if (d.kind === 'pending') {
          actions.refresh()
          refreshPending()
        }
      },
      // We lagged or reconnected past the ring: the snapshot is the
      // authority, so refetch rather than trying to patch the gap.
      onOverflow: () => actions.refresh(),
      signal: ctl.signal,
    })
    return () => {
      window.removeEventListener('hashchange', update)
      ctl.abort()
      disconnect()
    }
  })

  const chatMatch = $derived(/^#chat\/(.+)$/.exec(hash))
  const chatSessionId = $derived(chatMatch?.[1] ?? null)

  const titles: Record<string, string> = {
    '#pending': 'Pending',
    '#clients': 'MCP Clients',
    '#activity': 'Activity',
  }
</script>

<div class="flex h-screen overflow-hidden">
  <Sidebar
    open={navOpen}
    onToggle={toggleNav}
    agents={$fleet.agents}
    projects={$fleet.projects}
    pendingCount={pending.length}
    clientCount={clients.length}
    route={hash}
    activeAgentId={chatSessionId}
    onNavigate={navigate}
  />

  <main class="min-w-0 flex-1 overflow-hidden">
    {#if chatSessionId}
      <!--
        Keyed so a hash change from one chat to another builds a fresh Chat
        rather than reusing the instance. createSessionStore captures the
        session id for the component's lifetime and says so; without the key
        that assumption is false, and going straight between two chats keeps
        the first one's store, header and transcript.
      -->
      {#key chatSessionId}
        <Chat sessionId={chatSessionId} onBack={() => navigate('#')} />
      {/key}
    {:else if hash === '#new'}
      <div class="h-full overflow-y-auto">
        <NewAgent onDone={(id) => navigate(id ? `#chat/${id}` : '#')} />
      </div>
    {:else if titles[hash]}
      <div class="h-full overflow-y-auto">
        <div class="mx-auto flex max-w-4xl flex-col gap-4 p-6">
          <h1 class="text-lg font-semibold">{titles[hash]}</h1>
          {#if hash === '#pending'}
            <PendingList {pending} onResolved={refreshPending} />
          {:else if hash === '#clients'}
            <ClientsPanel />
          {:else}
            <ActivityFeed />
          {/if}
        </div>
      </div>
    {:else}
      <div class="h-full overflow-y-auto">
        <Dashboard
          fleet={$fleet}
          onRefresh={actions.refresh}
          onOpenAgent={(id) => navigate(`#chat/${id}`)}
          onNewAgent={() => navigate('#new')}
        />
      </div>
    {/if}
  </main>
</div>

<style>
  /*
    Body background, colour, margin and font live in app.css, which owns the
    palette. They used to be duplicated here as a light theme and, because
    Svelte injects component styles after app.css, that copy won the cascade
    and left near-white text on a light body. One source of truth only.

    Tailwind's preflight already sets box-sizing.
  */
</style>
