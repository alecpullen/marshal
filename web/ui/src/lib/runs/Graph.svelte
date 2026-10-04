<script lang="ts">
  import type { RunTask } from '../api'
  import { glyph } from '../glyphs'
  import { activeStage, criticalPath, layout, pathEdges, type StageKey } from './model'

  let {
    tasks,
    now,
    selected = null,
    onSelect,
  }: {
    tasks: RunTask[]
    now: number
    selected?: { task: number; stage: StageKey } | null
    onSelect: (s: { task: number; stage: StageKey }) => void
  } = $props()

  const W = 180
  const H = 56
  const GX = 60
  const GY = 24
  const PAD = 24

  const g = $derived(layout(tasks))
  const byN = $derived(new Map(tasks.map((t) => [t.n, t])))
  const pos = $derived(new Map(g.nodes.map((n) => [n.n, { x: PAD + n.layer * (W + GX), y: PAD + n.index * (H + GY) }])))
  const critical = $derived(pathEdges(criticalPath(tasks, now)))
  const width = $derived(PAD * 2 + (Math.max(0, ...g.nodes.map((n) => n.layer)) + 1) * (W + GX) - GX)
  const height = $derived(PAD * 2 + (Math.max(0, ...g.nodes.map((n) => n.index)) + 1) * (H + GY) - GY)

  const edgePath = (from: number, to: number) => {
    const a = pos.get(from)!
    const b = pos.get(to)!
    const x1 = a.x + W
    const y1 = a.y + H / 2
    const x2 = b.x
    const y2 = b.y + H / 2
    const dx = (x2 - x1) / 2
    return `M${x1},${y1} C${x1 + dx},${y1} ${x2 - dx},${y2} ${x2},${y2}`
  }

  const stroke: Record<string, string> = { done: 'var(--color-ok)', failed: 'var(--color-err)', active: 'var(--color-accent)', pending: 'var(--color-line)' }
  const mark: Record<string, string> = { done: glyph.OK, failed: glyph.Error, active: glyph.Running, pending: glyph.Ambient }
  const trunc = (s: string, n = 22) => (s.length > n ? s.slice(0, n - 1) + '…' : s)

  // Pan by dragging the background and zoom with the wheel.
  let view = $state({ x: 0, y: 0, k: 1 })
  let drag: { px: number; py: number; x: number; y: number } | null = null
  function down(e: PointerEvent) {
    if ((e.target as Element).closest('[data-node]')) return
    drag = { px: e.clientX, py: e.clientY, x: view.x, y: view.y }
    ;(e.currentTarget as Element).setPointerCapture?.(e.pointerId)
  }
  function move(e: PointerEvent) {
    if (drag) view = { ...view, x: drag.x + e.clientX - drag.px, y: drag.y + e.clientY - drag.py }
  }
  const up = () => (drag = null)
  function wheel(e: WheelEvent) {
    e.preventDefault()
    const k = Math.min(2.5, Math.max(0.4, view.k * (e.deltaY < 0 ? 1.1 : 1 / 1.1)))
    view = { ...view, k }
  }
</script>

<svg
  class="h-[28rem] w-full cursor-grab touch-none rounded-lg border border-border bg-surface select-none active:cursor-grabbing"
  role="group"
  aria-label="Task graph"
  data-testid="graph"
  viewBox="0 0 {Math.max(width, 480)} {Math.max(height, 160)}"
  onpointerdown={down}
  onpointermove={move}
  onpointerup={up}
  onpointercancel={up}
  onwheel={wheel}
>
  <g transform="translate({view.x} {view.y}) scale({view.k})">
    {#each g.edges as e (`${e.from}>${e.to}`)}
      {@const crit = critical.has(`${e.from}>${e.to}`)}
      <path
        d={edgePath(e.from, e.to)}
        fill="none"
        stroke={crit ? 'var(--color-accent)' : 'var(--color-dim)'}
        stroke-width={crit ? 2 : 1.25}
        stroke-dasharray={crit ? '6 4' : undefined}
        data-critical={crit ? 'true' : undefined}
        data-testid="edge"
      />
    {/each}
    {#each g.nodes as n (n.n)}
      {@const t = byN.get(n.n)!}
      {@const p = pos.get(n.n)!}
      <g
        data-node={n.n}
        transform="translate({p.x} {p.y})"
        class="cursor-pointer"
        role="button"
        tabindex="0"
        aria-label="Task {t.n}: {t.title}, {t.status}"
        onclick={() => onSelect({ task: t.n, stage: activeStage(t) })}
        onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && onSelect({ task: t.n, stage: activeStage(t) })}
      >
        <rect
          width={W}
          height={H}
          rx="8"
          fill="var(--color-raise)"
          stroke={selected?.task === t.n ? 'var(--color-accent)' : (stroke[t.status] ?? stroke.pending)}
          stroke-width={selected?.task === t.n ? 2 : 1.25}
        />
        <text x="10" y="21" font-size="12" fill="var(--color-fg)">{n.n}  {trunc(t.title)}</text>
        <text x="10" y="41" font-size="11" fill="var(--color-muted)" font-family="var(--font-mono)">
          {mark[t.status] ?? glyph.Ambient} {t.status}{t.status === 'active' ? ` · ${activeStage(t)}` : ''}{t.fixRounds ? ` · ↻${t.fixRounds}` : ''}
        </text>
      </g>
    {/each}
  </g>
</svg>
