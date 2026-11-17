<script lang="ts">
  import { onMount } from 'svelte'
  import Fleet from './views/Fleet.svelte'
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
  import Schedules from './views/Schedules.svelte'
  import Gallery from './views/workspaces/Gallery.svelte'
  import Designer from './views/workspaces/Designer.svelte'
  import Builds from './views/workspaces/Builds.svelte'
  import Network from './views/Network.svelte'
  import Project from './views/Project.svelte'
  import Factory from './views/Factory.svelte'
  import FactoryNew from './views/FactoryNew.svelte'
  import FactoryItem from './views/FactoryItem.svelte'
  import WorkingDrafts from './views/WorkingDrafts.svelte'
  import Planner from './views/Planner.svelte'
  import ReviewBot from './views/automations/ReviewBot.svelte'
  import CIFixer from './views/automations/CIFixer.svelte'
  import Palette from './lib/Palette.svelte'
  import Home from './views/Home.svelte'
  import Roadmap from './views/Roadmap.svelte'
  import StudioNav from './lib/nav/StudioNav.svelte'
  import MobileNav from './lib/nav/MobileNav.svelte'
  import { buildNavModel } from './lib/nav/model'
  import { filterFactoryItems } from './lib/studio/model'
  import { createNavigationGuard } from './lib/navigationGuard'
  import StateMessage from './lib/ui/StateMessage.svelte'
  import PendingList from './lib/PendingList.svelte'
  import ProjectsPanel from './lib/ProjectsPanel.svelte'
  import SessionsPanel from './lib/SessionsPanel.svelte'
  import { connectFleetSSE } from './lib/sse'
  import { createFleetStore, type NetworkDecisionItem } from './lib/fleet'
  import { createFactoryStore } from './lib/factoryStore'
  import { get } from 'svelte/store'
  import { loadPrefs, maybeNotify, show as showNotification, type FleetLike } from './lib/notify'
  import DecisionOutcomeView from './lib/inbox/DecisionOutcome.svelte'
  import { outcomeFor, type DecisionOutcome } from './lib/inbox/decision'
  import { sessionsProjectFromHash, isScopedSessions, pageFromHash, parseChatRoute, parseRunRoute, parseLiveRoute, parseLibraryRoute, parseSettingsRoute, parseUsageRoute, parseWorkspacesRoute, parseNetworkRoute, parseProjectRoute, parseWatchesRoute, parseFactoryRoute, parseNewAgentRoute, parseRoadmapRoute, parsePlannerRoute, redirectLegacy } from './lib/routes'
  import { isTokenRejected, listPending, listClients, requestToken, type PendingSubmission, type MCPClient, type NetDecisionKind } from './lib/api'

  let hash = $state('#')

  /*
    A token the bridge refused is not a transient error: the client stops
    calling the API rather than re-prompting, because re-prompting for the
    value the server just rejected is a loop. That leaves the UI with no way
    back unless it offers one, so this surfaces the refusal as an action.
    Re-checked on a timer because the refusal happens inside a request, not
    in a store the shell can subscribe to.
  */
  let tokenRejected = $state(false)

  function enterToken() {
    try {
      requestToken()
    } catch {
      // Cancelling or entering nothing leaves the refusal standing, which
      // is the honest outcome: the banner stays until a token is accepted.
    }
    tokenRejected = isTokenRejected()
    if (!tokenRejected) actions.refresh()
  }

  /*
    Collapsed is a rail that keeps its place in the layout rather than an
    overlay, so it works at any width without a backdrop or focus trap and
    can never cover the content. The default follows the viewport: the labelled
    sidebar needs 1200px (design C4), so the tablet band between 768 and 1199
    starts as the icon rail. A stored preference — `marshal.ui.sidebar`, "1"
    open and "0" collapsed — still wins at any width.
  */
  let navOpen = $state(true)

  const NAV_KEY = 'marshal.ui.sidebar'

  /*
    Below 768px the shell is the phone one: a top app bar and a bottom primary
    navigation, and no sidebar at all. It replaces the sidebar in the markup
    rather than hiding it, so a phone never carries a second navigation
    landmark or its focus order. The width here and the shell classes in
    app.css must stay the same number.
  */
  const PHONE_QUERY = '(max-width: 767px)'
  let phone = $state(false)

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

  /*
    The factory work list is shell state for the same reason: the list, one
    item and the create view share one store, so a command's own result is
    already visible wherever it is next read and no view needs a poller of its
    own. `start`/`stop` are idempotent and mirror the fleet store's lifecycle.
  */
  const factoryStore = createFactoryStore()
  const factory = factoryStore.state

  /*
    The views treat a manual refresh as fire-and-forget: the store records the
    error it wants shown, and this only swallows the rejection that keeps a
    poller's backoff counting. The item view does not use this — its stale-409
    path awaits the store's own `refresh` and reports a failure itself.
  */
  function refreshFactory() {
    void factoryStore.refresh().catch(() => {})
  }

  // Finished runs already announced by a browser notification.
  const announcedRuns = new Set<string>()

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

  /*
    One guard for the shell's lifetime. A view that holds unsaved input
    registers itself and clears itself when it is destroyed; the shell asks
    here, once per navigation, so a view never has to intercept a link.
    Nothing registered means every route is free to leave.
  */
  const guard = createNavigationGuard()

  /*
    The hash `navigate` already asked about. Assigning `location.hash` fires
    `hashchange`, and that handler would otherwise ask the same question a
    second time — or ask it about a route the person just approved.
  */
  let approvedNext: string | null = null

  /** The hash the shell displays, in the same shape the routes are parsed in. */
  const currentHash = () => window.location.hash || '#'

  function navigate(next: string) {
    if (next === currentHash()) return
    if (!guard.allow()) return
    approvedNext = next
    window.location.hash = next
  }

  onMount(() => {
    try {
      const stored = localStorage.getItem(NAV_KEY)
      navOpen = stored === null ? window.innerWidth >= 1200 : stored === '1'
    } catch {
      navOpen = window.innerWidth >= 1200
    }

    /*
      The phone shell is a different structure, not a different width of the
      same one, so it is decided here rather than in a media query and the
      result is passed down as markup. A missing `matchMedia` (an old embedded
      webview) keeps the desktop shell, which every route still renders.
    */
    const width = typeof window.matchMedia === 'function' ? window.matchMedia(PHONE_QUERY) : null
    phone = width?.matches ?? false
    const onWidth = (e: MediaQueryListEvent) => (phone = e.matches)
    width?.addEventListener('change', onWidth)

    tokenRejected = isTokenRejected()
    const tokenPoll = setInterval(() => {
      tokenRejected = isTokenRejected()
    }, 1000)

    // A project added or removed in ProjectsPanel is already reflected in
    // that panel's own list, but the sidebar badge reads the fleet store —
    // so returning from these routes is the moment to bring it back in
    // sync, ahead of the next SSE delta.
    const update = () => {
      const current = hash
      const incoming = currentHash()
      if (incoming !== current) {
        // A route `navigate` already asked about is not asked about again:
        // the pending answer is consumed either way, so a stale approval can
        // never wave through a different route.
        const approved = approvedNext
        approvedNext = null
        if (approved !== incoming && !guard.allow()) {
          /*
            Browser Back, or a hash typed by hand, was declined. Putting the
            displayed hash back with `replaceState` rewrites the URL without
            firing another `hashchange`, so the mounted form — and every bit
            of input in it — is left exactly as it was.
          */
          history.replaceState(null, '', current)
          return
        }
      }
      // Old standalone pages moved; replace the entry so Back does not bounce.
      const moved = redirectLegacy(incoming)
      if (moved) {
        // The replace fires another hashchange; approve the destination up
        // front so that pass consumes the approval and never asks the same
        // leave-guard question again.
        approvedNext = moved
        window.location.replace(moved)
        return
      }
      hash = incoming
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
    factoryStore.start()
    refreshPending()
    refreshClients()

    const ctl = new AbortController()
    const disconnect = connectFleetSSE({
      onDelta: (d) => {
        actions.applyDelta(d)
        const toast = maybeNotify(d as FleetLike, loadPrefs(), document.visibilityState === 'visible', {
          nameOf: (id) => get(fleet).agents.find((a) => a.id === id)?.name || id,
          seen: announcedRuns,
        })
        if (toast) showNotification(toast)
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
      width?.removeEventListener('change', onWidth)
      clearInterval(tokenPoll)
      factoryStore.stop()
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
  const watchesRoute = $derived(parseWatchesRoute(hash))
  const networkRoute = $derived(parseNetworkRoute(hash))
  const projectRoute = $derived(parseProjectRoute(hash))
  const plannerRouteState = $derived(parsePlannerRoute(hash))
  const factoryRoute = $derived(parseFactoryRoute(hash))
  const newAgentRoute = $derived(parseNewAgentRoute(hash))
  const roadmapRoute = $derived(parseRoadmapRoute(hash))
  const page = $derived(pageFromHash(hash))

  /*
    One navigation model for the whole shell, so the sidebar, the rail and the
    phone navigation cannot disagree about a destination. The count is the same
    three sources Fleet treats as needing a person: factory work waiting on an
    approval, requests in the inbox queue, and blocked network requests.
  */
  const navModel = $derived(
    buildNavModel({
      route: hash,
      projects: $fleet.projects,
      factoryItems: $factory.items,
      needsCount: filterFactoryItems($factory.items, 'needs-you').length + $fleet.inbox.queue.length + $fleet.decisions.length,
    }),
  )

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

<div class="studio-shell flex h-screen overflow-hidden">
  {#if phone}
    <!--
      The phone chrome floats over the content column instead of taking a
      column of its own, so a view keeps the full width and `studio-phone-main`
      reserves the bars' height as padding — a composer at the bottom of a view
      stays above the bottom destinations rather than under them.
    -->
    <div class="studio-phone-chrome">
      <MobileNav model={navModel} onNavigate={navigate} />
    </div>
  {:else}
    <StudioNav model={navModel} open={navOpen} onToggle={toggleNav} onNavigate={navigate} />
  {/if}

  <main class="studio-phone-main min-w-0 flex-1 overflow-hidden">
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
          <!--
            Back returns to the factory item this session was opened from when
            the route carries one; parseChatRoute already drops a `from` that
            names no item, so the fallback is Fleet itself.
          -->
          <Chat sessionId={chatSessionId} route={chatRoute} agent={$fleet.agents.find((a) => a.id === chatSessionId)} onBack={() => navigate(chatRoute?.from ?? '#')} />
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
    {:else if plannerRouteState}
      <div class="h-full overflow-y-auto">
        <!--
          Keyed so moving between creation and one planner, or between two
          planners, builds a fresh view: the session's replay cursor and
          captured handoff belong to one planner and must not survive a
          change of route.
        -->
        {#key plannerRouteState.kind === 'session' ? plannerRouteState.id : `new:${plannerRouteState.project ?? ''}`}
          <Planner route={plannerRouteState} onNavigate={navigate} />
        {/key}
      </div>
    {:else if factoryRoute}
      <div class="h-full overflow-y-auto">
        {#if factoryRoute.drafts}
          {#key `${factoryRoute.draftId ?? ''}|${factoryRoute.create ? 'new' : 'list'}`}
            <WorkingDrafts
              projectRoot={new URLSearchParams(hash.split('?')[1] ?? '').get('project') ?? ''}
              draftId={factoryRoute.draftId ?? null}
              newDraft={factoryRoute.create === true}
              onNavigate={navigate}
            />
          {/key}
        {:else if factoryRoute.create}
          <FactoryNew projectRoot={new URLSearchParams(hash.split('?')[1] ?? '').get('project') ?? ''} onNavigate={navigate} registerGuard={guard.register} upsert={factoryStore.upsert} />
        {:else if factoryRoute.itemId}
          {#key factoryRoute.itemId}<FactoryItem itemId={factoryRoute.itemId} route={factoryRoute} factory={$factory} agents={$fleet.agents} getItem={factoryStore.getItem} upsert={factoryStore.upsert} onRefresh={factoryStore.refresh} onNavigate={navigate} />{/key}
        {:else}<Factory factory={$factory} onRefresh={refreshFactory} projectRoot={factoryRoute.projectRoot} filter={factoryRoute.filter} agents={$fleet.agents} onNavigate={navigate} />{/if}
      </div>
    {:else if projectRoute}
      <div class="h-full overflow-y-auto">
        {#key projectRoute.root}
          {#if projectRoute.automations === 'review-bot'}
            <ReviewBot root={projectRoute.root} draftId={projectRoute.draft} refreshTick={$fleet.automationTick} onNavigate={navigate} />
          {:else if projectRoute.automations === 'ci-fixer'}
            <CIFixer root={projectRoute.root} agents={$fleet.agents} refreshTick={$fleet.automationTick} onNavigate={navigate} />
          {:else}
            <Project root={projectRoute.root} agents={$fleet.agents} telemetry={$fleet.telemetry} onNavigate={navigate} registerGuard={guard.register} />
          {/if}
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
    {:else if watchesRoute}
      <div class="h-full overflow-y-auto">
        {#if watchesRoute.tab === 'schedules'}
          <Schedules projects={$fleet.projects} onNavigate={navigate} />
        {:else}
          <Watches agents={$fleet.agents} tick={$fleet.watchTick} onNavigate={navigate} />
        {/if}
      </div>
    {:else if roadmapRoute}
      <div class="h-full overflow-y-auto">
        <Roadmap area={roadmapRoute.area} onNavigate={navigate} />
      </div>
    {:else if newAgentRoute}
      <div class="h-full overflow-y-auto">
        <NewAgent
          projectRoot={newAgentRoute.projectRoot}
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
    {:else if page === 'fleet'}
      <div class="h-full overflow-y-auto">
        <!--
          The fleet stream is shell state, so its connection error is reported
          here rather than inside a page: retiring the old dashboard must not
          silently drop the only surface that told the user the stream broke.
        -->
        {#if $fleet.error}
          <div class="mx-auto max-w-6xl px-6 pt-6">
            <div class="rounded-md border border-danger bg-danger/10 p-3 text-sm">{$fleet.error}</div>
          </div>
        {/if}
        <Fleet
          agents={$fleet.agents}
          inbox={$fleet.inbox}
          notices={$fleet.notices}
          onDismissNotice={actions.dismissNotice}
          projects={$fleet.projects}
          automationTick={$fleet.automationTick}
          decisions={$fleet.decisions}
          onDecide={decideNetwork}
          onRefreshPending={refreshPending}
          onOpenAgent={(id) => navigate(`#chat/${id}`)}
          onNavigate={navigate}
          factory={$factory}
          onRefreshFactory={refreshFactory}
          onRefreshFleet={actions.refresh}
        />
      </div>
    {:else if page === 'inbox'}
      <div class="h-full overflow-y-auto">
        <Home
          agents={$fleet.agents}
          inbox={$fleet.inbox}
          notices={$fleet.notices}
          onDismissNotice={actions.dismissNotice}
          projects={$fleet.projects}
          automationTick={$fleet.automationTick}
          decisions={$fleet.decisions}
          onDecide={decideNetwork}
          onRefreshPending={refreshPending}
          onOpenAgent={(id) => navigate(`#chat/${id}`)}
          onNavigate={navigate}
        />
      </div>
    {:else}
      <div class="h-full overflow-y-auto">
        <div class="mx-auto max-w-3xl p-6">
          <StateMessage
            tone="neutral"
            title="Not Found"
            body="There is no page at {hash || '#'}. The shell only routes the pages in its navigation."
            actionLabel="Go to Fleet"
            onAction={() => navigate('#')}
          />
        </div>
      </div>
    {/if}
  </main>
</div>

{#if tokenRejected}
  <div class="fixed right-4 bottom-4 z-50 max-w-sm rounded-md border border-danger bg-raise p-3 text-sm shadow-lg" role="alert" data-testid="token-rejected">
    <p>The bridge rejected this token. Requests are paused rather than retried, because retrying sends the same refused value.</p>
    <div class="mt-2 flex items-center gap-2">
      <button type="button" class="cursor-pointer rounded border border-border px-2 py-1 text-xs hover:bg-hover" onclick={enterToken}>Enter a different token</button>
    </div>
  </div>
{/if}

{#if toast}
  <div class="fixed right-4 bottom-4 z-50 max-w-sm rounded-md border border-attention bg-raise p-3 text-sm shadow-lg" role="status">
    {toast.text}
    {#if toast.href}<a class="ml-1 text-info underline" href={toast.href} target="_blank" rel="noreferrer">Open</a>{/if}
  </div>
{/if}

<DecisionOutcomeView outcome={decisionOutcome} onClose={() => (decisionOutcome = null)} />
<Palette open={paletteOpen} agents={$fleet.agents} onClose={() => (paletteOpen = false)} onNavigate={navigate} />

<!--
  The Flutter Studio, served beside this one at /next/ (F2). It is out of the
  flow on purpose: this Svelte Studio is frozen to bug fixes, so the link must
  not disturb any layout a fix has to touch.
-->
<a class="fixed bottom-2 right-3 z-50 text-xs text-info underline" href="/next/" target="_blank" rel="noreferrer">Studio (preview)</a>

<style>
  /*
    Body background, colour, margin and font live in app.css, which owns the
    palette. They used to be duplicated here as a light theme and, because
    Svelte injects component styles after app.css, that copy won the cascade
    and left near-white text on a light body. One source of truth only.

    Tailwind's preflight already sets box-sizing.
  */
</style>
