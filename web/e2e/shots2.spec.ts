import { expect, test } from '@playwright/test'

import { mockApi, PASSWORD, stubEventSource } from './mocks'

const dir = process.env.SHOTS_DIR
test.skip(!dir, 'set SHOTS_DIR to capture review screenshots')
test.use({ colorScheme: 'dark' })

test('login 375 dark', async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 })
  await mockApi(page, { role: null })
  await stubEventSource(page)
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
  await page.screenshot({ path: `${dir}/login-375-dark.png` })
})

test('change password 375 dark', async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 })
  await mockApi(page, { role: null, loginMustChange: true })
  await stubEventSource(page)
  await page.goto('/')
  await page.getByLabel('Username').fill('tomer')
  await page.getByLabel('Password', { exact: true }).fill(PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Choose a new password' })).toBeVisible()
  await page.screenshot({ path: `${dir}/change-password-375-dark.png` })
})

for (const w of [375, 1280]) {
  test(`cards with actions ${w} dark`, async ({ page }) => {
    await page.setViewportSize({ width: w, height: w === 375 ? 812 : 800 })
    await mockApi(page, { role: 'admin' })
    await stubEventSource(page)
    await page.goto('/devices')
    await expect(page.getByTestId('device-actions')).toHaveCount(3)
    await page.screenshot({ path: `${dir}/cards-actions-${w}-dark.png`, fullPage: true })
  })
}

test('notify drawer 375 dark', async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 })
  await mockApi(page, { role: 'admin' })
  await stubEventSource(page)
  await page.goto('/devices')
  await page.getByRole('button', { name: 'Notify Tomer Pixel' }).click()
  await page.getByLabel('Message').fill('Please call me back when you see this')
  await page.waitForTimeout(400)
  await page.screenshot({ path: `${dir}/notify-drawer-375-dark.png` })
})

test('sms dialog 1280 dark', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await mockApi(page, { role: 'admin' })
  await stubEventSource(page)
  await page.goto('/devices')
  await page.getByRole('button', { name: 'Send SMS from Tomer Pixel' }).click()
  await page.getByLabel('Phone number').fill('+972501234567')
  await page.getByLabel('Message').fill('שלום, הגעתי הביתה ✔')
  await page.waitForTimeout(400)
  await page.screenshot({ path: `${dir}/sms-dialog-1280-dark.png` })
})
