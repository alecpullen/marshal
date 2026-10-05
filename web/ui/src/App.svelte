<script lang="ts">
  import { onMount } from 'svelte'
  import Dashboard from './views/Dashboard.svelte'
  import NewAgent from './views/NewAgent.svelte'
  import Chat from './views/Chat.svelte'
  import Runs from './views/Runs.svelte'
  import Run from './views/Run.svelte'
  import Live from './views/Live.svelte'
  import Review from './views/Review.svelte'

  import Library from './views/Library.svelte'
  import Settings from './views/Settings.svelte'
  import Usage from './views/Usage.svelte'
  import Watches from './views/Watches.svelte'
  import Gallery from './views/workspaces/Gallery.svelte'
  import Designer from './views/workspaces/Designer.svelte'
  import Builds from './views/workspaces/Builds.svelte'
  import Network from './views/Network.svelte'
  import Project from './views/Project.svelte'
  import Sidebar from './lib/Sidebar.svelte'
  import Rail from './lib/Rail.svelte'
  import Palette from './lib/Palette.svelte'
  import Home from './views/Home.svelte'
  import PendingList from './lib/PendingList.svelte'
  import ProjectsPanel from './lib/ProjectsPanel.svelte'
  import SessionsPanel from './lib/SessionsPanel.svelte'
  import { connectFleetSSE } from './lib/sse'
  import { createFleetStore, type NetworkDecisionItem } from './lib/fleet'
  import DecisionOutcomeView from './lib/inbox/DecisionOutcome.svelte'
  import { outcomeFor, type DecisionOutcome } from './lib/inbox/decision'
  import { sessionsProjectFromHash, isScopedSessions, pageFromHash, parseChatRoute, parseRunRoute, parseLiveRoute, parseLibraryRoute, parseSettingsRoute, parseUsageRoute, parseWorkspacesRoute, parseNetworkRoute, parseProjectRoute, redirectLegacy } from './lib/routes'
  import { listPending, listClients, type PendingSubmission, type MCPClient, type NetDecisionKind } from './lib/api'

  let hash = $state('#')

  /*
    Collapsed is a rail that keeps its place in the layout rather than an
    overlay, so it works at any width without a backdrop or focus trap and
    can never cover the content. The default follows the viewport: wide
    screens have room for the index, narrow ones do not.
  */
  let navOpen = $state(true)

  const NAV_KEY = 'marshal.ui.sidebar'

  let paletteOpen = $state(false)

  let toast = $state<{ text: string; href?: string } | null>(null)
  let toastTimer: ReturnType<typeof setTimeout> | undefined
  function flashToast(text: string, href?: string) {
    toast = { text, href }
    clearTimeout(toastTimer)
    toastTimer = setTimeout(() => (toast = null), 8000)
  }

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

  // What a settled decision shows: a draft toast or a repo-patch modal. Owned here so Home and Live share it.
  let decisionOutcome = $state<DecisionOutcome | null>(null)
  async function decideNetwork(item: NetworkDecisionItem, decision: NetDecisionKind) {
    const res = await actions.decideNetwork(item.agentId, item.host, decision)
    decisionOutcome = outcomeFor(item, decision, res)
  }

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

    // A project added or removed in ProjectsPanel is already reflected in
    // that panel's own list, but the sidebar badge reads the fleet store —
    // so returning from these routes is the moment to bring it back in
    // sync, ahead of the next SSE delta.
    const update = () => {
      // Old standalone pages moved; replace the entry so Back does not bounce.
      const moved = redirectLegacy(window.location.hash)
      if (moved) {
        window.location.replace(moved)
        return
      }
      hash = window.location.hash || '#'
      if (hash === '#projects' || isScopedSessions(hash) || hash === '#sessions') actions.refresh()
    }
    window.addEventListener('hashchange', update)
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        paletteOpen = !paletteOpen
      }
    }
    window.addEventListener('keydown', onKey)
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
      window.removeEventListener('keydown', onKey)
      ctl.abort()
      disconnect()
    }
  })

  const chatRoute = $derived(parseChatRoute(hash))
  const chatSessionId = $derived(chatRoute?.id ?? null)
  const runRoute = $derived(parseRunRoute(hash))
  const liveRoute = $derived(parseLiveRoute(hash))
  const libraryRoute = $derived(parseLibraryRoute(hash))
  const settingsRoute = $derived(parseSettingsRoute(hash))
  const usageRoute = $derived(parseUsageRoute(hash))
  const workspacesRoute = $derived(parseWorkspacesRoute(hash))
  const networkRoute = $derived(parseNetworkRoute(hash))
  const projectRoute = $derived(parseProjectRoute(hash))

  /*
    Sessions open either unscoped (#sessions — the project picker) or
    scoped to one project (#sessions/<encoded root>), which is where the
    sidebar's per-project sessions affordance links. The parse rule and
    its malformed-escape degradation live in lib/routes, tested there.
  */
  const sessionsProject = $derived(sessionsProjectFromHash(hash))

  const titles: Record<string, string> = {
    '#pending': 'Pending',
    '#projects': 'Projects',
    '#sessions': 'Sessions',
  }
</script>

