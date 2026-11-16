import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, cleanup, screen, fireEvent, waitFor, within } from '@testing-library/svelte'
import SubagentControls from './SubagentControls.svelte'
import { APIError } from '../api'
import {
  createSubagentControls,
  NO_SUBAGENT_CONTROLS,
  type SubagentControls as Transport,
  type WireSubagentControls,
} from '../stack'

/*
  One live child's controls. The transport is `createSubagentControls`, so these
  tests give the strip a fake one and keep the wording under test: queue
  acceptance is not consumption, a refusal is a failure, a missing answer is
  unknown, and a stop is acknowledged before it is observed.
*/
afterEach(cleanup)

const capabilities = (over: Partial<WireSubagentControls> = {}): WireSubagentControls => ({
  settings: true,
  steer: true,
  stop: true,
  observe: true,
  ...over,
})

const transport = (over: Partial<Transport> = {}): Transport => ({
  steer: vi.fn(async () => ({ requestId: 'req-1', status: 'accepted' })),
  stop: vi.fn(async () => ({ cancelled: true })),
  ...over,
})

const props = (over: Record<string, unknown> = {}) => ({
  sessionId: 's1',
  childId: 3,
  label: 'reviewer',
  capabilities: capabilities(),
  controls: transport(),
  ...over,
})

const instruction = () => screen.getByLabelText('Instruction for reviewer') as HTMLTextAreaElement

async function instruct(text = 'focus on the failing check') {
  await fireEvent.input(instruction(), { target: { value: text } })
  await fireEvent.click(screen.getByRole('button', { name: 'Send instruction' }))
}

