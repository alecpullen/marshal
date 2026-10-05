<script lang="ts">
  import Card from '../lib/ui/Card.svelte'
  import Tag from '../lib/ui/Tag.svelte'
  import Segmented from '../lib/ui/Segmented.svelte'
  import { getNetworkAgents, getNetworkHosts, getNetworkRequests, errMessage, type NetAgentRow, type NetHostRow, type NetRecord, type NetView } from '../lib/api'
  import { PROCESS_MODE_BANNER, agoMs, bytesLabel, clockTime, filterRequests, hostViews, ruleTone, sortHosts, type HostSort, type HostSortKey } from '../lib/network/model'

  let { workspace, agent, onNavigate }: { workspace?: string; agent?: string; onNavigate: (hash: string) => void } = $props()

  /*
    Hosts and Requests follow the scope in the URL (one agent, or one
    workspace). By agent is per workspace; a page scoped to one agent has
    nothing to break down, so that view is not offered there.
  */
  let view = $state<NetView>('hosts')
  let hosts = $state<NetHostRow[]>([])
  let requests = $state<NetRecord[]>([])
  let agents = $state<NetAgentRow[]>([])
  let processMode = $state(false)
  let error = $state('')
  let loaded = $state(false)
  let sort = $state<HostSort>({ key: 'requests', dir: 'desc' })
  let filter = $state('')
  let now = $state(Date.now())

  // Only the latest request may write state: a slower, older one is dropped.
  let seq = 0
  async function load() {
    const mine = ++seq
    // svelte-ignore state_referenced_locally
    const scope = { workspace, agent }
    try {
      if (view === 'hosts') {
        const r = await getNetworkHosts(scope)
        if (mine !== seq) return
        hosts = r.rows
        processMode = r.processMode
      } else if (view === 'requests') {
        const r = await getNetworkRequests(scope)
        if (mine !== seq) return
        requests = r
      } else {
        const r = await getNetworkAgents({ workspace })
        if (mine !== seq) return
        agents = r
      }
      error = ''
    } catch (e) {
      if (mine !== seq) return
      error = errMessage(e)
    } finally {
      if (mine === seq) {
        loaded = true
        now = Date.now()
      }
    }
  }

  // Reload when the view or scope changes, and every 10s while the page is open.
  $effect(() => {
    void view
    void workspace
    void agent
    void load()
    const t = setInterval(() => void load(), 10_000)
    return () => clearInterval(t)
  })

  const rows = $derived(sortHosts(hostViews(hosts), sort))
  const shown = $derived(filterRequests(requests, filter))
  const scopeLabel = $derived(agent ? `agent ${agent}` : workspace ? `workspace ${workspace}` : 'all workspaces')

  function sortBy(key: HostSortKey) {
    sort = sort.key === key ? { key, dir: sort.dir === 'asc' ? 'desc' : 'asc' } : { key, dir: key === 'host' || key === 'rule' ? 'asc' : 'desc' }
  }
  const aria = (key: HostSortKey) => (sort.key === key ? (sort.dir === 'asc' ? 'ascending' : 'descending') : 'none')
  const COLS: { key: HostSortKey; label: string; right?: boolean }[] = [
    { key: 'host', label: 'Host' },
    { key: 'rule', label: 'Rule' },
    { key: 'requests', label: 'Requests', right: true },
    { key: 'bytes', label: 'Bytes up / down', right: true },
    { key: 'agents', label: 'Agents', right: true },
    { key: 'lastSeen', label: 'Last seen', right: true },
  ]
  const VIEWS = $derived([
    { value: 'hosts', label: 'Hosts' },
    { value: 'requests', label: 'Requests' },
    ...(agent ? [] : [{ value: 'agents', label: 'By agent' }]),
  ])
</script>

