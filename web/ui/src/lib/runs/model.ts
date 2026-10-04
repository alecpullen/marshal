/*
  Pure helpers behind the Runs page (spec §6.2): lanes, the dependency
  graph layout, the critical path and the timeline's role bars. Nothing
  here touches the DOM or the network, so the views stay thin.
*/
import type { RunTask, SDDRun } from '../api'
import type { WireNode } from '../stack'

export const STAGES = ['implement', 'verify', 'review', 'commit'] as const
export type StageKey = (typeof STAGES)[number]

export interface LaneCell {
  state: string
  detail?: string
  fixRounds?: number
  sha?: string
}
export interface Lane {
  n: number
  title: string
  deps: number[]
  status: string
  cells: Record<StageKey, LaneCell>
}

const PENDING: LaneCell = { state: 'pending' }

/** One row per task; each cell comes straight from the task's stage list. */
export function lanes(run: SDDRun): Lane[] {
  return (run.tasks ?? []).map((t) => {
    const cells = {} as Record<StageKey, LaneCell>
    for (const key of STAGES) {
      const s = t.stages?.find((x) => x.name.toLowerCase() === key)
      cells[key] = s ? { state: s.state, ...(s.detail ? { detail: s.detail } : {}) } : { ...PENDING }
    }
    // Fix rounds are the loop between review and implement; the review cell carries them.
    if (t.fixRounds > 0) cells.review.fixRounds = t.fixRounds
    if (t.commit?.head) cells.commit.sha = t.commit.head.slice(0, 7)
    return { n: t.n, title: t.title, deps: t.dependsOn ?? [], status: t.status, cells }
  })
}

export interface GraphNode { n: number; layer: number; index: number }
export interface GraphEdge { from: number; to: number }

/**
 * A layered DAG layout: a node's layer is the length of its longest path
 * from a root, and nodes within a layer are ordered by task number.
 * Dependencies on unknown tasks are ignored; a cycle (which a valid plan
 * cannot have) is cut rather than looped on.
 */
export function layout(tasks: { n: number; dependsOn: number[] }[]): { nodes: GraphNode[]; edges: GraphEdge[] } {
  const byN = new Map(tasks.map((t) => [t.n, t]))
  const layerOf = new Map<number, number>()
  const visiting = new Set<number>()
  const depth = (n: number): number => {
    const known = layerOf.get(n)
    if (known !== undefined) return known
    if (visiting.has(n)) return 0
    visiting.add(n)
    let layer = 0
    for (const d of byN.get(n)?.dependsOn ?? []) if (byN.has(d)) layer = Math.max(layer, depth(d) + 1)
    visiting.delete(n)
    layerOf.set(n, layer)
    return layer
  }
  const ordered = [...tasks].sort((a, b) => a.n - b.n)
  const perLayer = new Map<number, number>()
  const nodes = ordered.map((t) => {
    const layer = depth(t.n)
    const index = perLayer.get(layer) ?? 0
    perLayer.set(layer, index + 1)
    return { n: t.n, layer, index }
  })
  const edges: GraphEdge[] = []
  for (const t of ordered) for (const d of t.dependsOn ?? []) if (byN.has(d)) edges.push({ from: d, to: t.n })
  return { nodes, edges }
}

const duration = (t: RunTask, now: number): number => {
  if (!t.startedAt) return 0
  return Math.max(0, (t.endedAt || now) - t.startedAt)
}

/**
 * The chain of task numbers with the largest summed duration. An
 * unfinished task counts its elapsed time so far, a pending one counts 0.
 * Ties go to the lower task number so the answer is stable.
 */
export function criticalPath(tasks: RunTask[], now: number): number[] {
  const byN = new Map(tasks.map((t) => [t.n, t]))
  const best = new Map<number, { total: number; prev?: number }>()
  const visiting = new Set<number>()
  const solve = (n: number): number => {
    const known = best.get(n)
    if (known) return known.total
    const t = byN.get(n)!
    if (visiting.has(n)) return duration(t, now)
    visiting.add(n)
    let prev: number | undefined
    let prevTotal = 0
    for (const d of [...(t.dependsOn ?? [])].sort((a, b) => a - b)) {
      if (!byN.has(d)) continue
      const total = solve(d)
      if (prev === undefined || total > prevTotal) {
        prev = d
        prevTotal = total
      }
    }
    visiting.delete(n)
    const total = duration(t, now) + prevTotal
    best.set(n, { total, prev })
    return total
  }
  let end: number | undefined
  let endTotal = -1
  for (const t of [...tasks].sort((a, b) => a.n - b.n)) {
    const total = solve(t.n)
    if (total > endTotal) {
      end = t.n
      endTotal = total
    }
  }
  const chain: number[] = []
  const seen = new Set<number>()
  for (let n = end; n !== undefined && !seen.has(n); n = best.get(n)?.prev) {
    seen.add(n)
    chain.unshift(n)
  }
  return chain
}

