import { expect, test } from '@playwright/test'

import { devices, emitEvent, mockApi, stubEventSource } from './mocks'

for (const vp of [
  { name: 'mobile', width: 375, height: 812 },
  { name: 'desktop', width: 1280, height: 800 },
]) {
  test.describe(vp.name, () => {
    test.use({ viewport: { width: vp.width, height: vp.height } })

    test.beforeEach(async ({ page }) => {
      await mockApi(page)
      await stubEventSource(page)
      await page.goto('/')
    })

    test('renders cards without horizontal scroll and the right navigation', async ({ page }) => {
      await expect(page).toHaveURL(/\/devices$/)
      const cards = page.getByTestId('device-card')
      await expect(cards).toHaveCount(3)
      await expect(page.getByRole('heading', { name: 'הטלפון של נועה' })).toBeVisible()
      await expect(page.getByText('Never seen')).toBeVisible()
      const noScroll = await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      )
      expect(noScroll).toBe(true)
      if (vp.name === 'mobile') {
        await expect(page.getByTestId('tabbar')).toBeVisible()
        await expect(page.getByTestId('sidebar')).toBeHidden()
      } else {
        await expect(page.getByTestId('sidebar')).toBeVisible()
        await expect(page.getByTestId('tabbar')).toBeHidden()
      }
    })

    test('search and status filter work and persist in the URL', async ({ page }) => {
      await expect(page.getByTestId('device-card')).toHaveCount(3)
      await page.getByRole('searchbox').fill('נועה')
      await expect(page.getByTestId('device-card')).toHaveCount(1)
      await expect(page).toHaveURL(/q=/)
      await page.getByRole('searchbox').fill('')
      await page.getByRole('button', { name: 'Online', exact: true }).click()
      await expect(page.getByTestId('device-card')).toHaveCount(1)
      await expect(page.getByRole('heading', { name: 'Tomer Pixel' })).toBeVisible()
      await page.getByRole('button', { name: 'Offline', exact: true }).click()
      await expect(page.getByTestId('device-card')).toHaveCount(2)
    })

    test('theme toggles to dark', async ({ page }) => {
      await expect(page.getByTestId('device-card')).toHaveCount(3)
      const toggle = page.getByTestId('theme-toggle')
      const html = page.locator('html')
      await expect(html).not.toHaveClass(/dark/)
      await toggle.click() // system -> light
      await toggle.click() // light -> dark
      await expect(html).toHaveClass(/dark/)
    })
  })
}

test('live events add, update and remove cards', async ({ page }) => {
  await mockApi(page)
  await stubEventSource(page)
  await page.goto('/devices')
  await expect(page.getByTestId('device-card')).toHaveCount(3)
  await expect(page.getByText(/Live updates reconnecting/)).toHaveCount(0)

  const fresh = { ...devices[0], key: 'dradis/new', device: 'new', full_name: 'Fresh phone' }
  await emitEvent(page, 'device.updated', fresh)
  await expect(page.getByTestId('device-card')).toHaveCount(4)
  await expect(page.getByRole('heading', { name: 'Fresh phone' })).toBeVisible()

  const pixel = page.getByTestId('device-card').filter({ hasText: 'Tomer Pixel' })
  await expect(pixel.getByText('Online', { exact: true })).toBeVisible()
  await emitEvent(page, 'device.updated', { ...devices[0], online: false })
  await expect(pixel.getByText('Offline', { exact: true })).toBeVisible()

  await emitEvent(page, 'device.removed', { key: 'dradis/new' })
  await expect(page.getByTestId('device-card')).toHaveCount(3)
  await expect(page.getByRole('heading', { name: 'Fresh phone' })).toHaveCount(0)
})