describe('SubagentControls', () => {
  it('says unsupported when the session claims no child controls', async () => {
    render(SubagentControls, props({ capabilities: NO_SUBAGENT_CONTROLS }))
    expect(await screen.findByTestId('subagent-unsupported')).toBeTruthy()
    // Nothing is offered: no field, no stop, and nothing guessed from another
    // session's report.
    expect(screen.queryByRole('textbox')).toBeNull()
    expect(screen.queryByRole('button', { name: /Stop/ })).toBeNull()
    expect(screen.queryByTestId('subagent-unavailable')).toBeNull()
  })

  it('says unavailable when the child cannot be reached from here', async () => {
    render(SubagentControls, props({ reachable: false }))
    // Unavailable is its own state, not a missing capability.
    expect(await screen.findByTestId('subagent-unavailable')).toBeTruthy()
    expect(screen.queryByTestId('subagent-unsupported')).toBeNull()
    expect(screen.queryByRole('textbox')).toBeNull()
  })

  it('shows one control unavailable when the session registers only the other', async () => {
    render(SubagentControls, props({ capabilities: capabilities({ stop: false }) }))
    expect(await screen.findByTestId('stop-unavailable')).toBeTruthy()
    expect(screen.getByRole('textbox')).toBeTruthy()
  })

  it('reports queue acceptance without claiming the child read it', async () => {
    const controls = transport()
    render(SubagentControls, props({ controls }))
    await instruct()

    const receipt = await screen.findByTestId('steer-receipt')
    expect(receipt.dataset.state).toBe('accepted')
    expect(controls.steer).toHaveBeenCalledWith('focus on the failing check')
    expect(receipt.textContent).toContain('Accepted')
    // Acceptance is the queue taking it; the child reads it at its next step.
    expect(receipt.textContent).toContain('not read yet')
    expect(receipt.textContent).toContain('next step')
    expect(screen.getByTestId('steer-request').textContent).toContain('req-1')
    expect(receipt.textContent).not.toMatch(/delivered|has read/i)
    // The field is cleared only by an acceptance.
    expect((screen.getByLabelText('Instruction for reviewer') as HTMLTextAreaElement).value).toBe('')
  })

  it('keeps the instruction when the bridge refused it', async () => {
    const controls = transport({
      steer: vi.fn(async () => {
        throw new APIError(409, { error: 'request id reused' })
      }),
    })
    render(SubagentControls, props({ controls }))
    await instruct()

    const receipt = await screen.findByTestId('steer-receipt')
    // A refusal is the session's own answer, so it is a failure, not unknown.
    expect(receipt.dataset.state).toBe('failed')
    expect(receipt.textContent).toContain('Refused')
    expect(receipt.textContent).toContain('409')
    expect((instruction() as HTMLTextAreaElement).value).toBe('focus on the failing check')
  })

  it('never renders a missing answer as a failure or a success', async () => {
    const controls = transport({
      steer: vi.fn(async () => {
        throw new TypeError('network down')
      }),
    })
    render(SubagentControls, props({ controls }))
    await instruct()

    const receipt = await screen.findByTestId('steer-receipt')
    expect(receipt.dataset.state).toBe('unknown')
    expect(receipt.textContent).toContain('Outcome unknown')
    expect(receipt.textContent).not.toContain('Refused')
    expect(receipt.textContent).not.toContain('Accepted')
  })

  it('treats a status it does not know as unknown', async () => {
    const controls = transport({ steer: vi.fn(async () => ({ requestId: 'req-2', status: 'queued-somewhere' })) })
    render(SubagentControls, props({ controls }))
    await instruct()

    const receipt = await screen.findByTestId('steer-receipt')
    expect(receipt.dataset.state).toBe('unknown')
    expect(receipt.textContent).toContain('queued-somewhere')
  })

  it('sends on the send chord and keeps a plain Enter as a newline', async () => {
    const controls = transport()
    render(SubagentControls, props({ controls }))
    await fireEvent.input(instruction(), { target: { value: 'one instruction' } })

    // Enter is a line break in a multi-line field: nothing is sent.
    await fireEvent.keyDown(instruction(), { key: 'Enter' })
    expect(controls.steer).not.toHaveBeenCalled()

    await fireEvent.keyDown(instruction(), { key: 'Enter', ctrlKey: true })
    await waitFor(() => expect(controls.steer).toHaveBeenCalledWith('one instruction'))

    // Every control the strip offers is a real, tabbable button.
    const stop = screen.getByRole('button', { name: 'Stop reviewer' })
    expect(stop.tagName).toBe('BUTTON')
    await fireEvent.click(stop)
    await waitFor(() => expect(controls.stop).toHaveBeenCalledTimes(1))
  })

  it('keeps a stop pending until the child is observed terminal', async () => {
    const controls = transport()
    const view = render(SubagentControls, props({ controls, status: 'running' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Stop reviewer' }))

    const receipt = await screen.findByTestId('stop-receipt')
    // The acknowledgement is a receipt about the request, not the stop.
    expect(receipt.dataset.state).toBe('pending')
    expect(receipt.textContent).toContain('pending')
    expect(receipt.textContent).toContain('leaves running')

    // The child's own status leaving running is what ends it.
    await view.rerender(props({ controls, status: 'done' }))
    await waitFor(() => expect(screen.getByTestId('stop-receipt').dataset.state).toBe('stopped'))
    expect(screen.getByTestId('stop-receipt').textContent).toContain('done')
    // A finished child cannot be stopped again.
    expect((screen.getByRole('button', { name: 'Stop reviewer' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('shows an unacknowledged stop as unknown', async () => {
    const controls = transport({ stop: vi.fn(async () => ({ cancelled: false })) })
    render(SubagentControls, props({ controls, status: 'running' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Stop reviewer' }))

    const receipt = await screen.findByTestId('stop-receipt')
    expect(receipt.dataset.state).toBe('unknown')
    expect(receipt.textContent).toContain('may still be running')
  })

  it('never shares controls between the same child number in two sessions', async () => {
    const posted: string[] = []
    const post = vi.fn(async (path: string) => {
      posted.push(path)
      return { requestId: 'req-1', status: 'accepted' }
    })
    const first = createSubagentControls('s1', 1, { post })
    const second = createSubagentControls('s2', 1, { post })

    render(SubagentControls, props({ sessionId: 's1', childId: 1, label: 'first', controls: first }))
    render(SubagentControls, props({ sessionId: 's2', childId: 1, label: 'second', controls: second }))

    const strips = screen.getAllByTestId('subagent-controls')
    expect(strips.map((s) => [s.dataset.session, s.dataset.child])).toEqual([
      ['s1', '1'],
      ['s2', '1'],
    ])

    const firstStrip = within(strips[0])
    const field = firstStrip.getByLabelText('Instruction for first') as HTMLTextAreaElement
    await fireEvent.input(field, { target: { value: 'only the first' } })
    await fireEvent.click(firstStrip.getByRole('button', { name: 'Send instruction' }))

    // Child 1 of s1 is a different child from child 1 of s2: the request names
    // the session that owns it, and the other child's control is untouched.
    await waitFor(() => expect(posted).toEqual(['/api/sessions/s1/subagents/1/steer']))
    expect(screen.getByLabelText('Instruction for second')).toBeTruthy()
    expect((screen.getByLabelText('Instruction for second') as HTMLTextAreaElement).value).toBe('')
  })

  it('stacks on a narrow screen with short labels', async () => {
    const controls = transport()
    render(SubagentControls, props({ controls, compact: true }))

    const strip = screen.getByTestId('subagent-controls')
    expect(strip.dataset.layout).toBe('compact')
    // The same controls, with the labels a phone-width column can carry.
    expect(await screen.findByRole('button', { name: 'Send' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Send instruction' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Stop reviewer' })).toBeTruthy()
    expect((screen.getByLabelText('Instruction for reviewer') as HTMLTextAreaElement).rows).toBe(2)

    await fireEvent.input(instruction(), { target: { value: 'narrow' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(controls.steer).toHaveBeenCalledWith('narrow'))
  })

  it('is a labelled region so a screen reader can tell the strips apart', async () => {
    render(SubagentControls, props({ label: 'reviewer' }))
    expect(screen.getByRole('region', { name: 'Controls for reviewer' })).toBeTruthy()
  })
})
