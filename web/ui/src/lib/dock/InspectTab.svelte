<script lang="ts">
  import Tag from '../ui/Tag.svelte'
  import Segmented from '../ui/Segmented.svelte'
  import DiffLines from '../DiffLines.svelte'
  import { glyph, toolGlyph } from '../glyphs'
  import { getLastRequest, type CallDetail, type NodeDetail, type NodeDetailResponse, type RequestJSON } from '../api'
  import type { StackState, WireCall, WireNode } from '../stack'
  import { compactDuration } from '../transcript/format'
  import { latestStepId, liveStep, nodeLabel } from './nodes'
  import type { createNodeCache } from './nodeCache'
  import type { DockState } from './dock'

  let {
    sessionId,
    subagentId,
    stack,
    dock,
    cache,
    onSelect,
  }: {
    sessionId: string
    subagentId?: number
    stack: StackState
    dock: DockState
    cache: ReturnType<typeof createNodeCache>
    onSelect: (id: string) => void
  } = $props()

  const node = $derived<WireNode | undefined>(
    dock.mode === 'select' && dock.selected ? stack.nodes.get(dock.selected) : liveStep(stack),
  )

  let detail = $state<NodeDetailResponse | 'unsupported' | 'error' | 'loading'>('loading')
  let sub = $state('output')
  let detailKey = ''

  // Refetched whenever the wire node changes; the cache makes unchanged ones free.
  // Only a different node (or transcript) resets to loading: a live node gets a
  // new object on every patch, and the previous detail stays up while it refetches.
  $effect(() => {
    const n = node
    if (!n) return
    const key = `${subagentId ?? 0}:${n.id}`
    if (key !== detailKey) {
      detailKey = key
      detail = 'loading'
    }
    let stale = false
    cache
      .get(sessionId, n.id, n, subagentId)
      .then((r) => !stale && (detail = r))
      .catch(() => !stale && (detail = 'error'))
    return () => (stale = true)
  })

  const d = $derived<NodeDetail | undefined>(typeof detail === 'object' ? detail.detail : undefined)
  const calls = $derived(d?.calls ?? [])
  const wireCalls = $derived(node?.tool?.calls ?? [])
  const hasDiff = $derived(calls.some((c) => c.diff))
  const parent = $derived(node?.parent ? stack.nodes.get(node.parent) : undefined)
  const stepInfo = $derived(node?.step ?? parent?.step)

  const tabs = $derived.by(() => {
    const t: { value: string; label: string }[] = []
    if (node?.tool) {
      t.push(hasDiff ? { value: 'diff', label: 'Diff' } : { value: 'output', label: 'Output' })
      t.push({ value: 'args', label: 'Args' })
    } else if (node?.step) {
      t.push({ value: 'narration', label: 'Narration' }, { value: 'thinking', label: 'Thinking' })
    } else {
      t.push({ value: 'output', label: 'Detail' })
    }
    return t
  })
  const active = $derived(tabs.some((t) => t.value === sub) ? sub : tabs[0]?.value)

  const failed = $derived(wireCalls.some((c) => c.failed) || calls.some((c) => c.error))
  const status = $derived(node?.live ? 'running' : failed ? 'failed' : 'done')
  const statusTone = $derived(status === 'running' ? 'accent' : status === 'failed' ? 'err' : 'ok')
  const icon = $derived(node?.tool ? toolGlyph(node.tool.name) : node?.step ? glyph.Running : node?.subagent ? glyph.Agent : glyph.Ambient)

  const time = (ms?: number) => (ms ? new Date(ms).toLocaleTimeString() : '')
  const firstCall = $derived(wireCalls[0])
  const detailCall = $derived(calls[0])
  const started = $derived(firstCall?.at ?? node?.step?.startedAt ?? node?.subagent?.startedAt)
  const duration = $derived.by(() => {
    if (firstCall?.durationMs) return compactDuration(firstCall.durationMs)
    const s = node?.step ?? node?.subagent
    return s?.startedAt && s.endedAt ? compactDuration(s.endedAt - s.startedAt) : ''
  })
  const exitCode = $derived(detailCall?.exitCode ?? firstCall?.exitCode)
  const why = $derived(((d?.narration?.[0] ?? stepInfo?.rest ?? '').split(/(?<=[.!?])\s/)[0] ?? '').trim())
  const sandbox = $derived.by(() => {
    const s = detailCall?.sandbox as { backend?: string; mode?: string } | undefined
    if (!s) return ''
    return typeof s === 'object' ? (s.backend ?? s.mode ?? JSON.stringify(s)) : String(s)
  })
  const approval = $derived([firstCall?.approval, firstCall?.risk].filter(Boolean).join(' · '))
  const hooks = $derived(detailCall?.hooks?.length ?? 0)
  const model = $derived(detailCall?.model || stepInfo?.model || node?.subagent?.model || '')

  function pretty(s?: string): string {
    if (!s) return ''
    try {
      return JSON.stringify(JSON.parse(s), null, 2)
    } catch {
      return s
    }
  }

  // The last model request: newest step of the latest turn only.
  const narration = $derived(d?.narration?.length ? d.narration : [stepInfo?.rest ?? ''].filter(Boolean))
  const thoughts = $derived(d?.thinking?.length ? d.thinking : (node?.step?.thoughts ?? []))
  const shownCalls = $derived<(CallDetail | WireCall)[]>(calls.length ? calls : wireCalls)

  const groups = $derived(
    [
      { title: 'Likely fixed by', ids: d?.relations?.fixedBy ?? [] },
      { title: 'Likely caused by', ids: d?.relations?.causedBy ?? [] },
    ].filter((g) => g.ids.length),
  )

  const showRequest = $derived(!!node && node.kind === 'step' && node.id === latestStepId(stack))
  let req = $state<RequestJSON | 'unsupported' | 'error' | null>(null)
  let reqOpen = $state(false)
  $effect(() => {
    if (!reqOpen || !showRequest) return
    req = null
    getLastRequest(sessionId).then((r) => (req = r)).catch(() => (req = 'error'))
  })
  const size = (n: number) => (n >= 1024 ? `${(n / 1024).toFixed(1)} KB` : `${n} B`)
