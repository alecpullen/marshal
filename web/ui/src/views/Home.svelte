<script lang="ts">
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Tag from '../lib/ui/Tag.svelte'
  import Segmented from '../lib/ui/Segmented.svelte'
  import PendingActions from '../lib/inbox/PendingActions.svelte'
  import NetworkDecision from '../lib/inbox/NetworkDecision.svelte'
  import type { DecideFn } from '../lib/inbox/decision'
  import DiskPanel from '../lib/DiskPanel.svelte'
  import ActivityFeed from '../lib/ActivityFeed.svelte'
  import { buildInbox } from '../lib/inbox'
  import { describePending, type AgentRow, type NetworkDecisionItem, type RerouteNotice } from '../lib/fleet'
  import { APIError, approvePending, denyPending, undoReroute, errMessage, type PendingSubmission } from '../lib/api'
  import { shortName } from '../lib/utils'

  let {
    agents,
    pending,
    onRefreshPending,
    onOpenAgent,
    onNavigate,
    notices = [],
    onDismissNotice = () => {},
    decisions = [],
    onDecide = async () => {},
  }: {
    agents: AgentRow[]
    pending: PendingSubmission[]
    /** Watches that rerouted a role, each undoable until dismissed. */
    notices?: RerouteNotice[]
    onDismissNotice?: (id: string) => void
    /** Requests the egress proxy blocked, each waiting on Block, Allow for this agent, or Add to workspace. */
    decisions?: NetworkDecisionItem[]
    onDecide?: DecideFn
    onRefreshPending: () => void
    onOpenAgent: (id: string) => void
    onNavigate: (hash: string) => void
  } = $props()

  const SCOPE_KEY = 'marshal.ui.inbox.scope'
  function readScope(): 'mine' | 'everyone' {
    try {
      return localStorage.getItem(SCOPE_KEY) === 'everyone' ? 'everyone' : 'mine'
    } catch {
      return 'mine'
    }
  }
  let scope = $state<'mine' | 'everyone'>(readScope())
  function setScope(v: string) {
    scope = v === 'everyone' ? 'everyone' : 'mine'
    try {
      localStorage.setItem(SCOPE_KEY, scope)
    } catch {
      // A convenience only; the choice just does not persist.
    }
  }

  const inbox = $derived(buildInbox(agents, pending, scope === 'mine'))
  // Oldest first, so the request that has waited longest is on top.
  const netDecisions = $derived([...decisions].sort((a, b) => a.at - b.at))
  const agentName = (id: string) => agents.find((a) => a.id === id)?.name || id

  const originLetter = (origin?: string) =>
    ({ ui: 'U', cli: 'C', mcp: 'M', issue: '#' })[origin ?? ''] ?? (origin ? origin[0].toUpperCase() : '·')

  let notice = $state<string | null>(null)

  async function run(action: () => Promise<unknown>) {
    notice = null
    try {
      await action()
    } catch (e) {
      // 410 means it was already resolved elsewhere; refreshing is the answer.
      notice = e instanceof APIError && e.status === 410 ? 'That request was already resolved.' : e instanceof Error ? e.message : String(e)
    } finally {
      onRefreshPending()
    }
  }

  async function undo(n: RerouteNotice) {
    try {
      await undoReroute(n.id)
      onDismissNotice(n.id)
    } catch (e) {
      notice = errMessage(e)
      // 409: the binding changed since (or it was undone); 404: the bridge restarted. Undo cannot work any more.
      if (e instanceof APIError && (e.status === 409 || e.status === 404)) onDismissNotice(n.id)
    }
  }

  function elapsed(since: string): string {
    const ms = Date.now() - new Date(since).getTime()
    if (!Number.isFinite(ms) || ms < 0) return ''
    const s = Math.floor(ms / 1000)
    if (s < 60) return `${s}s`
    const m = Math.floor(s / 60)
    return m < 60 ? `${m}m` : `${Math.floor(m / 60)}h ${m % 60}m`
  }
</script>

