import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import AttentionCard from './AttentionCard.svelte'
import ProjectFactoryCard from './ProjectFactoryCard.svelte'
import { summarizeFactoryProject } from './model'
import type { FactoryConfig, FactoryItem } from '../factory'
import type { ProjectStatus } from '../api'

afterEach(cleanup)

/**
 * The cards read a factory item's root, state, timestamp, decision, workers
 * and history, so the fixture is a whole item — the wire always sends one.
 */
const config: FactoryConfig = {
  repoId: 'acme/widgets',
  projectRoot: '/w/alpha',
  hostId: 'local',
  targetBranch: 'main',
  workspace: 'default',
  native: false,
  implementerRouting: {},
  reviewerRouting: {},
  reviewerInstructions: '',
  implementerAuto: false,
  expectedChecks: [],
  noRequiredCiAcknowledged: false,
  maxRounds: 3,
  ciPollSeconds: 30,
  ciWaitSeconds: 600,
}

function item(x: Partial<FactoryItem> & { id: string }): FactoryItem {
  const title = `Work on ${x.id}`
  return {
    title,
    state: 'implementing',
    stage: 'implement',
    repoId: 'acme/widgets',
    projectRoot: '/w/alpha',
    hostId: 'local',
    branch: `factory/${x.id}`,
    baseSha: 'base0',
    version: 1,
    draft: { requestId: `req-${x.id}`, title, plan: '', spec: '', config },
    approvals: null,
    grants: null,
    rounds: null,
    workers: null,
    prs: null,
    effects: null,
    decision: null,
    history: null,
    createdAt: '2026-11-01T00:00:00Z',
    approvedAt: '',
    updatedAt: '2026-11-01T00:00:00Z',
    ...x,
  }
}

const project = (root: string, over: Partial<ProjectStatus> = {}): ProjectStatus => ({
  root,
  available: true,
  trust: 'trusted',
  isolation: 'available',
  ...over,
})

describe('AttentionCard', () => {
  it('longTitlesWrapWithoutOverflow', () => {
    // A title long enough to overflow a single line on a narrow card.
    const title = `${'A long factory item title that keeps going '.repeat(6)}and ends here`
    render(AttentionCard, {
      source: 'Factory',
      title,
      href: '#factory/item-1',
      actionLabel: 'Open',
      onNavigate: () => {},
    })

    const rendered = screen.getByText(title)
    // The whole title is rendered, and it wraps instead of being clipped.
    expect(rendered.textContent).toBe(title)
    expect(rendered.className).toContain('break-words')
    expect(rendered.className).not.toContain('truncate')
  })

  it('cardActionsNavigateDirectly', async () => {
    const onNavigate = vi.fn()
    render(AttentionCard, {
      source: 'Agent',
      title: 'ask-agent',
      body: 'rm -rf build',
      href: '#chat/ask',
      actionLabel: 'Open agent',
      onNavigate,
    })

    await userEvent.click(screen.getByRole('button', { name: 'Open agent' }))
    expect(onNavigate).toHaveBeenCalledWith('#chat/ask')
  })
})

describe('ProjectFactoryCard', () => {
  it('missingProjectRegistrationIsExplicit', () => {
    render(ProjectFactoryCard, {
      summary: summarizeFactoryProject('/w/beta', [item({ id: 'b1', projectRoot: '/w/beta' })]),
      onNavigate: () => {},
    })

    // A factory-only root keeps its card and says so; it is not called unavailable.
    expect(screen.getByText('Project not registered')).toBeTruthy()
    expect(screen.queryByText('/w/beta is unavailable')).toBeNull()
    // Nothing can be started in a project the bridge does not know about.
    expect(screen.queryByRole('button', { name: 'Start agent' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'New work item' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Open factory' })).toBeTruthy()
  })

  it('registeredProjectOffersItsActions', () => {
    render(ProjectFactoryCard, {
      project: project('/w/alpha'),
      summary: summarizeFactoryProject('/w/alpha', []),
      onNavigate: () => {},
    })

    expect(screen.queryByText('Project not registered')).toBeNull()
    expect(screen.getByRole('button', { name: 'New work item' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Start agent' })).toBeTruthy()
  })

  it('zeroCountsAreRealEmptyCounts', () => {
    render(ProjectFactoryCard, {
      project: project('/w/alpha'),
      summary: summarizeFactoryProject('/w/alpha', []),
      onNavigate: () => {},
    })

    // A project with nothing in flight reads as zeros, not as a blank summary.
    expect(screen.getByTestId('count-current').textContent).toBe('0')
    expect(screen.getByTestId('count-needs-you').textContent).toBe('0')
    expect(screen.getByTestId('count-ready').textContent).toBe('0')
    expect(screen.getByTestId('count-linked-agents').textContent).toBe('0')
  })
})
