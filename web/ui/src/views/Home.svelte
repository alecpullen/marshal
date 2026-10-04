<script lang="ts">
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Tag from '../lib/ui/Tag.svelte'
  import Segmented from '../lib/ui/Segmented.svelte'
  import QuestionModal from '../lib/QuestionModal.svelte'
  import DiskPanel from '../lib/DiskPanel.svelte'
  import ActivityFeed from '../lib/ActivityFeed.svelte'
  import { buildInbox } from '../lib/inbox'
  import { describePending, toPendingQuestion, type AgentRow } from '../lib/fleet'
  import {
    APIError,
    approvePending,
    denyPending,
    resolvePermission,
    resolveQuestion,
    type Answers,
    type PendingSubmission,
  } from '../lib/api'
  import { shortName } from '../lib/utils'

  let {
    agents,
    pending,
    onRefreshPending,
    onOpenAgent,
    onNavigate,
  }: {
    agents: AgentRow[]
    pending: PendingSubmission[]
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

  const originLetter = (origin?: string) =>
    ({ ui: 'U', cli: 'C', mcp: 'M', issue: '#' })[origin ?? ''] ?? (origin ? origin[0].toUpperCase() : '·')

  let notice = $state<string | null>(null)
  let answering = $state<AgentRow | null>(null)

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

  const approvePermission = (a: AgentRow) => a.pending && run(() => resolvePermission(a.pending!.id, { approved: true }))
  const denyPermission = (a: AgentRow) => a.pending && run(() => resolvePermission(a.pending!.id, { approved: false }))

  const answerPending = $derived(answering?.pending?.kind === 'question' ? toPendingQuestion(answering.id, answering.pending) : null)

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

  <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_18rem]">
    <div class="flex flex-col gap-6">
      <section aria-labelledby="inbox-needs">
        <h2 id="inbox-needs" class="mb-2 text-xs tracking-wide text-attention uppercase">Needs you · {inbox.needsYou.length}</h2>
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
                {#if a.pending?.kind === 'approval'}
                  <Button onclick={() => approvePermission(a)}>Approve</Button>
                  <Button variant="danger" onclick={() => denyPermission(a)}>Deny</Button>
                {:else}
                  <Button onclick={() => (answering = a)}>Answer</Button>
                  <Button variant="ghost" onclick={() => onNavigate(`#chat/${a.id}`)}>Open</Button>
                {/if}
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
          {:else}
            <p class="text-sm text-muted">Nothing is waiting on you.</p>
          {/each}
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

{#if answering && answerPending}
  <QuestionModal
    question={answerPending}
    onResolve={(ans: Answers) => {
      const q = answerPending.questionId
      answering = null
      run(() => resolveQuestion(q, ans))
    }}
    onDecline={() => {
      const q = answerPending.questionId
      answering = null
      run(() => resolveQuestion(q, { declined: true }))
    }}
  />
{/if}
