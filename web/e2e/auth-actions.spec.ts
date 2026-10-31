import { expect, test, type Page } from '@playwright/test'

import { emitEvent, mockApi, PASSWORD, stubEventSource } from './mocks'

const pixel = (page: Page) => page.getByTestId('device-card').filter({ hasText: 'Tomer Pixel' })
const noa = (page: Page) => page.getByTestId('device-card').filter({ hasText: 'הטלפון של נועה' })

async function signIn(page: Page, password = PASSWORD) {
  await page.getByLabel('Username').fill('tomer')
  await page.getByLabel('Password', { exact: true }).fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
}

for (const vp of [
  { name: 'mobile', width: 375, height: 812 },
  { name: 'desktop', width: 1280, height: 800 },
]) {
  test.describe(vp.name, () => {
    test.use({ viewport: { width: vp.width, height: vp.height } })

    test('login: bad password, rate limit, then success shows cards', async ({ page }) => {
      const state = await mockApi(page, { role: null })
      await stubEventSource(page)
      await page.goto('/')
      await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
      await expect(page.getByLabel('Username')).toBeFocused()

      await signIn(page, 'wrong')
      await expect(page.getByRole('alert')).toHaveText('Invalid credentials')

      state.loginRateLimitSeconds = 30
      await signIn(page)
      await expect(page.getByRole('alert')).toContainText('30s')

      state.loginRateLimitSeconds = null
      await signIn(page)
      await expect(page.getByTestId('device-card')).toHaveCount(3)
      const noStorage = await page.evaluate(
        () =>
          localStorage.length === 0 &&
          sessionStorage.length === 0 &&
          !location.href.includes('csrf'),
      )
      expect(noStorage).toBe(true)
    })

    test('password toggle reveals and hides the password', async ({ page }) => {
      await mockApi(page, { role: null })
      await stubEventSource(page)
      await page.goto('/')
      const pw = page.getByLabel('Password', { exact: true })
      await expect(pw).toHaveAttribute('type', 'password')
      await page.getByRole('button', { name: 'Show password' }).click()
      await expect(pw).toHaveAttribute('type', 'text')
    })

    test('must-change-password forces the change screen', async ({ page }) => {
      await mockApi(page, { role: null, loginMustChange: true })
      await stubEventSource(page)
      await page.goto('/')
      await signIn(page)
      await expect(page.getByRole('heading', { name: 'Choose a new password' })).toBeVisible()
      await page.getByLabel('Current password').fill(PASSWORD)
      await page.getByLabel('New password', { exact: true }).fill('another-long-password')
      await page.getByLabel('Confirm new password').fill('another-long-password')
      await page.getByRole('button', { name: 'Change password' }).click()
      await expect(page.getByTestId('device-card')).toHaveCount(3)
    })

    test('viewer sees cards with no action controls and a gated activity page', async ({
      page,
    }) => {
      await mockApi(page, { role: 'viewer' })
      await stubEventSource(page)
      await page.goto('/devices')
      await expect(page.getByTestId('device-card')).toHaveCount(3)
      await expect(page.getByTestId('device-actions')).toHaveCount(0)
      await expect(page.getByRole('button', { name: /^Ping/ })).toHaveCount(0)
      await page.goto('/activity')
      await expect(page.getByText('Not available for your role')).toBeVisible()
    })

    test('admin: ping sends the right request, shows Sent and Stop', async ({ page }) => {
      const state = await mockApi(page, { role: 'admin' })
      await stubEventSource(page)
      await page.goto('/devices')
      await expect(pixel(page).getByTestId('device-actions')).toBeVisible()
      await pixel(page).getByRole('button', { name: 'Ping Tomer Pixel' }).click()
      await expect(pixel(page).getByRole('button', { name: 'Ping Tomer Pixel' })).toHaveText('Sent')
      await expect(pixel(page).getByRole('button', { name: /Stop ping/ })).toBeVisible()
      const req = state.requests.find((r) => r.path.endsWith('/ping'))
      expect(req).toMatchObject({
        method: 'POST',
        path: '/api/devices/dradis/pixel/ping',
        csrf: 'csrf-token-1',
        body: {},
      })
      await pixel(page)
        .getByRole('button', { name: /Stop ping/ })
        .click()
      await expect.poll(() => state.requests.some((r) => r.path.endsWith('/ping/stop'))).toBe(true)
      await expect(pixel(page).getByRole('button', { name: /Stop ping/ })).toHaveCount(0)
      await expect(pixel(page).getByRole('button', { name: 'Ping Tomer Pixel' })).toHaveText(
        /Ping|Sent/,
      )
    })

    test('admin: pinging an offline device asks for confirmation first', async ({ page }) => {
      const state = await mockApi(page, { role: 'admin' })
      await stubEventSource(page)
      await page.goto('/devices')
      // aria-disabled (not disabled): Playwright treats it as non-actionable, a real user can still click it.
      const ping = noa(page).getByRole('button', { name: /^Ping/ })
      await ping.scrollIntoViewIfNeeded()
      await expect(ping).toHaveAttribute('aria-disabled', 'true')
      // Nothing may cover the button, or a real user could not click it either.
      expect(
        await ping.evaluate((el) => {
          const r = el.getBoundingClientRect()
          const top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)
          return top === el || el.contains(top)
        }),
      ).toBe(true)
      await ping.dispatchEvent('click')
      await expect(page.getByRole('alertdialog')).toBeVisible()
      expect(state.requests.filter((r) => r.path.endsWith('/ping'))).toHaveLength(0)
      await page.getByRole('button', { name: 'Send anyway' }).click()
      await expect
        .poll(() => state.requests.some((r) => r.path === '/api/devices/dradis/noa/ping'))
        .toBe(true)
    })

    test('admin: notify dialog sends title and text', async ({ page }) => {
      const state = await mockApi(page, { role: 'admin' })
      await stubEventSource(page)
      await page.goto('/devices')
      await pixel(page).getByRole('button', { name: 'Notify Tomer Pixel' }).click()
      const dialog = page.getByTestId('dialog')
      await expect(dialog).toBeVisible()
      const bottom = async () => {
        const b = await dialog.boundingBox()
        return Math.round((b?.y ?? 0) + (b?.height ?? 0))
      }
      if (vp.name === 'mobile') {
        // bottom sheet: docked to the bottom edge (after its slide-in), full width
        await expect.poll(bottom).toBe(vp.height)
        expect(Math.round((await dialog.boundingBox())?.width ?? 0)).toBe(vp.width)
      } else {
        expect((await dialog.boundingBox())?.width).toBeLessThanOrEqual(512)
        await expect.poll(bottom).toBeLessThan(vp.height)
      }
      await expect(dialog.getByLabel('Title')).toHaveValue('CIC')
      await dialog.getByRole('button', { name: 'Send notification' }).click()
      await expect(dialog.getByRole('alert')).toHaveText('Message is required')
      await dialog.getByLabel('Message').fill('  hello שלום  ')
      await expect(dialog.getByText('14/1000')).toBeVisible()
      await dialog.getByRole('button', { name: 'Send notification' }).click()
      await expect(dialog).toHaveCount(0)
      const req = state.requests.find((r) => r.path.endsWith('/notify'))
      expect(req).toMatchObject({
        method: 'POST',
        csrf: 'csrf-token-1',
        body: { title: 'CIC', text: 'hello שלום' },
      })
    })

    test('admin: SMS flow with counter, confirm and result over SSE', async ({ page }) => {
      const state = await mockApi(page, { role: 'admin' })
      await stubEventSource(page)
      await page.goto('/devices')
      await pixel(page).getByRole('button', { name: 'Send SMS from Tomer Pixel' }).click()
      const dialog = page.getByTestId('dialog')
      await dialog.getByLabel('Phone number').fill('0501234567')
      await dialog.getByLabel('Message').fill('a'.repeat(161))
      await expect(dialog.getByTestId('sms-counter')).toHaveText(
        '161 characters · 2 segments · GSM-7',
      )
      await dialog.getByLabel('Message').fill('שלום')
      await expect(dialog.getByTestId('sms-counter')).toHaveText(
        '4 characters · 1 segment · Unicode',
      )
      await dialog.getByRole('button', { name: 'Review' }).click()
      await expect(dialog.getByText(/international format/)).toHaveAttribute('role', 'alert')
      await dialog.getByLabel('Phone number').fill('+972 50 123 4567')
      await dialog.getByRole('button', { name: 'Review' }).click()
      await expect(dialog.getByTestId('sms-confirm-number')).toHaveText('+972501234567')
      expect(state.requests.filter((r) => r.path.endsWith('/sms'))).toHaveLength(0)
      await dialog.getByRole('button', { name: 'Send SMS' }).click()
      await expect(dialog).toHaveCount(0)
      const req = state.requests.find((r) => r.path.endsWith('/sms'))
      expect(req).toMatchObject({
        method: 'POST',
        path: '/api/devices/dradis/pixel/sms',
        csrf: 'csrf-token-1',
        body: { phone: '+972501234567', text: 'שלום' },
      })
      await expect(pixel(page).getByTestId('sms-status')).toHaveText('Sending SMS…')
      await emitEvent(page, 'command.result', {
        id: 'cmd-1',
        action: 'sms',
        device: 'dradis/pixel',
        status: 'confirmed_ok',
      })
      await expect(pixel(page).getByTestId('sms-status')).toHaveText('Delivered')
    })

    test('admin: server errors map to messages', async ({ page }) => {
      const state = await mockApi(page, { role: 'admin' })
      await stubEventSource(page)
      await page.goto('/devices')
      state.commandError = {
        status: 429,
        headers: { 'Retry-After': '9' },
        json: { error: 'slow down', code: 'rate_limited' },
      }
      await pixel(page).getByRole('button', { name: 'Ping Tomer Pixel' }).click()
      await expect(page.getByText('Too many requests, try again in 9s')).toBeVisible()
    })

    test('admin: overflow menu refreshes status', async ({ page }) => {
      const state = await mockApi(page, { role: 'admin' })
      await stubEventSource(page)
      await page.goto('/devices')
      await pixel(page).getByRole('button', { name: 'More actions for Tomer Pixel' }).click()
      await page.getByRole('menuitem', { name: 'Refresh status' }).click()
      await expect
        .poll(() =>
          state.requests.some((r) => r.path === '/api/devices/dradis/pixel/refresh-status'),
        )
        .toBe(true)
    })

    test('admin: activity page loads more', async ({ page }) => {
      await mockApi(page, { role: 'admin' })
      await stubEventSource(page)
      await page.goto('/activity')
      await expect(page.getByTestId('activity-list').getByRole('listitem')).toHaveCount(2)
      await page.getByRole('button', { name: 'Load more' }).click()
      await expect(page.getByTestId('activity-list').getByRole('listitem')).toHaveCount(3)
      await expect(page.getByRole('button', { name: 'Load more' })).toHaveCount(0)
    })

    test('a 401 mid-session returns to the login page', async ({ page }) => {
      const state = await mockApi(page, { role: 'admin' })
      await stubEventSource(page)
      await page.goto('/devices')
      await expect(page.getByTestId('device-card')).toHaveCount(3)
      state.user = null
      await pixel(page).getByRole('button', { name: 'Ping Tomer Pixel' }).click()
      await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
    })

    test('sign out from the user menu', async ({ page }) => {
      const state = await mockApi(page, { role: 'admin' })
      await stubEventSource(page)
      await page.goto('/devices')
      await page.getByTestId('user-menu').click()
      await expect(page.getByText('admin', { exact: true })).toBeVisible()
      await page.getByRole('menuitem', { name: 'Sign out' }).click()
      await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
      expect(state.requests.find((r) => r.path === '/api/auth/logout')?.csrf).toBe('csrf-token-1')
    })
  })
}
