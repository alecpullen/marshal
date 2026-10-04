<script lang="ts">
  import { roleBars } from './model'
  import type { WireNode } from '../stack'
  import { compactDuration } from '../transcript/format'
  import { roleTone } from '../glyphs'

  export interface Sample { t: number; tokens: number }

  let {
    nodes,
    startedAt,
    endedAt,
    now,
    samples,
    tokensUsed,
    tokensMax,
    onSelectStep,
  }: {
    nodes: Map<string, WireNode>
    startedAt?: number
    endedAt?: number
    now: number
    samples: Sample[]
    tokensUsed: number
    tokensMax: number
    onSelectStep?: (id: string) => void
  } = $props()

  // Without a run start (a swarm), the range opens at the first step.
  const from = $derived(
    startedAt ||
      Math.min(now, ...[...nodes.values()].flatMap((n) => (n.kind === 'step' && n.step?.startedAt ? [n.step.startedAt] : []))),
  )
  const to = $derived(Math.max(endedAt || now, from + 1000))
  const span = $derived(to - from)
  const bars = $derived(roleBars(nodes.values(), from, to, now))
  const pct = (t: number) => ((t - from) / span) * 100
  const ticks = $derived([0, 0.25, 0.5, 0.75, 1].map((f) => ({ f, label: compactDuration(span * f) })))

  const toneClass: Record<string, string> = { gold: 'bg-gold', violet: 'bg-violet', info: 'bg-info', neutral: 'bg-muted' }

  // The curve: where the run began, every delta seen while the page was open, and now.
  const points = $derived.by(() => {
    const pts = [{ t: from, tokens: 0 }, ...samples.filter((s) => s.t > from && s.t < to), { t: Math.min(now, to), tokens: tokensUsed }]
    return pts.sort((a, b) => a.t - b.t)
  })
  const yMax = $derived(Math.max(tokensMax, tokensUsed, 1))
  const poly = $derived(points.map((p) => `${pct(p.t).toFixed(2)},${(100 - (p.tokens / yMax) * 100).toFixed(2)}`).join(' '))
</script>

<div class="flex flex-col gap-4" data-testid="timeline">
  <div class="flex flex-col gap-1.5">
    {#each bars as b (b.role)}
      <div class="flex items-center gap-2">
        <span class="w-36 shrink-0 truncate font-mono text-xs text-muted" title={b.role}>{b.role}</span>
        <div class="relative h-5 flex-1 rounded bg-surface">
          {#each b.segments as s (s.stepId)}
            <button
              type="button"
              class="absolute inset-y-0.5 min-w-px cursor-pointer rounded-sm {toneClass[roleTone(b.role)] ?? 'bg-muted'} opacity-80 hover:opacity-100"
              style="left: {pct(s.start)}%; width: {Math.max(pct(s.end) - pct(s.start), 0.3)}%"
              aria-label="{b.role} step"
              data-testid="bar"
              onclick={() => onSelectStep?.(s.stepId)}
            ></button>
          {/each}
        </div>
      </div>
    {:else}
      <p class="text-sm text-muted">No role activity yet.</p>
    {/each}
    <div class="ml-[9.5rem] flex justify-between font-mono text-[11px] text-dim" aria-hidden="true">
      {#each ticks as t (t.f)}<span>{t.label}</span>{/each}
    </div>
  </div>

  <div class="ml-[9.5rem]">
    <div class="mb-1 flex justify-between text-xs text-muted">
      <span>Tokens</span>
      <span class="font-mono">{tokensUsed.toLocaleString()}{tokensMax ? ` / ${tokensMax.toLocaleString()}` : ''}</span>
    </div>
    <svg class="h-24 w-full rounded bg-surface" viewBox="0 0 100 100" preserveAspectRatio="none" role="img" aria-label="Cumulative tokens over time">
      <polyline points={poly} fill="none" stroke="var(--color-accent)" stroke-width="1.5" vector-effect="non-scaling-stroke" data-testid="token-line" />
    </svg>
  </div>
</div>
