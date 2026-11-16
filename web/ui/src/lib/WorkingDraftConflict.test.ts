import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import WorkingDraftConflict from './WorkingDraftConflict.svelte'
import { emptyWorkingDraftContent, type WorkingDraftContent } from './working-drafts'

afterEach(cleanup)

const content = (patch: Partial<WorkingDraftContent> = {}): WorkingDraftContent => ({
  ...emptyWorkingDraftContent(),
  native: true,
  ...patch,
})

const handlers = () => ({ onResolve: vi.fn(), onKeepLocal: vi.fn(), onDiscardLocal: vi.fn() })

const value = (label: string): string =>
  (screen.getByLabelText(label) as HTMLInputElement | HTMLTextAreaElement).value

it('fieldChoicesProduceEditableCombinedResult', async () => {
  const local = content({ title: 'Local title', plan: 'Local plan', spec: 'Local spec' })
  const server = content({
    title: 'Server title',
    plan: 'Server plan',
    spec: 'Server spec',
    checksMode: 'custom',
    expectedChecksText: 'check_run:build:7',
  })
  const on = handlers()
  render(WorkingDraftConflict, { local, server, base: content({ title: 'Agreed title' }), ...on })

  // Nothing is chosen yet: the combined result is the local input, whole.
  expect(value('Combined Title')).toBe('Local title')
  expect(value('Combined Plan')).toBe('Local plan')
  // Both original versions stay on screen for the user to compare.
  expect(screen.getByTestId('local-title').textContent).toBe('Local title')
  expect(screen.getByTestId('server-title').textContent).toBe('Server title')
  expect(screen.getByTestId('base-title').textContent).toContain('Agreed title')

  // One field takes the server's value; the others are untouched.
  await fireEvent.click(screen.getByRole('button', { name: 'Use server Title' }))
  expect(value('Combined Title')).toBe('Server title')
  expect(value('Combined Plan')).toBe('Local plan')

  // Every selected value stays editable, so a merge is not "pick a side".
  await fireEvent.input(screen.getByLabelText('Combined Title'), { target: { value: 'Merged title' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Use server Plan' }))
  await fireEvent.input(screen.getByLabelText('Combined Plan'), { target: { value: 'Merged plan' } })
  expect(value('Combined Title')).toBe('Merged title')
  expect(value('Combined Plan')).toBe('Merged plan')

  // The checks mode and its text are one atomic choice.
  await fireEvent.click(screen.getByRole('button', { name: 'Use server expected checks' }))
  expect((screen.getByLabelText('Combined expected checks source') as HTMLSelectElement).value).toBe('custom')
  expect(value('Combined expected CI checks')).toBe('check_run:build:7')

  await fireEvent.click(screen.getByRole('button', { name: 'Save combined version' }))
  expect(on.onResolve).toHaveBeenCalledTimes(1)
  const merged = on.onResolve.mock.calls[0][0] as WorkingDraftContent
  expect(merged.title).toBe('Merged title')
  expect(merged.plan).toBe('Merged plan')
  expect(merged.spec).toBe('Local spec')
  expect(merged.checksMode).toBe('custom')
  expect(merged.expectedChecksText).toBe('check_run:build:7')
  // The component never saves anything itself.
  expect(on.onKeepLocal).not.toHaveBeenCalled()
  expect(on.onDiscardLocal).not.toHaveBeenCalled()
})

it('secondConflictRetainsCombinedResult', async () => {
  const local = content({ title: 'Local title', plan: 'Local plan' })
  const on = handlers()
  const props = { local, server: content({ title: 'Server one', plan: 'Server plan one' }), base: null, ...on }
  const { rerender } = render(WorkingDraftConflict, props)

  await fireEvent.input(screen.getByLabelText('Combined Title'), { target: { value: 'Merged title' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Use server Plan' }))
  await fireEvent.input(screen.getByLabelText('Combined Plan'), { target: { value: 'Merged plan' } })

  // The other editor saved again while this merge was in progress: the newest
  // server version is shown, and the work in the combined result survives it.
  await rerender({ ...props, server: content({ title: 'Server two', plan: 'Server plan two' }) })
  expect(screen.getByTestId('server-title').textContent).toBe('Server two')
  expect(screen.getByTestId('server-plan').textContent).toBe('Server plan two')
  expect(value('Combined Title')).toBe('Merged title')
  expect(value('Combined Plan')).toBe('Merged plan')

  // And the choice made before the second conflict is still the recorded one.
  expect(screen.getByRole('button', { name: 'Use server Plan' }).getAttribute('aria-pressed')).toBe('true')

  await fireEvent.click(screen.getByRole('button', { name: 'Save combined version' }))
  expect(on.onResolve.mock.calls[0][0]).toMatchObject({ title: 'Merged title', plan: 'Merged plan' })
})
