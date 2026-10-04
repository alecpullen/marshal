import type { StackState, WireCall, WireNode } from '../stack'

/** Every node reachable from the roots, depth first in display order. */
export function orderedNodes(stack: Pick<StackState, 'nodes' | 'roots'>): WireNode[] {
  const out: WireNode[] = []
  const seen = new Set<string>()
  const walk = (id: string) => {
    if (seen.has(id)) return
    seen.add(id)
    const n = stack.nodes.get(id)
    if (!n) return
    out.push(n)
    for (const c of n.children ?? []) walk(c)
  }
  for (const r of stack.roots) walk(r)
  return out
}

/** The step the agent is on now: the last live step, else the last step. */
export function liveStep(stack: Pick<StackState, 'nodes' | 'roots'>): WireNode | undefined {
  const steps = orderedNodes(stack).filter((n) => n.kind === 'step' && n.step)
  return [...steps].reverse().find((n) => n.live) ?? steps.at(-1)
}

/** The newest step of the latest turn, the only one that can show a last request. */
export function latestStepId(stack: Pick<StackState, 'nodes' | 'roots'>): string | undefined {
  const turn = [...stack.roots].reverse().map((r) => stack.nodes.get(r)).find((n) => n?.kind === 'turn')
  if (!turn) return undefined
  return orderedNodes({ nodes: stack.nodes, roots: [turn.id] })
    .filter((n) => n.kind === 'step')
    .at(-1)?.id
}

const FILE_READ_TOOLS = new Set(['file.read', 'symbols.find'])

/** A node's edit calls, i.e. those naming the files they changed. */
export function editCalls(n: WireNode): WireCall[] {
  return (n.tool?.calls ?? []).filter((c) => (c.files?.length ?? 0) > 0)
}

/** The last tool node that edited a file, in display order. */
export function latestEdit(stack: Pick<StackState, 'nodes' | 'roots'>): WireNode | undefined {
  return orderedNodes(stack)
    .filter((n) => n.kind === 'tool' && editCalls(n).length > 0)
    .at(-1)
}

/** A `path` or `path:line` target. */
export function splitTarget(target: string): { path: string; line?: number } {
  const m = /^(.*?):(\d+)(?:[:-]\d+)?$/.exec(target)
  return m ? { path: m[1], line: Number(m[2]) } : { path: target }
}

/** The file (and line, when the target carries one) a node points at. */
export function nodeFile(n: WireNode): { path: string; line?: number } | undefined {
  const calls = n.tool?.calls ?? []
  const edit = calls.find((c) => c.files?.length)
  if (edit?.files?.[0]) return { path: edit.files[0] }
  if (n.tool && FILE_READ_TOOLS.has(n.tool.name)) {
    const t = calls.find((c) => c.target)?.target ?? n.tool.running?.target
    if (t) return splitTarget(t)
  }
  return undefined
}

/** A short label for a node, for the dock header and relation links. */
export function nodeLabel(n: WireNode | undefined, id = ''): string {
  if (!n) return id
  if (n.step) return n.step.headline
  if (n.tool) return [n.tool.display, n.tool.calls?.[0]?.target ?? n.tool.running?.target].filter(Boolean).join(' ')
  if (n.task) return n.task.content
  if (n.subagent) return n.subagent.label
  if (n.message) return n.message.content.split('\n')[0].slice(0, 80)
  if (n.thinking) return 'thinking'
  if (n.runEvent) return n.runEvent.title ?? n.runEvent.kind
  if (n.jobExit) return n.jobExit.command
  return id || n.id
}

/** The subagent id behind a `sub:<id>` node key. */
export function subagentIdOf(nodeId: string): number | undefined {
  const m = /^sub:(\d+)$/.exec(nodeId)
  return m ? Number(m[1]) : undefined
}
