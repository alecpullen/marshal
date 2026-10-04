import type { WatchSample } from '../api'

export const DAY_MS = 24 * 60 * 60 * 1000

export interface SparkPoint { x: number; y: number; value: number; tripped: boolean }
export interface SparkGeometry { points: SparkPoint[]; path: string; thresholdY: number | null }

/**
 * Plots the last 24h of samples into a `w` by `h` box, time left to right and
 * `now` at the right edge. The vertical scale spans the samples and, when
 * given, the threshold, so the threshold line is always in frame. A flat
 * series draws mid-height rather than dividing by zero. SVG y runs downward.
 */
export function sparkGeometry(samples: WatchSample[], w: number, h: number, opts: { now?: number; threshold?: number | null; pad?: number } = {}): SparkGeometry {
  const now = opts.now ?? Date.now()
  const pad = opts.pad ?? 2
  const start = now - DAY_MS
  const recent = samples.filter((s) => s.at >= start && s.at <= now).sort((a, b) => a.at - b.at)
  const threshold = opts.threshold ?? null
  const values = recent.map((s) => s.value)
  if (threshold !== null) values.push(threshold)
  const lo = values.length ? Math.min(...values) : 0
  const hi = values.length ? Math.max(...values) : 0
  const y = (v: number) => (hi === lo ? h / 2 : pad + (1 - (v - lo) / (hi - lo)) * (h - 2 * pad))
  const points = recent.map((s) => ({ x: ((s.at - start) / DAY_MS) * w, y: y(s.value), value: s.value, tripped: s.tripped }))
  // A lone point still needs a segment for the path to render as a dot.
  const path = points.length === 0 ? '' : points.length === 1 ? `M${r(points[0].x)} ${r(points[0].y)}L${r(points[0].x)} ${r(points[0].y)}` : points.map((p, i) => `${i === 0 ? 'M' : 'L'}${r(p.x)} ${r(p.y)}`).join('')
  return { points, path, thresholdY: threshold === null || recent.length === 0 ? null : y(threshold) }
}

const r = (n: number) => Math.round(n * 100) / 100

/** An SVG path for the last 24h of samples; '' when there are none. */
export function sparkPath(samples: WatchSample[], w: number, h: number, now?: number): string {
  return sparkGeometry(samples, w, h, { now }).path
}

/**
 * The numeric operand of a condition worth drawing as a threshold line:
 * `exit_code N`, or `json <path> <op> <number>`. Anything else, including a
 * json comparison against text, has none.
 */
export function thresholdOf(condition: string | undefined): number | null {
  const parts = (condition ?? '').trim().split(/\s+/)
  const num = (s: string | undefined) => (s !== undefined && /^-?\d+(\.\d+)?$/.test(s) ? Number(s) : null)
  if (parts[0] === 'exit_code' && parts.length === 2) return num(parts[1])
  if (parts[0] === 'json' && parts.length === 4) return num(parts[3])
  return null
}
