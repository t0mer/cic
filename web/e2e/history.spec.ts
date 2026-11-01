import { expect, test, type Page } from '@playwright/test'

import { mockApi, mockMapStyles, stubEventSource } from './mocks'

async function open(page: Page, role: 'admin' | 'viewer', path = '/devices') {
  const state = await mockApi(page, { role })
  await mockMapStyles(page)
  await stubEventSource(page)
  await page.goto(path)
  return state
}

const noHScroll = (page: Page) =>
  page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)

test('admin opens History from a card, switches the range and sees the days and route', async ({
  page,
}) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await open(page, 'admin')
  const card = page.getByTestId('device-card').filter({ hasText: 'Tomer Pixel' })
  await card.getByRole('link', { name: /Location history/ }).click()
  await expect(page).toHaveURL(/\/devices\/dradis\/pixel\/history$/)
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Tomer Pixel')

  await page.getByRole('button', { name: 'Last 7 days' }).click()
  await expect(page).toHaveURL(/from=.*&to=|to=.*&from=/)
  const rows = page.getByTestId('day-row')
  await expect(rows).toHaveCount(4)
  await expect(rows.first()).toContainText('12 points')
  expect(await noHScroll(page)).toBe(true)

  // hiding a day dims its row and drops it from the legend
  const legend = page.getByTestId('history-legend').getByRole('listitem')
  const before = await legend.count()
  await rows
    .first()
    .getByRole('button', { name: /^Hide / })
    .click()
  await expect(legend).toHaveCount(before - 1)

  await page.getByRole('tab', { name: 'Points' }).click()
  await expect(page.getByRole('table', { name: 'Recorded points' })).toBeVisible()
  await expect(page.getByText(/Page 1 of 1 · 48 points/)).toBeVisible()

  // The route layer needs WebGL: skip (visibly) when the browser has none.
  await page.getByRole('tab', { name: 'Route' }).click()
  const fit = page.getByRole('button', { name: 'Fit route' })
  const unavailable = page.getByRole('alert').filter({ hasText: /map could not start/ })
  await expect(fit.or(unavailable)).toBeVisible({ timeout: 15_000 })
  test.skip(await unavailable.isVisible(), 'WebGL is unavailable in this browser')
  await fit.click()
  await expect(page.getByTestId('history-map-canvas')).toBeVisible()
  expect(errors).toEqual([])
})

test('shows the empty state for a range without points and the sample notice', async ({ page }) => {
  const state = await open(page, 'admin', '/devices/dradis/pixel/history')
  await expect(page.getByText('No locations recorded in this range')).toBeVisible()
  state.historyTruncated = true
  await page.getByRole('button', { name: 'Last 7 days' }).click()
  await expect(page.getByTestId('day-row').first()).toBeVisible()
  await page.getByRole('tab', { name: 'Points' }).click()
  await expect(page.getByText(/Showing a sample of the points/)).toBeVisible()
})

test('shows an error with retry', async ({ page }) => {
  const state = await open(page, 'admin', '/devices/dradis/pixel/history')
  await expect(page.getByText('No locations recorded in this range')).toBeVisible()
  state.historyFails = true
  await page.getByRole('button', { name: 'Yesterday' }).click()
  await expect(page.getByText('Could not load history')).toBeVisible()
  state.historyFails = false
  await page.getByRole('button', { name: 'Retry' }).click()
  await expect(page.getByText('Could not load history')).toHaveCount(0)
})

test('a range over 31 days is rejected before any request', async ({ page }) => {
  await open(
    page,
    'admin',
    '/devices/dradis/pixel/history?from=2026-08-01T00:00&to=2026-10-01T00:00',
  )
  await expect(page.getByRole('alert')).toContainText('at most 31 days')
  await expect(page.getByTestId('day-row')).toHaveCount(0)
})

test('mobile layout has no horizontal scroll', async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 })
  await open(page, 'admin', '/devices/dradis/pixel/history')
  await page.getByRole('button', { name: 'Last 7 days' }).click()
  await expect(page.getByTestId('day-row').first()).toBeVisible()
  expect(await noHScroll(page)).toBe(true)
  await page.getByRole('tab', { name: 'Points' }).click()
  await expect(page.getByRole('table', { name: 'Recorded points' })).toBeVisible()
  expect(await noHScroll(page)).toBe(true)
})

test('viewer sees no History link and the page is blocked', async ({ page }) => {
  await open(page, 'viewer')
  await expect(page.getByTestId('device-card').first()).toBeVisible()
  await expect(page.getByRole('link', { name: /Location history/ })).toHaveCount(0)
  await page.goto('/devices/dradis/pixel/history')
  await expect(page.getByText(/for administrators only/)).toBeVisible()
})
