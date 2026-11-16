import { expect, test } from '@playwright/test'
import { BASE, ROOT, currentHash, open, projectName, reopen } from './fixtures'

/*
  Real-bridge coverage for the Fleet-first shell: the routes that live now,
  driven against the SPA the fixture `webbridge` actually serves.

  Nothing here creates, approves, retries or recovers factory work. The
  fixture bridge has no GitHub credential, and `factoryAPI.preflight` requires
  one, so a case that tried to would be proving the credential's absence
  rather than the UI. Presentation states that need real factory records live
  in `studio-fleet.pw.ts`, which says so in its own header.
*/

const projectHash = `#projects/${encodeURIComponent(ROOT)}`
const scopedFactory = `#projects/${encodeURIComponent(ROOT)}/factory`

const ROADMAP_AREAS = ['planning', 'voice', 'workflow', 'hosts', 'environments', 'connections'] as const

test.describe('studio shell', () => {
  test('emptyFleetHasWorkingStartActions', async ({ page }) => {
    await open(page, '#')
    await expect(page.getByRole('heading', { name: 'Fleet', level: 1 })).toBeVisible()

    // The fixture bridge lists its throwaway project, so the fleet is not
    // empty of projects — it is empty of work, which the counts say.
    const card = page.getByTestId('project-factory-card')
    await expect(card).toBeVisible()
    await expect(card.getByTestId('count-current')).toHaveText('0')
    await expect(card.getByTestId('count-needs-you')).toHaveText('0')
    await expect(card.getByTestId('count-ready')).toHaveText('0')
    // Nothing is waiting on a person, so the attention sections are absent.
    await expect(page.getByRole('heading', { name: /^Needs you/ })).toHaveCount(0)
    await expect(page.getByRole('heading', { name: /^Ready for review/ })).toHaveCount(0)

    // The three start actions a project card offers all reach their route.
    await card.getByRole('button', { name: 'Open factory' }).click()
    await expect(page.getByRole('heading', { name: 'Factory', level: 1 })).toBeVisible()
    expect(await currentHash(page)).toBe(scopedFactory)

    await reopen(page, '#')
    await page.getByTestId('project-factory-card').getByRole('button', { name: 'New work item' }).click()
    await expect(page.getByLabel('Title', { exact: true })).toBeVisible()

    await reopen(page, '#')
    await page.getByTestId('project-factory-card').getByRole('button', { name: 'Start agent' }).click()
    await expect(page.getByRole('heading', { name: 'New agent', level: 1 })).toBeVisible()
    await expect(page.getByRole('button', { name: /^Project:/ })).toContainText(projectName())

    // The resource directory is a plain set of links, not a second route model.
    await reopen(page, '#')
    const resources = page.getByRole('navigation', { name: 'Fleet resources' })
    await expect(resources.getByRole('link', { name: 'Projects', exact: true })).toHaveAttribute('href', '#projects')
    await expect(resources.getByRole('link', { name: 'Usage', exact: true })).toHaveAttribute('href', '#usage')
  })

  test('allRoadmapPagesArePlannedAndReadOnly', async ({ page }) => {
    const writes: string[] = []
    page.on('request', (r) => {
      if (r.method() !== 'GET') writes.push(`${r.method()} ${r.url()}`)
    })

    await open(page, '#roadmap/hosts')
    for (const area of ROADMAP_AREAS) {
      await page.goto(`${BASE}/#roadmap/${area}`)
      const roadmap = page.getByTestId('roadmap')
      await expect(roadmap).toBeVisible()
      await expect(roadmap).toHaveAttribute('data-area', area)
      // Every area is labelled Planned, because none of them exists yet.
      await expect(page.getByTestId('planned-badge')).toHaveText('Planned')
      await expect(page.getByTestId('roadmap-current')).not.toBeEmpty()
      await expect(page.getByTestId('roadmap-planned').getByRole('listitem')).not.toHaveCount(0)
      // Read-only: the page has no input of any kind to change.
      await expect(roadmap.locator('input, textarea, select, form')).toHaveCount(0)
      // It offers a working destination when one exists, and says so when not.
      const alternatives = await page.getByTestId('roadmap-alternatives').count()
      const none = await page.getByTestId('roadmap-no-alternatives').count()
      expect(alternatives + none, `${area} offers neither an alternative nor an explanation`).toBe(1)
    }

    // A bare `#roadmap` names no area, so it is not a route.
    await page.goto(`${BASE}/#roadmap`)
    await expect(page.getByText('Not Found')).toBeVisible()

    // A read-only page that mutates nothing also writes nothing.
    expect(writes, `the roadmap issued a write: ${writes.join(', ')}`).toEqual([])
  })

  test('projectPrefillAndUnknownProject', async ({ page }) => {
    await open(page, `#new?project=${encodeURIComponent(ROOT)}`)
    // A link that names a listed project selects exactly it.
    await expect(page.getByRole('button', { name: /^Project:/ })).toContainText(projectName())
    await expect(page.getByRole('button', { name: /^Create agent/ })).toBeEnabled()
    await expect(page.getByTestId('route-project-notice')).toHaveCount(0)

    // A link that names a project this bridge does not list selects nothing
    // rather than quietly falling back to the first listed one.
    const unknown = `${ROOT}-not-listed`
    await reopen(page, `#new?project=${encodeURIComponent(unknown)}`)
    await expect(page.getByTestId('route-project-notice')).toBeVisible()
    await expect(page.getByTestId('route-project-notice')).toContainText('does not list')
    await expect(page.getByRole('button', { name: /^Create agent/ })).toBeDisabled()
  })

  test('shellDeepLinksAndBrowserBack', async ({ page }) => {
    // A deep link renders the page it names, not the fleet, and survives a reload.
    await reopen(page, '#inbox')
    await expect(page.getByRole('heading', { name: 'Inbox', level: 1 })).toBeVisible()

    // The shell's own navigation is a hash assignment: no document reload.
    await page.getByRole('button', { name: 'Fleet', exact: true }).click()
    await expect(page.getByRole('heading', { name: 'Fleet', level: 1 })).toBeVisible()
    expect(await currentHash(page)).toBe('#')

    // Browser Back returns to the deep-linked page it came from.
    await page.goBack()
    await expect(page.getByRole('heading', { name: 'Inbox', level: 1 })).toBeVisible()

    // The retired standalone fleet page still resolves, to the fleet home.
    await reopen(page, '#fleet')
    await expect(page.getByRole('heading', { name: 'Fleet', level: 1 })).toBeVisible()
    expect(await currentHash(page)).toBe('#')

    // A project's factory is a deep link of its own.
    await reopen(page, scopedFactory)
    await expect(page.getByRole('heading', { name: 'Factory', level: 1 })).toBeVisible()
    expect(await currentHash(page)).toBe(scopedFactory)
  })

  test('routeGuardCoversShellNavigationAndBrowserBack', async ({ page }) => {
    await open(page, '#')
    await page.evaluate((hash) => (window.location.hash = hash), projectHash)
    const branch = page.getByLabel('Target branch', { exact: true })
    await expect(branch).toBeVisible()

    // An unsaved edit registers the shell's one route guard. The value is not
    // `trunk`, which an earlier case leaves seeded in this shared fixture
    // project: editing to the value already saved would not be an edit.
    await branch.fill('guard-check')
    await expect(page.getByTestId('unsaved')).toBeVisible()

    /*
      The dialog is opened inside the shell's own hash handler, so it arrives a
      tick after the click or `goBack()` resolves. Waiting for the event is the
      only honest way to observe it; sampling an array straight afterwards
      races the browser.
    */
    const declineNext = () =>
      new Promise<string>((resolve) => {
        page.once('dialog', async (dialog) => {
          const kind = dialog.type()
          await dialog.dismiss()
          resolve(kind)
        })
      })

    // Shell navigation: the guard asks, and a decline keeps the screen and the edit.
    const shellNav = declineNext()
    await page.getByRole('button', { name: 'Fleet', exact: true }).click()
    expect(await shellNav).toBe('confirm')
    await expect(branch).toHaveValue('guard-check')
    expect(await currentHash(page)).toBe(projectHash)

    // Browser Back: the guard asks the same question, and a decline rewrites the
    // hash with replaceState so the mounted form keeps every bit of input.
    const browserBack = declineNext()
    await page.goBack({ waitUntil: 'commit' }).catch(() => {
      // A declined hash change is undone synchronously, so the browser can
      // report the navigation as aborted. The assertions below are the point.
    })
    expect(await browserBack).toBe('confirm')
    await expect(branch).toHaveValue('guard-check')
    expect(await currentHash(page)).toBe(projectHash)
  })

  test('unknownRouteHasFleetReturn', async ({ page }) => {
    await reopen(page, '#not-a-page/at-all')
    await expect(page.getByText('Not Found')).toBeVisible()
    await expect(page.getByText(/There is no page at #not-a-page\/at-all/)).toBeVisible()

    // The one way forward is the fleet home, and it works.
    await page.getByRole('button', { name: 'Go to Fleet' }).click()
    await expect(page.getByRole('heading', { name: 'Fleet', level: 1 })).toBeVisible()
    expect(await currentHash(page)).toBe('#')

    // An unknown roadmap area is not a route either.
    await reopen(page, '#roadmap/teleportation')
    await expect(page.getByText('Not Found')).toBeVisible()
  })
})