/** The edges that lie on a chain, for drawing them dashed. */
export const pathEdges = (chain: number[]): Set<string> =>
  new Set(chain.slice(1).map((n, i) => `${chain[i]}>${n}`))

export interface RoleSegment { start: number; end: number; stepId: string }
export interface RoleBar { role: string; segments: RoleSegment[] }

/**
 * Step nodes with a role, grouped by role and clipped to [from, to]. An
 * open step runs to `now`. Steps entirely outside the range are dropped.
 */
export function roleBars(nodes: Iterable<WireNode>, from: number, to: number, now: number = Date.now()): RoleBar[] {
  const byRole = new Map<string, RoleSegment[]>()
  for (const n of nodes) {
    const s = n.step
    if (n.kind !== 'step' || !s?.role || !s.startedAt) continue
    const start = Math.max(s.startedAt, from)
    const end = Math.min(s.endedAt || now, to)
    if (end <= start) continue
    const list = byRole.get(s.role) ?? []
    list.push({ start, end, stepId: n.id })
    byRole.set(s.role, list)
  }
  return [...byRole.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([role, segments]) => ({ role, segments: segments.sort((a, b) => a.start - b.start) }))
}

/** The role a stage's steps run under; verify and commit have none of their own. */
export const STAGE_ROLE: Record<StageKey, string | null> = {
  implement: 'sdd_implementer',
  review: 'sdd_reviewer',
  verify: null,
  commit: null,
}

const SHELL_PREFIXES = ['shell.', 'test.']

/**
 * The step IDs a selected task stage covers: steps whose role matches the
 * stage and which started inside the task's window. Verify has no role of
 * its own, so it takes steps in the window that ran shell-family tools;
 * commit has no steps at all.
 */
export function stageSteps(nodes: Map<string, WireNode>, task: RunTask, stage: StageKey, now: number): string[] {
  if (stage === 'commit' || !task.startedAt) return []
  const end = task.endedAt || now
  const role = STAGE_ROLE[stage]
  const out: string[] = []
  for (const n of nodes.values()) {
    const s = n.step
    if (n.kind !== 'step' || !s?.startedAt) continue
    if (s.startedAt < task.startedAt || s.startedAt > end) continue
    if (role) {
      if (s.role === role) out.push(n.id)
    } else {
      const tools = (n.children ?? []).map((c) => nodes.get(c)).filter((c): c is WireNode => !!c?.tool)
      if (tools.some((c) => SHELL_PREFIXES.some((p) => c.tool!.name.startsWith(p)))) out.push(n.id)
    }
  }
  return out.sort((a, b) => (nodes.get(a)!.step!.startedAt ?? 0) - (nodes.get(b)!.step!.startedAt ?? 0))
}

/** Which task a stage selection falls on; the stage the controller is in when a node is clicked. */
export function activeStage(task: RunTask): StageKey {
  const s = task.stages?.find((x) => x.state === 'active' || x.state === 'failed')
  const key = s?.name.toLowerCase() as StageKey | undefined
  if (key && (STAGES as readonly string[]).includes(key)) return key
  const last = [...(task.stages ?? [])].reverse().find((x) => x.state === 'done')
  return (last?.name.toLowerCase() as StageKey | undefined) ?? 'implement'
}

export interface RunSummary {
  title: string
  kind: 'sdd' | 'swarm'
  done: number
  total: number
  phase: string
  running: boolean
  needsYou: boolean
  startedAt?: number
  endedAt?: number
  gateQuestion?: string
}

/** What a list row or a tile shows about a run; null for an agent with no run. */
export function summarize(run: { kind: string; sdd?: SDDRun; swarm?: import('../api').SwarmRun } | undefined, pending = false): RunSummary | null {
  if (!run) return null
  if (run.kind === 'sdd' && run.sdd) {
    const r = run.sdd
    const running = r.active && !r.finished
    return {
      title: r.planName || r.planPath || 'Plan run',
      kind: 'sdd',
      done: r.doneTasks,
      total: r.totalTasks,
      phase: r.finished ? (r.succeeded ? 'done' : 'failed') : r.gate ? 'waiting on you' : (r.phase ?? ''),
      running,
      needsYou: !!r.gate || (pending && running),
      startedAt: r.startedAt,
      endedAt: r.endedAt,
      gateQuestion: r.gate?.question,
    }
  }
  if (run.kind === 'swarm' && run.swarm) {
    const r = run.swarm
    const roles = r.roles ?? []
    return {
      title: r.goal || 'Swarm run',
      kind: 'swarm',
      done: roles.filter((x) => x.status === 'done').length,
      total: roles.length,
      phase: r.active ? 'running' : 'finished',
      running: r.active,
      needsYou: pending && r.active,
    }
  }
  return null
}

export type RunFilter = 'all' | 'running' | 'finished' | 'needs'

export function matchesFilter(s: RunSummary, f: RunFilter): boolean {
  if (f === 'running') return s.running
  if (f === 'finished') return !s.running
  if (f === 'needs') return s.needsYou
  return true
}
