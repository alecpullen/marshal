import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, within } from '@testing-library/svelte'
import Roadmap from './Roadmap.svelte'
import { ROADMAP } from '../lib/roadmap'
import { ROADMAP_AREAS } from '../lib/routes'

afterEach(cleanup)

const mount = (area: (typeof ROADMAP_AREAS)[number], onNavigate = vi.fn()) => render(Roadmap, { area, onNavigate })

describe('Roadmap page', () => {
  it('renders the planned area without operational data', () => {
    // Hosts is the sharpest case: it is the one area where nothing at all
    // works today, so a page that looked populated here would be lying.
    const { container } = mount('hosts')
    const entry = ROADMAP.hosts

    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Hosts')
    expect(within(screen.getByTestId('planned-badge')).getByText('Planned')).toBeTruthy()
    expect(screen.getByText(entry.summary)).toBeTruthy()
    expect(screen.getByTestId('roadmap-current').textContent).toBe(entry.current)
    const planned = screen.getByTestId('roadmap-planned')
    expect(within(planned).getAllByRole('listitem')).toHaveLength(entry.planned.length)
    for (const item of entry.planned) expect(within(planned).getByText(item)).toBeTruthy()

    /*
      No count, percentage, duration or date may appear. The registry has no
      such field, and the page must not manufacture one to look alive: a
      roadmap that rendered "3 planned, 0% complete, updated 2m ago" would be
      reporting numbers nobody measured.
    */
    const text = (container.textContent ?? '').replace(/\s+/g, ' ')
    expect(text).not.toMatch(/\d/)
    // "Reported capacity" is a planned capability, so the word is prose; a
    // capacity *figure*, a relative time or a check result is the leak.
    expect(text).not.toMatch(/\b(ago|just now|updated|last seen|percent|complete)\b/i)
  })

  it('opens working alternatives', async () => {
    // Every alternative has to be a destination that exists, so each button
    // is clicked and checked against the registry rather than assumed live.
    for (const area of ROADMAP_AREAS) {
      const onNavigate = vi.fn()
      const { unmount } = mount(area, onNavigate)
      const entry = ROADMAP[area]
      if (entry.alternatives.length === 0) {
        expect(screen.queryByTestId('roadmap-alternatives')).toBeNull()
        expect(screen.getByTestId('roadmap-no-alternatives')).toBeTruthy()
      } else {
        for (const action of entry.alternatives) {
          await fireEvent.click(screen.getByText(action.label))
        }
        expect(onNavigate.mock.calls).toEqual(entry.alternatives.map((a) => [a.hash]))
      }
      unmount()
    }
  })

  it('renders the external design link safely', () => {
    // The link leaves the app and is opened by someone else's server, so it
    // must not hand the new page a window handle.
    for (const area of ROADMAP_AREAS) {
      const { unmount } = mount(area)
      const link = screen.getByTestId('roadmap-design-link') as HTMLAnchorElement
      expect(link.getAttribute('href')).toBe(ROADMAP[area].designHref)
      expect(link.getAttribute('href')).toBe('https://github.com/alecpullen/marshal/blob/main/docs/studio-fleet-factory/design-notes.md')
      expect(link.getAttribute('target')).toBe('_blank')
      expect(link.getAttribute('rel')).toBe('noopener noreferrer')
      unmount()
    }
  })
})
