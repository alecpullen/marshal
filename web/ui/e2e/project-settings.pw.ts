import { expect, test, type APIRequestContext } from '@playwright/test'
import { BASE, ROOT, auth, expectNoHorizontalOverflow, open } from './fixtures'

/*
  These cases drive the SPA the bridge actually serves. A fixture bridge
  (scripts/e2e/run_browser_checks.sh) owns a throwaway project, three agent
  profiles (alpha/beta/gamma) and no reachable provider, so every case reads and
  writes settings and drafts — nothing here starts an agent or calls a model.

  The project settings are shared state: each case seeds the exact record it
  needs through the API, then reads or writes through the UI.
*/

const projectHash = `#projects/${encodeURIComponent(ROOT)}`
const factoryHash = `#factory/new?project=${encodeURIComponent(ROOT)}`

interface Versioned { settings: Record<string, unknown>; etag: string }

/** Read the project's settings with the version token the editor would use. */
async function readSettings(request: APIRequestContext): Promise<Versioned> {
  const res = await request.get(`${BASE}/api/projects/settings?root=${encodeURIComponent(ROOT)}`, { headers: auth() })
  expect(res.ok(), `GET settings: ${res.status()}`).toBeTruthy()
  const etag = res.headers()['etag']
  expect(etag, 'the bridge must return an ETag for a settings read').toBeTruthy()
  return { settings: await res.json(), etag }
}

/** Write a full settings record, conditionally when a version is supplied. */
async function writeSettings(request: APIRequestContext, settings: Record<string, unknown>, etag?: string): Promise<Versioned> {
  const res = await request.put(`${BASE}/api/projects/settings?root=${encodeURIComponent(ROOT)}`, {
    headers: { ...auth(), ...(etag ? { 'If-Match': etag } : {}) },
    data: settings,
  })
  expect(res.ok(), `PUT settings: ${res.status()}`).toBeTruthy()
  return { settings: await res.json(), etag: res.headers()['etag'] ?? '' }
}

/** Seed an exact settings record before a case runs. */
async function seed(request: APIRequestContext, settings: Record<string, unknown>) {
  const { etag } = await readSettings(request)
  await writeSettings(request, { intake: {}, ...settings }, etag)
}

