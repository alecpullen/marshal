import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen, within } from '@testing-library/svelte'
import ActivityTimeline from './ActivityTimeline.svelte'
import type { FactoryEffect, FactoryEvent, FactoryItem, FactoryWorker } from '../factory'

afterEach(cleanup)

/** One recorded factory event; the sequence is what orders the list. */
const event = (sequence: number, message: string, effectId = ''): FactoryEvent => ({
  sequence,
  at: `2026-01-0${Math.min(sequence, 9)}T00:00:0${sequence % 10}Z`,
  kind: 'factory',
  message,
  effectId,
})

/** A work item as the bridge reports one, with nothing inferred. */
const item = (over: Partial<FactoryItem> = {}): FactoryItem =>
  ({ id: 'x', title: 'Task', state: 'implementing', history: [], effects: [], workers: [], ...over }) as unknown as FactoryItem

const messages = () => screen.getAllByTestId('activity-event').map((row) => row.querySelector('p')?.textContent)
const roleOf = (message: string) => {
  const row = screen.getAllByTestId('activity-event').find((candidate) => candidate.textContent?.includes(message))
  return row?.querySelector('span')?.textContent
}

describe('ActivityTimeline', () => {
  it('ordersBySequence', () => {
    render(ActivityTimeline, {
      item: item({
        history: [event(3, 'third'), event(1, 'first'), event(2, 'second'), event(2, 'second again')],
      }),
    })
    // Oldest first by the factory's own sequence; equal sequences keep the
    // order the server sent them in rather than being re-sorted.
    expect(messages()).toEqual(['first', 'second', 'second again', 'third'])
  })

  it('mapsEffectsToWorkerRolesWhenAvailable', () => {
    const effects = [
      { id: 'e1', kind: 'implement', status: 'done', workerRole: 'implementer', round: 1, approvalRevision: 1 },
      { id: 'e2', kind: 'review', status: 'running', workerRole: '', round: 1, approvalRevision: 1 },
    ] as unknown as FactoryEffect[]
    const workers = [{ agentId: 'agent-2', sessionId: 's2', role: 'reviewer', spawnEffectId: 'e2', parked: false }] as FactoryWorker[]
    render(ActivityTimeline, {
      item: item({
        effects,
        workers,
        history: [event(1, 'implementing now', 'e1'), event(2, 'reviewing now', 'e2')],
      }),
    })
    // The effect names the role it ran as; a worker's spawn effect names it too.
    expect(roleOf('implementing now')).toBe('implementer')
    expect(roleOf('reviewing now')).toBe('reviewer')
  })

  it('keepsUnmappedEventsFactoryLevel', () => {
    render(ActivityTimeline, {
      item: item({
        effects: [{ id: 'e1', kind: 'implement', status: 'done', workerRole: 'implementer' }] as unknown as FactoryEffect[],
        history: [event(1, 'approved', ''), event(2, 'unknown effect', 'e9'), event(3, 'implementing now', 'e1')],
      }),
    })
    // An event the item cannot map to an effect is factory-level, not
    // attributed to whichever worker happened to run last.
    expect(roleOf('approved')).toBe('Factory')
    expect(roleOf('unknown effect')).toBe('Factory')
    expect(roleOf('implementing now')).toBe('implementer')
  })

  it('showsNewActivityCueWhenReadingOlderEvents', async () => {
    const view = render(ActivityTimeline, { item: item({ history: [event(1, 'first'), event(2, 'second')] }) })
    const list = screen.getByTestId('activity-scroll')
    // jsdom lays nothing out, so the viewport's geometry is stubbed: the
    // reader is a full screen above the bottom of the list.
    Object.defineProperty(list, 'scrollHeight', { value: 1000, configurable: true })
    Object.defineProperty(list, 'clientHeight', { value: 100, configurable: true })
    Object.defineProperty(list, 'scrollTop', { value: 0, writable: true, configurable: true })
    await fireEvent.scroll(list)
    // Nothing new has arrived yet, so reading older events says nothing.
    expect(screen.queryByRole('button', { name: 'New activity' })).toBeNull()
    await view.rerender({ item: item({ history: [event(1, 'first'), event(2, 'second'), event(3, 'third')] }) })
    // The list stays where the reader left it, and the new event waits behind
    // a cue rather than scrolling them away from what they were reading.
    const cue = screen.getByRole('button', { name: 'New activity' })
    expect(within(list).getByText('second')).toBeTruthy()
    await fireEvent.click(cue)
    expect(screen.queryByRole('button', { name: 'New activity' })).toBeNull()
  })
})