</script>

{#if !node}
  <p class="p-4 text-sm text-muted">Nothing to inspect yet.</p>
{:else}
  <div class="flex flex-col gap-3 p-3 text-sm" data-testid="inspect">
    <div class="flex items-center gap-2">
      <span class="text-muted">{icon}</span>
      <span class="min-w-0 truncate font-medium" title={nodeLabel(node)}>{nodeLabel(node)}</span>
      <Tag tone={statusTone}>{status}</Tag>
    </div>

    {#if detail === 'unsupported' || detail === 'error'}
      <p class="rounded border border-border bg-bg p-2 text-xs text-muted">
        {detail === 'unsupported' ? 'Full detail needs a newer agent.' : 'Could not load the full detail.'}
      </p>
    {/if}

    <dl class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
      {#if stepInfo?.owner || stepInfo?.role || model}
        <dt class="text-muted">actor</dt>
        <dd class="flex items-center gap-2">
          {#if stepInfo?.owner || stepInfo?.role}<Tag role={stepInfo?.role}>{stepInfo?.owner || stepInfo?.role}</Tag>{/if}
          {#if model}<span class="font-mono text-sub">{model}</span>{/if}
        </dd>
      {/if}
      {#if why}<dt class="text-muted">why</dt><dd>{why}</dd>{/if}
      {#if started}<dt class="text-muted">started</dt><dd class="font-mono">{time(started)}</dd>{/if}
      {#if duration}<dt class="text-muted">duration</dt><dd class="font-mono">{duration}</dd>{/if}
      {#if exitCode !== undefined}<dt class="text-muted">exit code</dt><dd class="font-mono">{exitCode}</dd>{/if}
      {#if sandbox}<dt class="text-muted">sandbox</dt><dd class="font-mono">{sandbox}</dd>{/if}
      {#if approval}<dt class="text-muted">approval</dt><dd>{approval}</dd>{/if}
      {#if hooks}<dt class="text-muted">hooks</dt><dd>{hooks}</dd>{/if}
      {#if firstCall?.callId}<dt class="text-muted">call</dt><dd class="truncate font-mono">{firstCall.callId}</dd>{/if}
    </dl>

    <Segmented label="Inspect view" value={active ?? ''} onchange={(v) => (sub = v)} options={tabs} />

    <div data-testid="inspect-body">
      {#if active === 'diff'}
        {#each calls.filter((c) => c.diff) as c, i (i)}<DiffLines diff={c.diff ?? ''} />{/each}
      {:else if active === 'output'}
        {#if node.tool}
          {#each shownCalls as c, i (i)}
            {#if calls.length > 1 || wireCalls.length > 1}<div class="mt-2 font-mono text-[11px] text-muted">{('target' in c && c.target) || ''}</div>{/if}
            <pre class="max-h-96 overflow-auto rounded bg-bg p-2 font-mono text-xs whitespace-pre-wrap">{c.error || c.output || '(no output)'}</pre>
            {#if 'truncated' in c && c.truncated && detail !== 'unsupported'}<p class="text-[11px] text-muted">output truncated</p>{/if}
          {/each}
          {#if detail === 'unsupported' && wireCalls.some((c) => c.truncated)}<p class="text-[11px] text-muted">Output is capped on this agent.</p>{/if}
        {:else}
          <pre class="max-h-96 overflow-auto rounded bg-bg p-2 font-mono text-xs whitespace-pre-wrap">{node.message?.content ?? node.task?.content ?? node.subagent?.summary ?? node.jobExit?.output ?? node.runEvent?.body ?? nodeLabel(node)}</pre>
        {/if}
      {:else if active === 'args'}
        {#each shownCalls as c, i (i)}
          <pre class="max-h-96 overflow-auto rounded bg-bg p-2 font-mono text-xs whitespace-pre-wrap">{pretty(c.args) || '(none)'}</pre>
        {/each}
      {:else if active === 'narration'}
        {#each narration as t, i (i)}<p class="mb-2 text-xs">{t}</p>{:else}<p class="text-xs text-muted">No narration.</p>{/each}
      {:else if active === 'thinking'}
        {#each thoughts as t, i (i)}
          <pre class="mb-2 overflow-auto rounded bg-bg p-2 text-xs whitespace-pre-wrap text-muted">{t.text}</pre>
        {:else}<p class="text-xs text-muted">No thinking recorded.</p>{/each}
      {/if}
    </div>

    {#if groups.length}
      <div class="flex flex-col gap-1 text-xs" data-testid="relations">
        {#each groups as g (g.title)}
          <div class="text-muted">{g.title}</div>
          {#each g.ids as id (id)}
            <button type="button" class="truncate text-left text-accent hover:underline" onclick={() => onSelect(id)}>{nodeLabel(stack.nodes.get(id), id)}</button>
          {/each}
        {/each}
      </div>
    {/if}

    {#if showRequest}
      <details class="rounded border border-border text-xs" ontoggle={(e) => (reqOpen = (e.currentTarget as HTMLDetailsElement).open)}>
        <summary class="cursor-pointer px-2 py-1.5 text-sub">Last model request</summary>
        <div class="flex flex-col gap-2 border-t border-border p-2" data-testid="last-request">
          {#if req === null}
            <span class="text-muted">loading…</span>
          {:else if req === 'unsupported' || req === 'error'}
            <span class="text-muted">{req === 'unsupported' ? 'Needs a newer agent.' : 'Could not load the request.'}</span>
          {:else}
            <div class="font-mono">{[req.provider, req.model].filter(Boolean).join(' / ')}</div>
            <div class="text-muted">
              {[
                req.options.thinking && `thinking ${req.options.thinking}`,
                req.options.streaming ? 'streaming' : 'not streaming',
                req.options.maxTokens && `max ${req.options.maxTokens} tokens`,
              ].filter(Boolean).join(' · ')}
            </div>
            {#if req.packKnown && req.packTokens}<div>pack {req.packTokens.toLocaleString()} / {(req.packWindow ?? 0).toLocaleString()} tokens</div>{/if}
            <div>outcome: <span class={req.outcome.status === 'ok' ? 'text-ok' : 'text-err'}>{req.outcome.status}</span>{req.outcome.err ? ` — ${req.outcome.err}` : ''}</div>
            <div class="text-muted">messages ({req.messages.length})</div>
            {#each req.messages as m, i (i)}
              <details><summary class="cursor-pointer"><span class="font-mono">{m.role}</span> <span class="text-muted">{size(m.content.length)}</span></summary>
                <pre class="max-h-60 overflow-auto rounded bg-bg p-2 whitespace-pre-wrap">{m.content}</pre></details>
            {/each}
            <div class="text-muted">tools ({req.tools.length})</div>
            {#each req.tools as t (t.name)}<div><span class="font-mono">{t.name}</span> <span class="text-muted">{t.description ?? ''}</span></div>{/each}
          {/if}
        </div>
      </details>
    {/if}
  </div>
{/if}