test.describe('project settings', () => {
  test.beforeEach(async ({ request }) => {
    await seed(request, {})
  })

  test('role overrides follow the agent default independently and reset', async ({ page }) => {
    await open(page, projectHash)
    const model = page.getByTestId('model-card')
    await expect(model).toBeVisible()

    // Following: both roles inherit the agent profile, which starts unset.
    await expect(page.getByTestId('implementer-effective')).toContainText('Not configured')

    // Choose the agent default; both roles follow it.
    await model.getByLabel('Model profile', { exact: true }).selectOption('alpha')
    await page.getByRole('button', { name: 'Save defaults' }).click()
    await expect(page.getByRole('status')).toContainText('Defaults saved')
    await expect(page.getByTestId('implementer-effective')).toContainText('alpha')
    await expect(page.getByTestId('reviewer-effective')).toContainText('alpha')

    // Override the implementer only; the reviewer keeps following.
    await model.getByLabel('Implementer profile', { exact: true }).selectOption('beta')
    await page.getByRole('button', { name: 'Save defaults' }).click()
    await expect(page.getByTestId('implementer-effective')).toContainText('beta')
    await expect(page.getByTestId('reviewer-effective')).toContainText('alpha')

    // Reset it: the role follows the agent default again.
    await model.getByLabel('Implementer profile', { exact: true }).selectOption('')
    await page.getByRole('button', { name: 'Save defaults' }).click()
    await expect(page.getByTestId('implementer-effective')).toContainText('alpha')

    // The reset is durable, not just on-screen.
    await page.reload()
    await expect(page.getByTestId('model-card').getByLabel('Model profile', { exact: true })).toHaveValue('alpha')
    await expect(page.getByTestId('model-card').getByLabel('Implementer profile', { exact: true })).toHaveValue('')
  })

  test('a stale save warns, keeps the edit, and offers reload', async ({ page, request }) => {
    await open(page, projectHash)
    await expect(page.getByTestId('model-card')).toBeVisible()

    // Someone else saves after this screen read its version token.
    const { etag } = await readSettings(request)
    await writeSettings(request, { intake: {}, factoryDefaults: { maxRounds: 7 } }, etag)

    // A local edit saved against the older token must not overwrite it.
    await page.getByTestId('model-card').getByLabel('Model profile', { exact: true }).selectOption('beta')
    await page.getByRole('button', { name: 'Save defaults' }).click()

    const alert = page.getByRole('alert')
    await expect(alert).toContainText('changed elsewhere')
    await expect(page.getByRole('button', { name: 'Reload' })).toBeVisible()
    // The edit survives the refused write.
    await expect(page.getByTestId('model-card').getByLabel('Model profile', { exact: true })).toHaveValue('beta')
    // And nothing was written.
    const after = await readSettings(request)
    expect(JSON.stringify(after.settings)).not.toContain('"profile":"beta"')
  })

  test('no horizontal overflow and readable labels', async ({ page }) => {
    await open(page, projectHash)
    await expect(page.getByTestId('workspace-card')).toBeVisible()
    await expectNoHorizontalOverflow(page, ['Workspace', 'Factory workspace', 'Model profile', 'Implementer profile', 'Reviewer profile'])
    // Labels are legible, not collapsed to the browser minimum.
    const size = await page.getByTestId('model-card').getByLabel('Model profile', { exact: true }).evaluate((el) => parseFloat(getComputedStyle(el).fontSize))
    expect(size).toBeGreaterThanOrEqual(12)
  })

  test('unsaved edits are confirmed before in-app navigation', async ({ page }) => {
    await open(page, projectHash)
    await page.getByTestId('model-card').getByLabel('Model profile', { exact: true }).selectOption('gamma')

    // The dialog is modal: the handler must be registered before the click,
    // because the click does not resolve until the dialog is answered.
    const seen: string[] = []
    page.once('dialog', async (dialog) => {
      seen.push(dialog.type())
      await dialog.dismiss()
    })
    await page.getByRole('link', { name: 'Projects', exact: true }).click()

    expect(seen).toEqual(['confirm'])
    // Dismissing keeps the page and the edit.
    await expect(page.getByTestId('model-card').getByLabel('Model profile', { exact: true })).toHaveValue('gamma')
  })
})

test.describe('factory handoff', () => {
  test('shows inherited sources and keeps its input when opening settings', async ({ page, request }) => {
    await seed(request, { routing: { profile: 'alpha' }, factoryDefaults: { targetBranch: 'trunk' } })
    await open(page, factoryHash)

    await page.getByLabel('Title', { exact: true }).fill('Inherited sources')
    await page.getByLabel('Plan', { exact: true }).fill('Plan body')

    // Each choice names where it came from.
    await expect(page.getByTestId('implementer-effective')).toContainText('alpha')
    await expect(page.getByTestId('reviewer-effective')).toContainText('alpha')
    await expect(page.getByTestId('target-branch-effective')).toContainText('trunk')

    // Opening project settings is a new tab, so this form stays mounted.
    const popupPromise = page.waitForEvent('popup')
    await page.getByRole('link', { name: 'Open project settings in a new tab' }).click()
    const popup = await popupPromise
    await expect(popup).toHaveURL(new RegExp('#projects/'))
    await popup.close()
    await expect(page.getByLabel('Title', { exact: true })).toHaveValue('Inherited sources')
    await expect(page.getByLabel('Plan', { exact: true })).toHaveValue('Plan body')
  })

  test('the handoff form does not overflow either viewport', async ({ page, request }) => {
    await seed(request, { routing: { profile: 'alpha' }, factoryDefaults: { targetBranch: 'trunk' } })
    await open(page, factoryHash)
    await expect(page.getByRole('button', { name: 'Save draft' })).toBeVisible()
    const width = await page.evaluate(() => document.documentElement.clientWidth)
    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth)
    expect(scrollWidth, `the factory form scrolls horizontally at ${width}px`).toBeLessThanOrEqual(width + 1)
  })
})
