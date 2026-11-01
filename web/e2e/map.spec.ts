import { expect, test, type Page } from '@playwright/test'

import { mockApi, mockMapStyles, stubEventSource } from './mocks'

const dir = process.env.SHOTS_DIR

async function open(page: Page, role: 'admin' | 'viewer' = 'viewer') {
  await mockApi(page, { role })
  await mockMapStyles(page)
  await stubEventSource(page)
  await page.goto('/map')
}

/**
 * Waits until the map either started ("Fit all" is there) or reported that it could not (no WebGL
 * in this browser), so callers branch on a settled state instead of racing the first paint.
 */
async function mapState(page: Page): Promise<'ready' | 'unavailable'> {
  const fitAll = page.getByRole('button', { name: 'Fit all' })
  const unavailable = page.getByRole('alert').filter({ hasText: /map could not start/ })
  await expect(fitAll.or(unavailable)).toBeVisible({ timeout: 15_000 })
  return (await unavailable.isVisible()) ? 'unavailable' : 'ready'
}
const NO_WEBGL = 'WebGL is unavailable in this browser, so the map cannot start'

const noHScroll = (page: Page) =>
  page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)

for (const vp of [
  { name: 'mobile', width: 375, height: 812 },
  { name: 'desktop', width: 1280, height: 800 },
]) {
  test.describe(vp.name, () => {
    test.use({ viewport: { width: vp.width, height: vp.height } })

    test('lists phones, selects one, fits all, no horizontal scroll', async ({ page }) => {
      await open(page)
      await expect(page.getByRole('heading', { name: 'Map', level: 1 })).toBeAttached()
      if (vp.name === 'mobile') {
        const handle = page.getByRole('button', { name: /3 phones/ })
        await expect(handle).toBeVisible()
        await expect(handle).toHaveAttribute('aria-expanded', 'false')
        await handle.click()
        await expect(handle).toHaveAttribute('aria-expanded', 'true')
      }
      const list = page.getByTestId('map-device-list')
      await expect(list.getByRole('listitem')).toHaveCount(3)
      await expect(list.getByText('Dizengoff St, Tel Aviv')).toBeVisible()
      await expect(list.getByText('No location')).toBeVisible()
      expect(await noHScroll(page)).toBe(true)

      await page.getByRole('searchbox', { name: 'Search phones' }).fill('נועה')
      await expect(list.getByRole('listitem')).toHaveCount(1)
      await page.getByRole('searchbox', { name: 'Search phones' }).fill('')

      expect(await noHScroll(page)).toBe(true)

      // The map itself needs WebGL: skip (visibly) rather than silently asserting nothing.
      test.skip((await mapState(page)) === 'unavailable', NO_WEBGL)
      await list.getByRole('button', { name: 'Show Tomer Pixel on the map' }).click()
      const tip = page.getByTestId('map-tooltip')
      await expect(tip).toBeVisible()
      await expect(tip.getByText('Dizengoff St, Tel Aviv')).toBeVisible()
      await expect(tip.getByRole('button', { name: /Ping/ })).toHaveCount(0) // viewer
      await page.keyboard.press('Escape')
      await expect(tip).toHaveCount(0)
      await page.getByRole('button', { name: 'Fit all' }).click()
      await expect(page.locator('.maplibregl-ctrl-attrib')).toContainText('Test map data')
      expect(await noHScroll(page)).toBe(true)
    })

    test('dark mode toggle switches the map style', async ({ page }) => {
      await open(page)
      const requested: string[] = []
      page.on('request', (r) => {
        if (r.url().startsWith('https://map.example.test/')) requested.push(r.url())
      })
      await expect(page.getByRole('heading', { name: 'Map', level: 1 })).toBeAttached()
      const toggle = page.getByTestId('theme-toggle')
      await toggle.click() // light
      await toggle.click() // dark
      await expect(page.locator('html')).toHaveClass(/dark/)
      expect(await noHScroll(page)).toBe(true)
      test.skip((await mapState(page)) === 'unavailable', NO_WEBGL)
      await expect.poll(() => requested.some((u) => u.endsWith('/dark'))).toBe(true)
    })

    for (const scheme of ['light', 'dark'] as const) {
      test(`screenshot ${scheme}`, async ({ page }) => {
        test.skip(!dir, 'set SHOTS_DIR to capture screenshots')
        await page.emulateMedia({ colorScheme: scheme })
        await open(page, 'admin')
        if (scheme === 'dark') {
          const toggle = page.getByTestId('theme-toggle')
          await toggle.click()
          await toggle.click()
        }
        await expect(page.getByRole('heading', { name: 'Map', level: 1 })).toBeAttached()
        const failed = (await mapState(page)) === 'unavailable'
        if (!failed) {
          if (vp.name === 'mobile') {
            await page.getByRole('button', { name: /3 phones/ }).click()
            await page.getByRole('button', { name: 'Show Tomer Pixel on the map' }).click()
          } else {
            await page.getByRole('button', { name: 'Show Tomer Pixel on the map' }).click()
          }
          await expect(page.getByTestId('map-tooltip')).toBeVisible()
          await page.waitForTimeout(1500)
        } else if (vp.name === 'mobile') {
          await page.getByRole('button', { name: /3 phones/ }).click()
        }
        await page.screenshot({ path: `${dir}/map-${vp.width}-${scheme}.png` })
      })
    }
  })
}

test('admin sees Ping in the marker tooltip, viewer does not', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await open(page, 'admin')
  test.skip((await mapState(page)) === 'unavailable', NO_WEBGL)
  const marker = page.getByRole('button', { name: /Tomer Pixel, online/ })
  await marker.focus()
  await expect(
    page.getByTestId('map-tooltip').getByRole('button', { name: 'Ping Tomer Pixel' }),
  ).toBeVisible()
})

test('cards show the location name with the coordinates as the secondary line', async ({
  page,
}) => {
  await mockApi(page)
  await stubEventSource(page)
  await page.goto('/devices')
  const card = page.getByTestId('device-card').filter({ hasText: 'Tomer Pixel' })
  await expect(card.getByText('Dizengoff St, Tel Aviv')).toBeVisible()
  await expect(card.getByText('32.0853, 34.7818')).toBeVisible()
})

test('leaving the map for Devices renders the cards and raises no page error', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await open(page)
  await mapState(page)
  await page.getByRole('link', { name: 'Devices' }).first().click()
  await expect(page).toHaveURL(/\/devices$/)
  await expect(page.getByRole('main')).not.toBeEmpty()
  expect(errors).toEqual([])
})
