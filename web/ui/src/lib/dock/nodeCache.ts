import { getNode, type NodeDetailResponse } from '../api'
import type { WireNode } from '../stack'

type Fetch = (sessionId: string, nodeId: string, subagentId?: number) => Promise<NodeDetailResponse | 'unsupported'>

const MAX_ENTRIES = 100

/** FNV-1a over the string's UTF-16 units; enough to tell wire versions apart. */
export function hash(s: string): string {
  let h = 0x811c9dc5
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  return (h >>> 0).toString(16)
}

/**
 * Full node detail, cached per node and wire version: a node whose wire JSON
 * changed is fetched again. Holds at most 100 entries, least recently used
 * out first.
 */
export function createNodeCache(fetch: Fetch = getNode) {
  const cache = new Map<string, Promise<NodeDetailResponse | 'unsupported'>>()

  function get(sessionId: string, nodeId: string, wireNode: WireNode, subagentId?: number) {
    const key = `${subagentId ?? 0}:${nodeId}:${hash(JSON.stringify(wireNode))}`
    const hit = cache.get(key)
    if (hit) {
      cache.delete(key)
      cache.set(key, hit)
      return hit
    }
    const p = fetch(sessionId, nodeId, subagentId)
    // A failed fetch must not stick.
    p.catch(() => cache.delete(key))
    cache.set(key, p)
    while (cache.size > MAX_ENTRIES) cache.delete(cache.keys().next().value as string)
    return p
  }

  return { get, size: () => cache.size }
}