<div class="mx-auto flex max-w-5xl flex-col gap-4 p-6">
  <header class="flex flex-wrap items-center justify-between gap-3">
    <div>
      <h1 class="text-lg font-semibold">Network</h1>
      <p class="text-xs text-muted">{scopeLabel}</p>
    </div>
    <Segmented label="Network view" value={view} onchange={(v) => (view = v as NetView)} options={VIEWS} />
  </header>

  {#if processMode}
    <Card class="border-warn text-sm" role="status" data-testid="process-banner">{PROCESS_MODE_BANNER}</Card>
  {/if}
  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}

  {#if view === 'hosts'}
    <Card class="overflow-x-auto p-0">
      <table class="w-full text-left text-sm" data-testid="hosts-table">
        <thead class="text-xs text-muted">
          <tr>
            {#each COLS as c (c.key)}
              <th scope="col" class="px-3 py-2 font-medium {c.right ? 'text-right' : ''}" aria-sort={aria(c.key)}>
                <button type="button" class="cursor-pointer hover:text-fg" onclick={() => sortBy(c.key)}>
                  {c.label}{sort.key === c.key ? (sort.dir === 'asc' ? ' ▲' : ' ▼') : ''}
                </button>
              </th>
            {/each}
          </tr>
        </thead>
        <tbody>
          {#each rows as h (h.host + (h.agentId ?? ''))}
            <tr class="border-t border-border" data-testid="host-row">
              <td class="px-3 py-2 font-mono">{h.host}</td>
              <td class="px-3 py-2">
                <span class="flex flex-wrap items-center gap-1.5">
                  <Tag tone={ruleTone(h.rule)}>{h.rule}</Tag>
                  {#if h.injected || h.rule === 'injected'}<Tag tone="info">proxy can read</Tag>{/if}
                </span>
              </td>
              <td class="px-3 py-2 text-right font-mono">{h.requests}{#if h.blocked} <span class="text-err">({h.blocked} blocked)</span>{/if}</td>
              <td class="px-3 py-2 text-right font-mono">{bytesLabel(h.bytesUp)} / {bytesLabel(h.bytesDown)}</td>
              <td class="px-3 py-2 text-right font-mono">{h.agents ?? '—'}</td>
              <td class="px-3 py-2 text-right text-muted">{agoMs(h.lastSeen, now)}</td>
            </tr>
          {:else}
            <tr><td colspan="6" class="px-3 py-4 text-sm text-muted">{loaded ? 'No network activity yet.' : 'Loading…'}</td></tr>
          {/each}
        </tbody>
      </table>
    </Card>
  {:else if view === 'requests'}
    <input aria-label="Filter by host" placeholder="Filter by host" bind:value={filter} class="rounded border border-border bg-bg p-2 text-sm" />
    <Card class="overflow-x-auto p-0">
      <table class="w-full text-left text-sm" data-testid="requests-table">
        <thead class="text-xs text-muted">
          <tr>
            <th scope="col" class="px-3 py-2 font-medium">Time</th>
            <th scope="col" class="px-3 py-2 font-medium">Agent</th>
            <th scope="col" class="px-3 py-2 font-medium">Host</th>
            <th scope="col" class="px-3 py-2 font-medium">Decision</th>
            <th scope="col" class="px-3 py-2 text-right font-medium">Bytes</th>
            <th scope="col" class="px-3 py-2 text-right font-medium">Duration</th>
          </tr>
        </thead>
        <tbody>
          {#each shown as r, i (r.at + ':' + r.agentId + ':' + r.host + ':' + i)}
            <tr class="border-t border-border" data-testid="request-row">
              <td class="px-3 py-2 font-mono text-xs text-muted">{clockTime(r.at)}</td>
              <td class="px-3 py-2 font-mono text-xs">{r.agentId}</td>
              <td class="px-3 py-2 font-mono">{r.host}{r.port && r.port !== 443 ? `:${r.port}` : ''}</td>
              <td class="px-3 py-2"><Tag tone={r.decision === 'block' ? 'err' : 'ok'}>{r.decision}</Tag>{#if r.injected} <Tag tone="info">injected</Tag>{/if}</td>
              <td class="px-3 py-2 text-right font-mono">{bytesLabel(r.bytesUp + r.bytesDown)}</td>
              <td class="px-3 py-2 text-right font-mono">{r.durationMs} ms</td>
            </tr>
          {:else}
            <tr><td colspan="6" class="px-3 py-4 text-sm text-muted">{loaded ? (filter ? 'No requests match.' : 'No requests yet.') : 'Loading…'}</td></tr>
          {/each}
        </tbody>
      </table>
    </Card>
  {:else}
    <Card class="overflow-x-auto p-0">
      <table class="w-full text-left text-sm" data-testid="agents-table">
        <thead class="text-xs text-muted">
          <tr>
            <th scope="col" class="px-3 py-2 font-medium">Agent</th>
            <th scope="col" class="px-3 py-2 text-right font-medium">Hosts</th>
            <th scope="col" class="px-3 py-2 text-right font-medium">Requests</th>
            <th scope="col" class="px-3 py-2 text-right font-medium">Blocked</th>
            <th scope="col" class="px-3 py-2 text-right font-medium">Bytes up / down</th>
            <th scope="col" class="px-3 py-2 text-right font-medium">Last seen</th>
          </tr>
        </thead>
        <tbody>
          {#each agents as a (a.agentId)}
            <tr class="border-t border-border" data-testid="agent-row">
              <td class="px-3 py-2"><a class="font-mono text-accent hover:underline" href="#chat/{a.agentId}">{a.agentId}</a></td>
              <td class="px-3 py-2 text-right font-mono">{a.hosts}</td>
              <td class="px-3 py-2 text-right font-mono">{a.requests}</td>
              <td class="px-3 py-2 text-right font-mono {a.blocked ? 'text-err' : ''}">{a.blocked}</td>
              <td class="px-3 py-2 text-right font-mono">{bytesLabel(a.bytesUp)} / {bytesLabel(a.bytesDown)}</td>
              <td class="px-3 py-2 text-right text-muted">{agoMs(a.lastSeen, now)}</td>
            </tr>
          {:else}
            <tr><td colspan="6" class="px-3 py-4 text-sm text-muted">{loaded ? 'No agents have made requests.' : 'Loading…'}</td></tr>
          {/each}
        </tbody>
      </table>
    </Card>
  {/if}

  {#if agent}
    <p class="text-xs text-muted"><a class="text-accent hover:underline" href="#chat/{agent}" onclick={(e) => { e.preventDefault(); onNavigate(`#chat/${agent}`) }}>← Back to the session</a></p>
  {/if}
</div>