<div class="flex h-screen overflow-hidden">
  <Rail route={hash} onNavigate={navigate} />
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
        {#if chatRoute?.view === 'review'}
          <Review agentId={chatSessionId} agent={$fleet.agents.find((a) => a.id === chatSessionId)} onShipped={(o) => {
              flashToast(o.message, o.href)
              // Discarding or merging ends the session's work; only a push leaves the agent to look at.
              navigate(o.kind === 'pushed' ? `#chat/${chatSessionId}` : '#')
            }} />
        {:else}
          <Chat sessionId={chatSessionId} route={chatRoute} agent={$fleet.agents.find((a) => a.id === chatSessionId)} onBack={() => navigate('#')} />
        {/if}
      {/key}
    {:else if runRoute}
      {#key runRoute.id}
        <Run
          agentId={runRoute.id}
          route={runRoute}
          agent={$fleet.agents.find((a) => a.id === runRoute.id)}
          onNavigate={navigate}
        />
      {/key}
    {:else if hash === '#runs'}
      <div class="h-full overflow-y-auto">
        <Runs agents={$fleet.agents} projects={$fleet.projects} onNavigate={navigate} onRefresh={actions.refresh} />
      </div>
    {:else if liveRoute}
      <div class="h-full overflow-y-auto">
        <Live
          agents={$fleet.agents}
          projects={$fleet.projects}
          route={liveRoute}
          onRefreshPending={refreshPending}
          onNavigate={navigate}
          decisions={$fleet.decisions}
          onDecide={decideNetwork}
        />
      </div>
    {:else if libraryRoute}
      <div class="h-full overflow-y-auto">
        <Library route={libraryRoute} onNavigate={navigate} />
      </div>
    {:else if settingsRoute}
      <div class="h-full overflow-y-auto">
        <Settings tab={settingsRoute.tab} budgetTick={$fleet.budgetTick} onNavigate={navigate} />
      </div>
    {:else if usageRoute}
      <div class="h-full overflow-y-auto">
        <Usage tab={usageRoute.tab} agents={$fleet.agents} budgetTick={$fleet.budgetTick} onNavigate={navigate} />
      </div>
    {:else if networkRoute}
      <div class="h-full overflow-y-auto">
        {#key `${networkRoute.workspace ?? ''}|${networkRoute.agent ?? ''}`}
          <Network workspace={networkRoute.workspace} agent={networkRoute.agent} onNavigate={navigate} />
        {/key}
      </div>
    {:else if projectRoute}
      <div class="h-full overflow-y-auto">
        {#key projectRoute.root}
          <Project root={projectRoute.root} agents={$fleet.agents} telemetry={$fleet.telemetry} onNavigate={navigate} />
        {/key}
      </div>
    {:else if workspacesRoute}
      <div class="h-full overflow-y-auto">
        {#if workspacesRoute.view === 'gallery'}
          <Gallery agents={$fleet.agents} onNavigate={navigate} />
        {:else if workspacesRoute.view === 'edit' && workspacesRoute.name}
          {#key workspacesRoute.name}
            <Designer name={workspacesRoute.name} onNavigate={navigate} />
          {/key}
        {:else if workspacesRoute.view === 'builds' && workspacesRoute.name}
          {#key workspacesRoute.name}
            <Builds name={workspacesRoute.name} onNavigate={navigate} />
          {/key}
        {/if}
      </div>
    {:else if hash === '#watches'}
      <div class="h-full overflow-y-auto">
        <Watches agents={$fleet.agents} tick={$fleet.watchTick} onNavigate={navigate} />
      </div>
    {:else if hash === '#new'}
      <div class="h-full overflow-y-auto">
        <NewAgent
          onDone={(id, warning) => {
            if (warning) flashToast(warning)
            navigate(id ? `#chat/${id}` : '#')
          }}
        />
      </div>
    {:else if titles[hash] || sessionsProject !== null}
      <div class="h-full overflow-y-auto">
        <div class="mx-auto flex max-w-4xl flex-col gap-4 p-6">
          <h1 class="text-lg font-semibold">{sessionsProject !== null ? 'Sessions' : titles[hash]}</h1>
          {#if hash === '#pending'}
            <PendingList {pending} onResolved={refreshPending} />
          {:else if hash === '#projects'}
            <ProjectsPanel />
          {:else if hash === '#sessions' || sessionsProject !== null}
            <!--
              Keyed on the scope so moving between the picker and a
              project — or between projects — builds a fresh panel. The
              in-panel picker choice must not survive a scope change made
              in the hash, and remounting leaves the mount effect as the
              only load path.
            -->
            {#key sessionsProject ?? ''}
              <SessionsPanel project={sessionsProject ?? undefined} onUnscope={() => navigate('#sessions')} />
            {/key}
          {/if}
        </div>
      </div>
    {:else if pageFromHash(hash) === 'home'}
      <div class="h-full overflow-y-auto">
        <Home
          agents={$fleet.agents}
          {pending}
          notices={$fleet.notices}
          onDismissNotice={actions.dismissNotice}
          decisions={$fleet.decisions}
          onDecide={decideNetwork}
          onRefreshPending={refreshPending}
          onOpenAgent={(id) => navigate(`#chat/${id}`)}
          onNavigate={navigate}
        />
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

{#if toast}
  <div class="fixed right-4 bottom-4 z-50 max-w-sm rounded-md border border-attention bg-raise p-3 text-sm shadow-lg" role="status">
    {toast.text}
    {#if toast.href}<a class="ml-1 text-info underline" href={toast.href} target="_blank" rel="noreferrer">Open</a>{/if}
  </div>
{/if}

<DecisionOutcomeView outcome={decisionOutcome} onClose={() => (decisionOutcome = null)} />
<Palette open={paletteOpen} agents={$fleet.agents} onClose={() => (paletteOpen = false)} onNavigate={navigate} />

<style>
  /*
    Body background, colour, margin and font live in app.css, which owns the
    palette. They used to be duplicated here as a light theme and, because
    Svelte injects component styles after app.css, that copy won the cascade
    and left near-white text on a light body. One source of truth only.

    Tailwind's preflight already sets box-sizing.
  */
</style>
