import { describe, expect, it, vi } from 'vitest'
import { get } from 'svelte/store'
import { createFileTree } from './fileTree'

const entries = (...names: string[]) => ({ entries: names.map((name) => ({ name, dir: !name.includes('.'), size: 1 })) })

describe('file tree', () => {
  it('toggle loads a directory once and collapses without refetching', async () => {
    const lister = vi.fn().mockResolvedValue(entries('a.go', 'src'))
    const t = createFileTree('a1', lister)
    await t.toggle('')
    await t.toggle('')
    await t.toggle('')
    expect(lister).toHaveBeenCalledTimes(1)
    expect(t.expanded().has('')).toBe(true)
    expect(get(t).get('')).toEqual(entries('a.go', 'src').entries)
  })

  it('reveal expands every ancestor, root first', async () => {
    const order: string[] = []
    const lister = vi.fn(async (_a: string, p: string) => {
      order.push(p)
      return entries('x')
    })
    const t = createFileTree('a1', lister)
    await t.reveal('src/lib/deep/file.ts')
    expect(order).toEqual(['', 'src', 'src/lib', 'src/lib/deep'])
    expect([...t.expanded()].sort()).toEqual(['', 'src', 'src/lib', 'src/lib/deep'])
  })

  it('records a failure and an unsupported agent', async () => {
    const t = createFileTree('a1', vi.fn().mockRejectedValue(new Error('boom')))
    await t.toggle('')
    expect(get(t).get('')).toBeInstanceOf(Error)
    const u = createFileTree('a1', vi.fn().mockResolvedValue('unsupported'))
    await u.toggle('')
    expect(get(u).get('')).toBe('unsupported')
  })
})
