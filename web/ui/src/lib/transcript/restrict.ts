import type { WireNode } from '../stack'

/**
 * Narrows a transcript tree to some nodes: those named, their ancestors
 * (so the rows keep their turn and task around them) and their
 * descendants (a step's tool rows). Everything else is dropped from the
 * roots and from every `children` list; nodes are copied, never mutated.
 */
export function restrict(
  tree: { nodes: Map<string, WireNode>; roots: string[] },
  only: ReadonlySet<string>,
): { nodes: Map<string, WireNode>; roots: string[] } {
  const keep = new Set<string>()
  const down = (id: string) => {
    if (keep.has(id)) return
    keep.add(id)
    for (const c of tree.nodes.get(id)?.children ?? []) down(c)
  }
  for (const id of only) {
    if (!tree.nodes.has(id)) continue
    down(id)
    for (let p = tree.nodes.get(id)?.parent; p && !keep.has(p); p = tree.nodes.get(p)?.parent) keep.add(p)
  }
  const nodes = new Map<string, WireNode>()
  for (const id of keep) {
    const n = tree.nodes.get(id)
    if (n) nodes.set(id, n.children ? { ...n, children: n.children.filter((c) => keep.has(c)) } : n)
  }
  return { nodes, roots: tree.roots.filter((r) => keep.has(r)) }
}