<div class="mx-auto flex max-w-6xl flex-col gap-4 p-6">
  <header class="flex items-center justify-between gap-3">
    <h1 class="text-lg font-semibold">Home</h1>
    <Segmented
      label="Scope"
      value={scope}
      onchange={setScope}
      options={[
        { value: 'mine', label: 'Mine' },
        { value: 'everyone', label: 'Everyone' },
      ]}
    />
  </header>

  {#if notice}
    <Card class="border-attention text-sm">{notice}</Card>
  {/if}

  {#each notices as n (n.id)}
    <Card class="flex items-center gap-3 p-3 text-sm" data-testid="reroute-notice">
      <span class="min-w-0 flex-1 truncate">
        A watch moved <span class="font-mono">{n.role}</span> from <span class="font-mono">{n.from}</span> to <span class="font-mono">{n.to}</span>.
      </span>
      <Button variant="ghost" onclick={() => undo(n)}>Undo</Button>
      <Button variant="ghost" aria-label="Dismiss" onclick={() => onDismissNotice(n.id)}>✕</Button>
    </Card>
  {/each}

  <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_18rem]">
    <div class="flex flex-col gap-6">
      <section aria-labelledby="inbox-needs">
        <h2 id="inbox-needs" class="mb-2 text-xs tracking-wide text-attention uppercase">Needs you · {inbox.needsYou.length + netDecisions.length}</h2>
        <div class="flex flex-col gap-2">
          {#each inbox.needsYou as item (item.kind === 'agent' ? 'a:' + item.agent.id : 'i:' + item.submission.id)}
            <Card class="flex items-center gap-3 p-3">
              {#if item.kind === 'agent'}
                {@const a = item.agent}
                <span class="flex size-7 shrink-0 items-center justify-center rounded-full bg-raise font-mono text-xs" title={a.origin}>{originLetter(a.origin)}</span>
                <div class="min-w-0 flex-1">
                  <div class="truncate text-sm font-medium">{a.name || a.id} <span class="font-normal text-muted">· {shortName(a.project)}</span></div>
                  <div class="truncate text-xs text-muted">{a.pending ? describePending(a.pending) : ''}</div>
                </div>
                <PendingActions agent={a} onResolved={onRefreshPending} onOpen={() => onNavigate(`#chat/${a.id}`)} />
              {:else}
                {@const p = item.submission}
                <span class="flex size-7 shrink-0 items-center justify-center rounded-full bg-raise font-mono text-xs" title={p.origin}>{originLetter(p.origin)}</span>
                <div class="min-w-0 flex-1">
                  <div class="truncate text-sm font-medium">{p.title} <span class="font-normal text-muted">· {p.repoId}</span></div>
                  <div class="truncate text-xs text-muted">intake request{p.clientId ? ` from ${p.clientId}` : ''}</div>
                </div>
                <Button onclick={() => run(() => approvePending(p.id))}>Approve</Button>
                <Button variant="danger" onclick={() => run(() => denyPending(p.id))}>Deny</Button>
              {/if}
            </Card>
          {/each}
          {#each netDecisions as d (d.agentId + '|' + d.host)}
            <Card class="flex items-center gap-3 p-3">
              <NetworkDecision item={d} agentName={agentName(d.agentId)} {onDecide} />
            </Card>
          {/each}
          {#if inbox.needsYou.length + netDecisions.length === 0}
            <p class="text-sm text-muted">Nothing is waiting on you.</p>
          {/if}
        </div>
      </section>

      <section aria-labelledby="inbox-ready">
        <h2 id="inbox-ready" class="mb-2 text-xs tracking-wide text-accent uppercase">Ready to ship · {inbox.ready.length}</h2>
        <div class="flex flex-col gap-2">
          {#each inbox.ready as a (a.id)}
            <Card class="flex items-center gap-3 p-3">
              <div class="min-w-0 flex-1">
                <div class="truncate text-sm font-medium">{a.name || a.id}</div>
                <div class="truncate text-xs text-muted">
                  {a.branch || shortName(a.project)} · {a.changedFiles} file{a.changedFiles === 1 ? '' : 's'}
                </div>
              </div>
              <Button variant="ghost" onclick={() => onOpenAgent(a.id)}>Review</Button>
              <Button onclick={() => onOpenAgent(a.id)}>Open PR</Button>
            </Card>
          {:else}
            <p class="text-sm text-muted">No finished work to ship.</p>
          {/each}
        </div>
      </section>

      <section aria-labelledby="inbox-running">
        <h2 id="inbox-running" class="mb-2 text-xs tracking-wide text-running uppercase">Running · {inbox.running.length}</h2>
        <div class="flex flex-col gap-2">
          {#each inbox.running as a (a.id)}
            <Card class="flex items-center gap-3 p-3">
              <button class="min-w-0 flex-1 cursor-pointer text-left" onclick={() => onOpenAgent(a.id)}>
                <div class="truncate text-sm font-medium">{a.name || a.id} <span class="font-normal text-muted">· {shortName(a.project)}</span></div>
                <div class="truncate text-xs text-muted">{a.activity || 'working'}</div>
              </button>
              <Tag tone="ok">{elapsed(a.updatedAt)}</Tag>
            </Card>
          {:else}
            <p class="text-sm text-muted">No agents running.</p>
          {/each}
        </div>
      </section>
    </div>

    <aside class="flex flex-col gap-4">
      <DiskPanel compact />
      <ActivityFeed limit={10} />
    </aside>
  </div>
</div>
