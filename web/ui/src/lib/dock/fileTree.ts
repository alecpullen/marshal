import { writable, get, type Readable } from 'svelte/store'
import { listFiles, type FileList } from '../api'

export type DirEntry = FileList['entries'][number]
/** A directory's state: its entries, still loading, or why it failed. */
export type DirState = DirEntry[] | 'loading' | Error | 'unsupported'

type Lister = (agentId: string, path: string) => Promise<FileList | 'unsupported'>

export interface FileTree extends Readable<Map<string, DirState>> {
  /** Expands or collapses a directory; the first expansion loads it. */
  toggle(path: string): Promise<void>
  /** Expands every ancestor of `path`, root first, so the file shows in the tree. */
  reveal(path: string): Promise<void>
  expanded(): Set<string>
}

const parentsOf = (path: string): string[] => {
  const parts = path.split('/').filter(Boolean)
  const out = ['']
  for (let i = 1; i < parts.length; i++) out.push(parts.slice(0, i).join('/'))
  return out
}

/** The worktree's directories, loaded lazily. The root is the empty path. */
export function createFileTree(agentId: string, lister: Lister = listFiles): FileTree {
  const dirs = writable(new Map<string, DirState>())
  const open = new Set<string>()
  const set = (path: string, st: DirState) => dirs.update((m) => new Map(m).set(path, st))

  async function load(path: string) {
    const cur = get(dirs).get(path)
    if (Array.isArray(cur) || cur === 'loading') return
    set(path, 'loading')
    try {
      const r = await lister(agentId, path)
      set(path, r === 'unsupported' ? 'unsupported' : r.entries ?? [])
    } catch (e) {
      set(path, e instanceof Error ? e : new Error(String(e)))
    }
  }

  async function toggle(path: string) {
    if (open.has(path)) {
      open.delete(path)
      dirs.update((m) => new Map(m))
      return
    }
    open.add(path)
    await load(path)
  }

  async function reveal(path: string) {
    for (const dir of parentsOf(path)) {
      open.add(dir)
      await load(dir)
    }
    dirs.update((m) => new Map(m))
  }

  return { subscribe: dirs.subscribe, toggle, reveal, expanded: () => new Set(open) }
}
