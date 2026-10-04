import { writable, get, type Readable } from 'svelte/store'
import { listFiles, type FileList } from '../api'

export type DirEntry = FileList['entries'][number]
/** A directory's state: its entries, still loading, or why it failed. */
export type DirState = DirEntry[] | 'loading' | Error | 'unsupported'

type Lister = (agentId: string, path: string) => Promise<FileList | 'unsupported'>

export interface TreeState {
  dirs: Map<string, DirState>
  /** The directories shown open. A new Set on every change, so views re-render. */
  open: Set<string>
}

export interface FileTree extends Readable<TreeState> {
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
  const tree = writable<TreeState>({ dirs: new Map(), open: new Set() })
  const set = (path: string, st: DirState) => tree.update((t) => ({ ...t, dirs: new Map(t.dirs).set(path, st) }))
  const setOpen = (path: string, on: boolean) =>
    tree.update((t) => {
      const open = new Set(t.open)
      if (on) open.add(path)
      else open.delete(path)
      return { ...t, open }
    })

  async function load(path: string) {
    const cur = get(tree).dirs.get(path)
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
    if (get(tree).open.has(path)) {
      setOpen(path, false)
      return
    }
    setOpen(path, true)
    await load(path)
  }

  async function reveal(path: string) {
    for (const dir of parentsOf(path)) {
      setOpen(dir, true)
      await load(dir)
    }
  }

  return { subscribe: tree.subscribe, toggle, reveal, expanded: () => new Set(get(tree).open) }
}
