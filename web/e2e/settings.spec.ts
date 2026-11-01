import { expect, test, type Page } from '@playwright/test'

import { GENERATED_PASSWORD, mockApi, RESET_PASSWORD, stubEventSource } from './mocks'

const settingsRow = (page: Page, text: string) =>
  page.getByTestId('settings-device-row').filter({ hasText: text })
const card = (page: Page, text: string) => page.getByTestId('device-card').filter({ hasText: text })

async function start(page: Page, role: 'admin' | 'viewer', path = '/settings') {
  const state = await mockApi(page, { role })
  await stubEventSource(page)
  await page.goto(path)
  return state
}

async function goTo(page: Page, name: 'Devices' | 'Settings') {
  await page.getByRole('link', { name, exact: true }).click()
}

for (const vp of [
  { name: 'mobile', width: 375, height: 812 },
  { name: 'desktop', width: 1280, height: 800 },
]) {
  test.describe(vp.name, () => {
    test.use({ viewport: { width: vp.width, height: vp.height } })

    test('viewer sees only the Account tab and no admin request is made', async ({ page }) => {
      const seen: string[] = []
      page.on('request', (r) => {
        if (r.url().includes('/api/'))
          seen.push(new URL(r.url()).pathname + new URL(r.url()).search)
      })
      await start(page, 'viewer', '/settings?tab=users')
      await expect(page.getByRole('tab')).toHaveCount(1)
      await expect(page.getByRole('tab', { name: 'Account' })).toBeVisible()
      await expect(page.getByRole('heading', { name: 'Account' })).toBeVisible()
      await expect(page.getByText('viewer', { exact: true })).toBeVisible()
      expect(seen.some((p) => p.startsWith('/api/users') || p.includes('include_hidden'))).toBe(
        false,
      )
      expect(seen.some((p) => p.startsWith('/api/geocode'))).toBe(false)
      const noScroll = await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      )
      expect(noScroll).toBe(true)
    })

    test('admin edits a device name and colour and sees it on Cards', async ({ page }) => {
      const state = await start(page, 'admin')
      await expect(page.getByTestId('settings-device-row')).toHaveCount(3)
      await settingsRow(page, 'Tomer Pixel')
        .getByRole('button', { name: 'Edit Tomer Pixel' })
        .click()
      const save = page.getByRole('button', { name: 'Save' })
      await expect(save).toBeDisabled()
      await page.getByLabel('Full name').fill('Tomer Phone')
      await page.getByRole('button', { name: 'Blue #1d4ed8' }).click()
      await expect(page.getByTestId('edit-preview')).toHaveText('Tomer Phone')
      await save.click()
      await expect(page.getByTestId('dialog')).toBeHidden()
      const patch = state.requests.find((r) => r.method === 'PATCH')
      expect(patch?.body).toEqual({ full_name: 'Tomer Phone', color: '#1d4ed8' })
      expect(patch?.csrf).toBe(state.csrf)
      await expect(settingsRow(page, 'Tomer Phone')).toBeVisible()
      await goTo(page, 'Devices')
      await expect(card(page, 'Tomer Phone')).toBeVisible()
      await expect(card(page, 'Tomer Pixel')).toHaveCount(0)
      await expect(card(page, 'Tomer Phone').getByTestId('device-avatar')).toHaveCSS(
        'background-color',
        'rgb(29, 78, 216)',
      )
    })

    test('hiding removes the device from Cards but keeps it in Settings', async ({ page }) => {
      await start(page, 'admin')
      await settingsRow(page, 'Spare phone')
        .getByRole('button', { name: 'Edit Spare phone' })
        .click()
      await page.getByRole('switch', { name: 'Hidden' }).check()
      await page.getByRole('button', { name: 'Save' }).click()
      await expect(page.getByTestId('dialog')).toBeHidden()
      await expect(
        settingsRow(page, 'Spare phone').getByText('Hidden', { exact: true }),
      ).toBeVisible()
      await goTo(page, 'Devices')
      await expect(page.getByTestId('device-card')).toHaveCount(2)
      await expect(card(page, 'Spare phone')).toHaveCount(0)
      await goTo(page, 'Settings')
      await expect(settingsRow(page, 'Spare phone')).toBeVisible()
    })

    test('deleting asks first, then removes the device everywhere', async ({ page }) => {
      const state = await start(page, 'admin')
      await settingsRow(page, 'Spare phone')
        .getByRole('button', { name: 'Delete Spare phone' })
        .click()
      const dlg = page.getByTestId('confirm-dialog')
      await expect(dlg).toContainText('reappear if the phone publishes again')
      await dlg.getByRole('button', { name: 'Cancel' }).click()
      await expect(settingsRow(page, 'Spare phone')).toBeVisible()
      await settingsRow(page, 'Spare phone')
        .getByRole('button', { name: 'Delete Spare phone' })
        .click()
      await page.getByTestId('confirm-dialog').getByRole('button', { name: 'Delete' }).click()
      await expect(settingsRow(page, 'Spare phone')).toHaveCount(0)
      expect(state.requests.filter((r) => r.method === 'DELETE')).toHaveLength(1)
      await goTo(page, 'Devices')
      await expect(page.getByTestId('device-card')).toHaveCount(2)
    })

    test('Cards overflow menu offers Edit and Delete', async ({ page }) => {
      await start(page, 'admin', '/devices')
      await card(page, 'Tomer Pixel')
        .getByRole('button', { name: 'More actions for Tomer Pixel' })
        .click()
      await page.getByRole('menuitem', { name: 'Edit name/color…' }).click()
      await page.getByLabel('Full name').fill('Renamed')
      await page.getByRole('button', { name: 'Save' }).click()
      await expect(card(page, 'Renamed')).toBeVisible()
      await card(page, 'Renamed').getByRole('button', { name: 'More actions for Renamed' }).click()
      await page.getByRole('menuitem', { name: 'Delete…' }).click()
      await page.getByTestId('confirm-dialog').getByRole('button', { name: 'Delete' }).click()
      await expect(page.getByTestId('device-card')).toHaveCount(2)
    })

    test('creating a user shows the generated password once and gates Close', async ({ page }) => {
      const state = await start(page, 'admin', '/settings?tab=users')
      await expect(page.getByTestId('user-row')).toHaveCount(2)
      await page.getByRole('button', { name: 'Add user' }).click()
      await page.getByLabel('Username').fill('newbie')
      await page.getByRole('button', { name: 'Create user' }).click()
      const pw = page.getByTestId('one-time-password')
      await expect(pw).toHaveText(GENERATED_PASSWORD)
      const close = page.getByRole('button', { name: 'Close' }).last()
      await expect(close).toBeDisabled()
      await page.keyboard.press('Escape')
      await expect(pw).toBeVisible()
      await page.getByRole('checkbox', { name: 'I have saved it' }).check()
      await expect(close).toBeEnabled()
      await close.click()
      await expect(pw).toHaveCount(0)
      await expect(page.getByTestId('user-row').filter({ hasText: 'newbie' })).toBeVisible()
      const stored = await page.evaluate(
        () =>
          JSON.stringify({ ...localStorage }) +
          JSON.stringify({ ...sessionStorage }) +
          location.href,
      )
      expect(stored).not.toContain(GENERATED_PASSWORD)
      expect(await page.content()).not.toContain(GENERATED_PASSWORD)
      expect(state.users.some((u) => u.username === 'newbie')).toBe(true)
    })

    test('resetting a password confirms, then reveals the new one once', async ({ page }) => {
      await start(page, 'admin', '/settings?tab=users')
      const dana = page.getByTestId('user-row').filter({ hasText: 'dana' })
      await dana.getByRole('button', { name: 'Reset password' }).click()
      await page
        .getByTestId('confirm-dialog')
        .getByRole('button', { name: 'Reset password' })
        .click()
      await expect(page.getByTestId('one-time-password')).toHaveText(RESET_PASSWORD)
      await page.getByRole('checkbox', { name: 'I have saved it' }).check()
      await page.getByRole('button', { name: 'Close' }).last().click()
      await expect(page.getByTestId('one-time-password')).toHaveCount(0)
      await expect(dana).toContainText('Must change password')
    })

    test('you cannot demote or disable yourself, others can be disabled and enabled', async ({
      page,
    }) => {
      const state = await start(page, 'admin', '/settings?tab=users')
      const me = page.getByTestId('user-row').filter({ hasText: 'tomer' })
      for (const name of ['Make viewer', 'Disable']) {
        const b = me.getByRole('button', { name })
        await expect(b).toHaveAttribute('aria-disabled', 'true')
        await expect(b).toHaveAttribute('title', /cannot change your own/)
        await b.click({ force: true })
      }
      expect(state.requests.filter((r) => r.method === 'PATCH')).toHaveLength(0)
      const dana = page.getByTestId('user-row').filter({ hasText: 'dana' })
      await dana.getByRole('button', { name: 'Disable' }).click()
      await page.getByTestId('confirm-dialog').getByRole('button', { name: 'Disable' }).click()
      await expect(dana).toContainText('Disabled')
      await dana.getByRole('button', { name: 'Enable' }).click()
      await expect(dana).toContainText('Active')
    })

    test('system and geocoding tabs render, geocoding handles a 404', async ({ page }) => {
      const state = await start(page, 'admin', '/settings?tab=geocoding')
      await expect(page.getByRole('meter')).toHaveAttribute('aria-valuenow', '25')
      state.geocodeUsage = null
      await page.goto('/settings?tab=geocoding')
      await expect(page.getByText('Geocoding not available')).toBeVisible()
      await page.getByRole('tab', { name: 'System' }).click()
      await expect(page.getByTestId('system-broker-state')).toHaveText('Connected')
      await expect(page.getByText('mqtt.example.net:8883')).toBeVisible()
      await expect(page.getByText('Test map data')).toBeVisible()
    })
  })
}
