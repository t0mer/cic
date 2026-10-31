import { expect, test } from '@playwright/test'

import { mockApi, stubEventSource } from './mocks'

const dir = process.env.SHOTS_DIR
test.skip(!dir, 'set SHOTS_DIR to capture review screenshots')

for (const vp of [
  { w: 375, h: 812 },
  { w: 1280, h: 800 },
]) {
  for (const scheme of ['light', 'dark'] as const) {
    test(`shot ${vp.w} ${scheme}`, async ({ page }) => {
      await page.setViewportSize({ width: vp.w, height: vp.h })
      await page.emulateMedia({ colorScheme: scheme })
      await mockApi(page)
      await stubEventSource(page)
      await page.goto('/devices')
      await expect(page.getByTestId('device-card')).toHaveCount(3)
      await page.screenshot({ path: `${dir}/devices-${vp.w}-${scheme}.png`, fullPage: true })
    })
  }
}
