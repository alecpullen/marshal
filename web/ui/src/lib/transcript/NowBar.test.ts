import { describe, it, expect, afterEach } from 'vitest'
import { render, cleanup, screen } from '@testing-library/svelte'
import NowBar from './NowBar.svelte'
import type { StackState, WireBand, WireNode } from '../stack'

afterEach(cleanup)

const state = (nodes: WireNode[]): StackState => ({
  status: 'ready',
  rev: 1,
  roots: [],
  nodes: new Map(nodes.map((n) => [n.id, n])),
})

const bandNode = (band: WireBand): WireNode => ({ id: 'band', kind: 'band', band })

const band: WireBand = {
  todos: true,
  done: 2,
  total: 9,
  active: 'Task 2 — AccessibleModal',
  stale: true,
  staleAgeMs: 720000,
  next: ['Task 3'],
  entities: [{ ref: 'sub:3', label: '#3 Task 1', detail: 'Running the gate' }],
}

describe('NowBar band', () => {
  it('renders the band: progress, stale tag, checklist and running entities', () => {
    render(NowBar, { stack: state([bandNode(band)]), now: Date.now(), onStop: () => {} })
    expect(screen.getByText(/2\/9 · Task 2/)).toBeTruthy()
    expect(screen.getByText(/stale 12m/)).toBeTruthy()
    expect(screen.getByText(/next: Task 3/)).toBeTruthy()
    expect(screen.getByText(/#3 Task 1/)).toBeTruthy()
  })

  it('shows the bar for band progress even when nothing is live', () => {
    render(NowBar, { stack: state([bandNode(band)]), now: Date.now(), onStop: () => {} })
    expect(screen.getByTestId('band-progress')).toBeTruthy()
  })

  it('counts the waiting items that did not fit as +N', () => {
    const long: WireBand = { ...band, next: ['Task 3', 'Task 4', 'Task 5', 'Task 6'] }
    render(NowBar, { stack: state([bandNode(long)]), now: Date.now(), onStop: () => {} })
    expect(screen.getByText(/next: Task 3/)).toBeTruthy()
    expect(screen.getByText(/\+\d/)).toBeTruthy()
  })

  it('shows no progress rows without a band, and no bar when nothing is running', () => {
    const { container } = render(NowBar, { stack: state([]), now: Date.now(), onStop: () => {} })
    expect(container.querySelector('[data-testid="now-bar"]')).toBeNull()
  })
})
