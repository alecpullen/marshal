import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import DraftApproval from './DraftApproval.svelte'
import type { FactoryConfig, FactoryItem } from '../factory'

afterEach(cleanup)

/** A saved draft as the bridge returns one: every configuration field resolved. */
const config = (over: Partial<FactoryConfig> = {}): FactoryConfig => ({
  repoId: 'acme/widgets',
  projectRoot: '/home/u/alpha',
  hostId: 'local',
  targetBranch: 'release',
  workspace: 'ci-dev',
  native: false,
  implementerRouting: { profile: 'careful', model: 'opus' },
  reviewerRouting: { profile: 'strict', model: 'sonnet' },
  reviewerInstructions: 'Check the migration',
  implementerAuto: true,
  expectedChecks: [{ kind: 'check_run', name: 'build', appId: 7 }],
  noRequiredCiAcknowledged: false,
  maxRounds: 9,
  ciPollSeconds: 30,
  ciWaitSeconds: 60,
  ...over,
})

const draft = (over: { plan?: string; spec?: string; config?: FactoryConfig } = {}): FactoryItem =>
  ({
    id: 'x',
    title: 'Task',
    state: 'draft',
    version: 1,
    draft: { requestId: 'r-1', title: 'Task', plan: 'Build it', spec: '', config: config(), ...over },
  }) as unknown as FactoryItem

describe('DraftApproval', () => {
  it('showsResolvedConfigAndConsequence', () => {
    render(DraftApproval, { item: draft(), busy: false, onApprove: vi.fn() })
    // The approval reads the same resolved rows creation previews.
    expect(screen.getByTestId('config-repository').textContent).toBe('acme/widgets')
    expect(screen.getByTestId('config-target-branch').textContent).toBe('release')
    expect(screen.getByTestId('config-environment').textContent).toBe('ci-dev')
    expect(screen.getByTestId('config-implementer').textContent).toBe('careful')
    expect(screen.getByTestId('config-reviewer').textContent).toBe('strict')
    expect(screen.getByTestId('config-expected-checks').textContent).toBe('check_run:build:7')
    expect(screen.getByTestId('config-total-rounds').textContent).toBe('9')
    // The consequence names what approving authorizes, and claims no progress.
    const consequence = screen.getByTestId('config-consequence').textContent ?? ''
    expect(consequence).toContain('authorizes execution under the resolved configuration')
    expect(consequence).toContain('starts nothing')
    // A routing object is only ever read for its profile, never printed.
    const approval = screen.getByTestId('effective-config')
    expect(approval.textContent).not.toContain('"profile"')
    expect(approval.textContent).not.toContain('{')
  })

  it('showsPlanAndOptionalSpecification', () => {
    render(DraftApproval, { item: draft({ plan: 'Build the widget', spec: 'Keep the migration safe' }), busy: false, onApprove: vi.fn() })
    expect(screen.getByTestId('summary-plan').textContent).toBe('Build the widget')
    expect(screen.getByTestId('summary-spec').textContent).toBe('Keep the migration safe')
    cleanup()
    // A draft without a specification shows no specification disclosure.
    render(DraftApproval, { item: draft({ plan: 'Build the widget' }), busy: false, onApprove: vi.fn() })
    expect(screen.getByTestId('summary-plan').textContent).toBe('Build the widget')
    expect(screen.queryByTestId('summary-spec')).toBeNull()
  })

  it('approvesOnlyOnExplicitAction', async () => {
    const onApprove = vi.fn()
    render(DraftApproval, { item: draft(), busy: false, onApprove })
    // Rendering the approval decides nothing on its own.
    expect(onApprove).not.toHaveBeenCalled()
    await fireEvent.click(screen.getByRole('button', { name: 'Approve & start' }))
    expect(onApprove).toHaveBeenCalledTimes(1)
  })

  it('disablesWhileBusy', () => {
    render(DraftApproval, { item: draft(), busy: true, onApprove: vi.fn() })
    // A command in flight owns the approval: the control is inert until it
    // settles, so the same decision cannot be sent twice.
    expect((screen.getByRole('button', { name: 'Approve & start' }) as HTMLButtonElement).disabled).toBe(true)
    cleanup()
    render(DraftApproval, { item: draft(), busy: false, onApprove: vi.fn() })
    expect((screen.getByRole('button', { name: 'Approve & start' }) as HTMLButtonElement).disabled).toBe(false)
  })
})
