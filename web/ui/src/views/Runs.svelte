<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Tag from '../lib/ui/Tag.svelte'
  import Segmented from '../lib/ui/Segmented.svelte'
  import NewRunModal from '../lib/runs/NewRunModal.svelte'
  import { listRuns, errMessage, type ProjectStatus, type RunRow } from '../lib/api'
  import type { AgentRow } from '../lib/fleet'
  import { mergeRuns } from '../lib/runs/list'
  import { matchesFilter, summarize, type RunFilter } from '../lib/runs/model'
  import { compactDuration } from '../lib/transcript/format'

  let {
    agents,
    projects,
    onNavigate,
  }: {
    agents: AgentRow[]
    projects: ProjectStatus[]
    onNavigate: (hash: string) => void
  } = $props()

  let listed = $state<RunRow[]>([])
  let error = $state('')
  let loaded = $state(false)
  let filter = $state<RunFilter>('all')
  let creating = $state(false)
  let now = $state(Date.now())

  async function load() {
    try {
      listed = await listRuns()
      error = ''
    } catch (e) {
      error = errMessage(e)
    } finally {
      loaded = true
    }
  }
  onMount(() => void load())

  const rows = $derived(
    mergeRuns(listed, agents).flatMap((r) => {
      const agent = agents.find((a) => a.id === r.agentId)
      const s = summarize(r.run, !!agent?.pending)
      return s ? [{ row: r, s }] : []
    }),
  )
  const shown = $derived(rows.filter((x) => matchesFilter(x.s, filter)))

  // Elapsed time moves only while something runs.
  $effect(() => {
    if (!rows.some((x) => x.s.running)) return
    now = Date.now()
    const t = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(t)
  })

  const elapsed = (start?: number, end?: number) => (start ? compactDuration(Math.max(0, (end || now) - start)) : '')
</script>

<div class="mx-auto flex max-w-5xl flex-col gap-4 p-6">
  <header class="flex flex-wrap items-center justify-between gap-3">
    <h1 class="text-lg font-semibold">Runs</h1>
    <div class="flex items-center gap-3">
      <Segmented
        label="Filter runs"
        value={filter}
        onchange={(v) => (filter = v as RunFilter)}
        options={[
          { value: 'all', label: 'All' },
          { value: 'running', label: 'Running' },
          { value: 'finished', label: 'Finished' },
          { value: 'needs', label: 'Needs you' },
        ]}
      />
      <Button onclick={() => (creating = true)}>New run</Button>
    </div>
  </header>

  {#if error}<Card class="border-attention text-sm">{error}</Card>{/if}

  <div class="flex flex-col gap-2">
    {#each shown as { row, s } (row.agentId)}
      <button
        class="flex w-full cursor-pointer flex-col gap-2 rounded-lg border border-border bg-surface p-3 text-left hover:bg-hover"
        data-testid="run-row"
        onclick={() => onNavigate(`#runs/${encodeURIComponent(row.agentId)}`)}
      >
        <div class="flex items-center gap-2">
          <span class="truncate text-sm font-medium">{row.name || row.agentId}</span>
          <span class="truncate text-xs text-muted">· {s.title}</span>
          <span class="flex-1"></span>
          {#if s.needsYou}<Tag tone="warn">needs you</Tag>{/if}
          <Tag tone={s.running ? 'ok' : s.phase === 'failed' ? 'err' : 'neutral'}>{s.phase || (s.running ? 'running' : 'finished')}</Tag>
          <span class="font-mono text-xs text-muted">{elapsed(s.startedAt, s.endedAt)}</span>
        </div>
        <div class="flex items-center gap-2">
          <div
            class="h-1.5 flex-1 overflow-hidden rounded-full bg-line"
            role="progressbar"
            aria-valuemin="0"
            aria-valuemax={s.total}
            aria-valuenow={s.done}
            aria-label="Progress"
          >
            <div class="h-full bg-accent" style="width: {s.total ? (s.done / s.total) * 100 : 0}%"></div>
          </div>
          <span class="font-mono text-xs text-muted">{s.done}/{s.total}</span>
        </div>
      </button>
    {:else}
      {#if loaded}
        <p class="text-sm text-muted">{rows.length === 0 ? 'No runs yet. Start one with New run.' : 'No runs match this filter.'}</p>
      {/if}
    {/each}
  </div>
</div>

{#if creating}
  <NewRunModal
    {agents}
    {projects}
    onClose={() => (creating = false)}
    onStarted={(id) => {
      creating = false
      void load()
      onNavigate(`#runs/${encodeURIComponent(id)}`)
    }}
  />
{/if}
