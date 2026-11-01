import { expect, test, type Page } from '@playwright/test'

import { mockApi, mockMapStyles, stubEventSource } from './mocks'

const card = (page: Page, text: string) => page.getByTestId('device-card').filter({ hasText: text })
const row = (page: Page, text: string) =>
  page.getByTestId('settings-place-row').filter({ hasText: text })

async function start(page: Page, role: 'admin' | 'viewer', path: string) {
  const state = await mockApi(page, { role })
  await mockMapStyles(page)
  await stubEventSource(page)
  await page.goto(path)
  return state
}

for (const vp of [
  { name: 'mobile', width: 375, height: 812 },
  { name: 'desktop', width: 1280, height: 800 },
]) {
  test.describe(vp.name, () => {
    test.use({ viewport: { width: vp.width, height: vp.height } })

    test('admin adds, edits and deletes a safe place', async ({ page }) => {
      const state = await start(page, 'admin', '/settings?tab=places')
      await expect(row(page, 'Home')).toBeVisible()

      await page.getByRole('button', { name: 'Add place' }).click()
      await page.getByLabel('Name').fill('Work')
      await page.getByLabel('Use a phone').selectOption({ label: 'הטלפון של נועה' })
      await expect(page.getByLabel('Latitude')).toHaveValue('31.7683')
      await page.getByLabel('Radius (metres)').fill('5')
      await page.getByRole('button', { name: 'Save' }).click()
      await expect(page.getByText('Radius must be between 10 and 50000 metres')).toBeVisible()
      await page.getByLabel('Radius (metres)').fill('200')
      await page.getByRole('button', { name: 'Save' }).click()
      await expect(page.getByTestId('dialog')).toBeHidden()
      await expect(row(page, 'Work')).toContainText('200 m radius')
      expect(state.requests.find((r) => r.method === 'POST')?.body).toEqual({
        name: 'Work',
        latitude: 31.7683,
        longitude: 35.2137,
        radius_m: 200,
      })

      // Duplicate names are rejected with a friendly message.
      await page.getByRole('button', { name: 'Add place' }).click()
      await page.getByLabel('Name').fill('home')
      await page.getByLabel('Latitude').fill('1')
      await page.getByLabel('Longitude').fill('2')
      await page.getByRole('button', { name: 'Save' }).click()
      await expect(page.getByRole('alert').filter({ hasText: 'already exists' })).toBeVisible()
      await page.getByRole('button', { name: 'Cancel' }).click()

      await row(page, 'Work').getByRole('button', { name: 'Edit Work' }).click()
      await page.getByLabel('Name').fill('Office')
      await page.getByRole('button', { name: 'Save' }).click()
      await expect(page.getByTestId('dialog')).toBeHidden()
      await expect(row(page, 'Office')).toBeVisible()
      expect(state.requests.find((r) => r.method === 'PATCH')?.body).toEqual({ name: 'Office' })

      await row(page, 'Office').getByRole('button', { name: 'Delete Office' }).click()
      await page.getByTestId('confirm-dialog').getByRole('button', { name: 'Delete' }).click()
      await expect(row(page, 'Office')).toHaveCount(0)
      expect(state.requests.some((r) => r.method === 'DELETE')).toBe(true)
    })

    test('deleting the place removes the badge from Cards', async ({ page }) => {
      await start(page, 'admin', '/settings?tab=places')
      await row(page, 'Home').getByRole('button', { name: 'Delete Home' }).click()
      await page.getByTestId('confirm-dialog').getByRole('button', { name: 'Delete' }).click()
      await expect(row(page, 'Home')).toHaveCount(0)
      await page.getByRole('link', { name: 'Devices', exact: true }).click()
      await expect(card(page, 'Tomer Pixel')).toBeVisible()
      await expect(card(page, 'Tomer Pixel').getByTestId('place-badge')).toHaveCount(0)
    })

    test('viewer sees no Safe places section but sees the badge with the address', async ({
      page,
    }) => {
      const seen: string[] = []
      page.on('request', (r) => {
        if (r.url().includes('/api/places')) seen.push(r.url())
      })
      await start(page, 'viewer', '/')
      const pixel = card(page, 'Tomer Pixel')
      await expect(pixel.getByTestId('place-badge')).toHaveText('Home')
      await expect(pixel).toContainText('Dizengoff St, Tel Aviv')
      await expect(card(page, 'הטלפון של נועה').getByTestId('place-badge')).toHaveCount(0)
      await page.goto('/settings?tab=places')
      await expect(page.getByRole('tab', { name: 'Safe places' })).toHaveCount(0)
      await expect(page.getByRole('heading', { name: 'Safe places' })).toHaveCount(0)
      expect(seen).toEqual([])
    })

    test('map list shows the place before the address', async ({ page }) => {
      await start(page, 'viewer', '/map')
      if (vp.name === 'mobile') await page.getByRole('button', { name: /3 phones/ }).click()
      await expect(page.getByTestId('map-device-list')).toContainText(
        'Home · Dizengoff St, Tel Aviv',
      )
      const fitAll = page.getByRole('button', { name: 'Fit all' })
      const failed = page.getByRole('alert').filter({ hasText: /map could not start/ })
      await expect(fitAll.or(failed)).toBeVisible({ timeout: 15_000 })
      if (await fitAll.isVisible()) {
        await page.getByRole('button', { name: 'Show Tomer Pixel on the map' }).click()
        const tip = page.getByTestId('map-tooltip')
        await expect(tip.getByTestId('place-badge')).toHaveText('Home')
        await expect(tip).toContainText('Dizengoff St, Tel Aviv')
      }
    })
  })
}
