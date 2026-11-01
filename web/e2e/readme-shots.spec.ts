import { mkdirSync } from 'node:fs'

import { expect, test, type Page } from '@playwright/test'

import { PASSWORD, stubEventSource } from './mocks'
import { readmeApi } from './readme-fixtures'

// Writes sanitized README screenshots from the mocked API only. Run with README_SHOTS=1.
test.skip(!process.env.README_SHOTS, 'set README_SHOTS=1 to capture README screenshots')

const OUT = '/opt/dev/cic/assets/screenshots'
mkdirSync(OUT, { recursive: true })

let realTiles = true
test.beforeAll(async ({ request }) => {
  try {
    const res = await request.get('https://tiles.openfreemap.org/styles/positron', {
      timeout: 10_000,
    })
    realTiles = res.ok()
  } catch {
    realTiles = false
  }
  console.log(`README_SHOTS tiles: ${realTiles ? 'real OpenFreeMap' : 'mocked blank style'}`)
})

const DESKTOP = { width: 1280, height: 800 }
const MOBILE = { width: 390, height: 844 }

type Variant = 'light' | 'dark' | 'mobile'
const variants: { v: Variant; scheme: 'light' | 'dark'; size: typeof DESKTOP }[] = [
  { v: 'light', scheme: 'light', size: DESKTOP },
  { v: 'dark', scheme: 'dark', size: DESKTOP },
  { v: 'mobile', scheme: 'light', size: MOBILE },
]

/** Uses the app's own theme toggle until the page has the wanted scheme. */
async function applyTheme(page: Page, scheme: 'light' | 'dark') {
  const toggle = page.getByTestId('theme-toggle')
  for (let i = 0; i < 3; i++) {
    const dark = await page.evaluate(() => document.documentElement.classList.contains('dark'))
    if (dark === (scheme === 'dark')) return
    if (!(await toggle.isVisible())) return
    await toggle.click()
  }
}

async function mapReady(page: Page) {
  const fit = page.getByRole('button', { name: /^Fit (all|route)$/ })
  await expect(fit).toBeVisible({ timeout: 20_000 })
  await fit.click()
  await page.waitForTimeout(3000)
}

type Shot = {
  name: string
  mobile?: boolean
  authed?: boolean
  run: (page: Page, v: Variant) => Promise<void>
}

const shots: Shot[] = [
  {
    name: 'cards',
    mobile: true,
    run: async (page) => {
      await page.goto('/devices')
      await expect(page.getByTestId('device-card')).toHaveCount(3)
      await expect(page.getByText('Rothschild Blvd, Tel Aviv')).toBeVisible()
    },
  },
  {
    name: 'map',
    mobile: true,
    run: async (page, v) => {
      await page.goto('/map')
      await expect(page.getByRole('heading', { name: 'Map', level: 1 })).toBeAttached()
      await mapReady(page)
      if (v === 'mobile') await page.getByRole('button', { name: /3 phones/ }).click()
      await page.getByRole('button', { name: 'Show Alex Pixel on the map' }).click()
      await expect(page.getByTestId('map-tooltip')).toBeVisible()
      await page.waitForTimeout(2500)
    },
  },
  {
    name: 'actions',
    run: async (page) => {
      await page.goto('/devices')
      await page.getByRole('button', { name: 'Send SMS from Alex Pixel' }).click()
      await page.getByLabel('Phone number').fill('+15550100123')
      await page.getByLabel('Message').fill('Running late, see you at 6!')
      await page.waitForTimeout(500)
    },
  },
  {
    name: 'history',
    run: async (page) => {
      await page.goto('/devices/dradis/alex-pixel/history')
      await page.getByRole('button', { name: 'Last 7 days' }).click()
      await expect(page.getByTestId('day-row')).toHaveCount(4)
      await page.getByRole('tab', { name: 'Route' }).click()
      await mapReady(page)
      await page.evaluate(() => window.scrollTo(0, 270))
    },
  },
  {
    name: 'settings-devices',
    run: async (page) => {
      await page.goto('/settings?tab=devices')
      await expect(page.getByTestId('settings-device-row')).toHaveCount(3)
    },
  },
  {
    name: 'settings-places',
    run: async (page) => {
      await page.goto('/settings?tab=places')
      await expect(page.getByText('Work').first()).toBeVisible()
      await page.waitForTimeout(500)
    },
  },
  {
    name: 'settings-geocoding',
    run: async (page) => {
      await page.goto('/settings?tab=geocoding')
      await expect(page.getByRole('tab', { name: 'Geocoding' })).toBeVisible()
      await page.waitForTimeout(800)
    },
  },
  {
    name: 'settings-users',
    run: async (page) => {
      await page.goto('/settings?tab=users')
      await expect(page.getByTestId('user-row')).toHaveCount(2)
    },
  },
  {
    name: 'settings-account-2fa',
    run: async (page) => {
      await page.goto('/settings?tab=account')
      await expect(page.getByTestId('backup-remaining')).toHaveText('10 backup codes remaining')
      await page.getByTestId('backup-remaining').scrollIntoViewIfNeeded()
      await page.evaluate(() => window.scrollBy(0, 200))
    },
  },
  {
    name: 'login',
    authed: false,
    run: async (page) => {
      await page.goto('/')
      await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
      await page.getByLabel('Username').fill('admin')
    },
  },
  {
    name: 'login-2fa',
    authed: false,
    run: async (page) => {
      await page.goto('/')
      await page.getByLabel('Username').fill('admin')
      await page.getByLabel('Password', { exact: true }).fill(PASSWORD)
      await page.getByRole('button', { name: 'Sign in' }).click()
      await expect(page.getByRole('heading', { name: 'Two-factor authentication' })).toBeVisible()
      await page.getByLabel('Authentication code').focus()
    },
  },
]

for (const s of shots) {
  for (const { v, scheme, size } of variants) {
    if (v === 'mobile' && !s.mobile) continue
    test(`${s.name}-${v}`, async ({ page }) => {
      test.setTimeout(180_000)
      await page.setViewportSize(size)
      await page.emulateMedia({
        colorScheme: s.authed === false || s.name === 'actions' ? scheme : 'light',
      })
      await readmeApi(page, {
        role: s.authed === false ? null : 'admin',
        realTiles,
        twoFactor: s.name === 'settings-account-2fa' || s.name === 'login-2fa',
      })
      await stubEventSource(page)
      await s.run(page, v)
      await applyTheme(page, scheme)
      await page.waitForTimeout(1200)
      if (s.name === 'map' || s.name === 'history') await page.waitForTimeout(2500)
      await page.screenshot({ path: `${OUT}/${s.name}-${v}.png` })
    })
  }
}
