import { expect, test } from '@playwright/test'

import { mockApi, stubEventSource } from './mocks'

const dir = process.env.SHOTS_DIR
test.skip(!dir, 'set SHOTS_DIR to capture review screenshots')

for (const scheme of ['light', 'dark'] as const) {
  for (const w of [375, 1280]) {
    test.describe(`${w} ${scheme}`, () => {
      test.use({ colorScheme: scheme, viewport: { width: w, height: w === 375 ? 812 : 800 } })
      const tag = `${w}-${scheme}`

      test(`settings devices, edit dialog ${tag}`, async ({ page }) => {
        await mockApi(page, { role: 'admin' })
        await stubEventSource(page)
        await page.goto('/settings')
        await expect(page.getByTestId('settings-device-row')).toHaveCount(3)
        await page.screenshot({ path: `${dir}/settings-devices-${tag}.png` })
        await page.getByRole('button', { name: 'Edit Spare phone' }).click()
        await page.getByLabel('Full name').fill('Spare phone (office)')
        await page.getByRole('button', { name: 'Teal #0f766e' }).click()
        await page.waitForTimeout(400)
        await page.screenshot({ path: `${dir}/edit-dialog-${tag}.png` })
      })

      test(`users, one-time password ${tag}`, async ({ page }) => {
        await mockApi(page, { role: 'admin' })
        await stubEventSource(page)
        await page.goto('/settings?tab=users')
        await expect(page.getByTestId('user-row')).toHaveCount(2)
        await page.screenshot({ path: `${dir}/users-${tag}.png` })
        await page.getByRole('button', { name: 'Add user' }).click()
        await page.getByLabel('Username').fill('newbie')
        await page.getByRole('button', { name: 'Create user' }).click()
        await expect(page.getByTestId('one-time-password')).toBeVisible()
        await page.waitForTimeout(400)
        await page.screenshot({ path: `${dir}/one-time-password-${tag}.png` })
      })
    })
  }
}
