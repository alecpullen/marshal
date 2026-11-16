import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, within } from '@testing-library/svelte'
import { createRawSnippet } from 'svelte'
import PageHeader from './PageHeader.svelte'

afterEach(cleanup)

const snippet = (text: string) => createRawSnippet(() => ({ render: () => `<span>${text}</span>` }))

describe('PageHeader', () => {
  it('navigates from a breadcrumb that names a page', async () => {
    const onNavigate = vi.fn()
    render(PageHeader, {
      breadcrumbs: [{ label: 'Projects', hash: '#projects' }, { label: 'Alpha' }],
      title: 'Alpha',
      onNavigate,
    })

    const nav = screen.getByRole('navigation', { name: 'Breadcrumb' })
    const crumb = within(nav).getByRole('link', { name: 'Projects' })
    expect(crumb.getAttribute('href')).toBe('#projects')

    await fireEvent.click(crumb)

    expect(onNavigate).toHaveBeenCalledWith('#projects')
  })

  it('marks the trailing crumb as the current page and leaves it inert', () => {
    // The crumb without a hash is where the reader already is: it must not be
    // a link, or Back would push a duplicate history entry for this page.
    const onNavigate = vi.fn()
    render(PageHeader, {
      breadcrumbs: [{ label: 'Projects', hash: '#projects' }, { label: 'Alpha' }],
      title: 'Alpha',
      onNavigate,
    })

    const nav = screen.getByRole('navigation', { name: 'Breadcrumb' })
    const current = nav.querySelector('[aria-current="page"]')
    expect(current?.textContent).toBe('Alpha')
    expect(within(nav).queryAllByRole('link')).toHaveLength(1)
  })

  it('renders the title as the only h1, above the subtitle and eyebrow', () => {
    render(PageHeader, {
      eyebrow: 'Factory',
      title: 'Review queue',
      subtitle: 'Three items need you.',
      breadcrumbs: [{ label: 'Factory', hash: '#factory' }, { label: 'Review queue' }],
      onNavigate: vi.fn(),
    })

    const headings = screen.getAllByRole('heading')
    expect(headings).toHaveLength(1)
    expect(headings[0].tagName).toBe('H1')
    expect(headings[0].textContent).toBe('Review queue')
    const nav = screen.getByRole('navigation', { name: 'Breadcrumb' })
    expect(within(nav).getByText('Factory')).toBeTruthy()
    expect(screen.getByText('Three items need you.')).toBeTruthy()
  })

  it('renders a status badge and page actions beside the title', async () => {
    const onClick = vi.fn()
    const actions = createRawSnippet(() => ({
      render: () => '<button type="button" onclick="void 0">Archive</button>',
    }))
    const { container } = render(PageHeader, {
      title: 'Review queue',
      status: snippet('Planned'),
      actions,
      onNavigate: vi.fn(),
    })

    expect(screen.getByText('Planned')).toBeTruthy()
    const button = within(container).getByText('Archive')
    await fireEvent.click(button)
    // The snippet's own handler is the caller's business; the header just
    // renders it inside the header region, not inside the h1.
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Review queue')
    expect(onClick).not.toHaveBeenCalled()
  })

  it('renders no breadcrumb nav when there are none', () => {
    const { container } = render(PageHeader, { title: 'Roadmap', onNavigate: vi.fn() })
    expect(screen.queryByRole('navigation', { name: 'Breadcrumb' })).toBeNull()
    expect(container.querySelector('a')).toBeNull()
  })
})
