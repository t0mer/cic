import { expect, test, type Page } from '@playwright/test'

import { BACKUP_CODE, mockApi, PASSWORD, stubEventSource, TOTP_CODE } from './mocks'

async function password(page: Page) {
  await page.getByLabel('Username').fill('tomer')
  await page.getByLabel('Password', { exact: true }).fill(PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
}

for (const vp of [
  { name: 'mobile', width: 375, height: 812 },
  { name: 'desktop', width: 1280, height: 800 },
]) {
  test.describe(vp.name, () => {
    test.use({ viewport: { width: vp.width, height: vp.height } })

    test('enable, sign in with a code and a single-use backup code, disable', async ({ page }) => {
      const state = await mockApi(page, { role: 'admin' })
      await stubEventSource(page)
      await page.goto('/settings?tab=account')

      // Enable
      await expect(page.getByText('Off. Add a second step')).toBeVisible()
      await page.getByRole('button', { name: 'Enable two-factor' }).click()
      const dialog = page.getByTestId('dialog')
      await dialog.getByLabel('Current password').fill('wrong')
      await dialog.getByRole('button', { name: 'Continue' }).click()
      await expect(dialog.getByRole('alert')).toHaveText('Incorrect password or code.')
      await dialog.getByLabel('Current password').fill(PASSWORD)
      await dialog.getByRole('button', { name: 'Continue' }).click()
      await expect(dialog.getByAltText('QR code for your authenticator app')).toBeVisible()
      await expect(dialog.getByTestId('totp-secret')).toHaveText('JBSWY3DPEHPK3PXP')
      await dialog.getByLabel('Code from the app').fill('000000')
      await dialog.getByRole('button', { name: 'Turn on' }).click()
      await expect(dialog.getByRole('alert')).toHaveText('Incorrect password or code.')
      await dialog.getByLabel('Code from the app').fill(TOTP_CODE)
      await dialog.getByRole('button', { name: 'Turn on' }).click()
      await expect(dialog.getByTestId('backup-codes')).toContainText(BACKUP_CODE)

      const close = dialog.getByRole('button', { name: 'Close', exact: true }).last()
      await expect(close).toBeDisabled()

      // History navigation from the codes screen asks first; declining keeps the codes
      const prompts: string[] = []
      const answer = { accept: false }
      page.on('dialog', (d) => {
        if (d.type() === 'confirm') prompts.push(d.message())
        void (answer.accept ? d.accept() : d.dismiss())
      })
      await page.evaluate(() => history.pushState({}, '', '/settings?tab=account#x'))
      await page.goBack()
      await expect.poll(() => prompts.length).toBeGreaterThan(0)
      expect(prompts[0]).toBe('Leave without saving your backup codes?')
      await expect(dialog.getByTestId('backup-codes')).toContainText(BACKUP_CODE)
      page.removeAllListeners('dialog')
      await dialog.getByLabel('I have saved these codes').check()
      await close.click()
      await expect(page.getByTestId('backup-remaining')).toHaveText('10 backup codes remaining')

      // Nothing secret reaches storage or the URL
      const leaked = await page.evaluate(
        () =>
          JSON.stringify({ ...localStorage }) +
          JSON.stringify({ ...sessionStorage }) +
          location.href,
      )
      expect(leaked).not.toContain('JBSWY3DPEHPK3PXP')
      expect(leaked).not.toContain('ABCDE-FGH23')

      // Log out, log in with the TOTP code (auto-submits at 6 digits)
      await page.getByRole('button', { name: 'Sign out' }).click()
      await password(page)
      await expect(page.getByRole('heading', { name: 'Two-factor authentication' })).toBeVisible()
      await page.getByLabel('Authentication code').fill('111111')
      await expect(page.getByRole('alert')).toHaveText('Invalid code')
      await page.getByLabel('Authentication code').fill(TOTP_CODE)
      await expect(page.getByRole('heading', { name: 'Account' })).toBeVisible()

      // Back returns to the password step
      await page.getByRole('button', { name: 'Sign out' }).click()
      await password(page)
      await page.getByRole('button', { name: 'Back', exact: true }).click()
      await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()

      // Backup code works once
      await password(page)
      await page.getByRole('button', { name: 'Use a backup code' }).click()
      await page.getByLabel('Backup code').fill(BACKUP_CODE)
      await page.getByRole('button', { name: 'Verify' }).click()
      await expect(page.getByRole('heading', { name: 'Account' })).toBeVisible()
      await expect(page.getByTestId('backup-remaining')).toHaveText('9 backup codes remaining')

      await page.getByRole('button', { name: 'Sign out' }).click()
      await password(page)
      await page.getByRole('button', { name: 'Use a backup code' }).click()
      await page.getByLabel('Backup code').fill(BACKUP_CODE)
      await page.getByRole('button', { name: 'Verify' }).click()
      await expect(page.getByRole('alert')).toHaveText('Invalid backup code')
      await page.getByLabel('Backup code').fill('')
      await page.getByRole('button', { name: 'Use authenticator code' }).click()
      await page.getByLabel('Authentication code').fill(TOTP_CODE)
      await expect(page.getByRole('heading', { name: 'Account' })).toBeVisible()

      // Disable
      await page.getByRole('button', { name: 'Turn off' }).click()
      await dialog.getByLabel('Current password').fill(PASSWORD)
      await dialog.getByLabel('Authentication code').fill(TOTP_CODE)
      await dialog.getByRole('button', { name: 'Turn off' }).click()
      await expect(page.getByText('Off. Add a second step')).toBeVisible()
      expect(state.twoFactor.enabled).toBe(false)
    })

    test('regenerate backup codes shows the new set once', async ({ page }) => {
      const state = await mockApi(page, { role: 'viewer' })
      state.twoFactor = { enabled: true, pending: false, backupCodes: [BACKUP_CODE] }
      await stubEventSource(page)
      await page.goto('/settings?tab=account')
      await expect(page.getByTestId('backup-remaining')).toContainText('1 backup code remaining')
      await page.getByRole('button', { name: 'Regenerate backup codes' }).click()
      const dialog = page.getByTestId('dialog')
      await dialog.getByLabel('Current password').fill(PASSWORD)
      await dialog.getByLabel('Authentication code').fill(TOTP_CODE)
      await dialog.getByRole('button', { name: 'Generate new codes' }).click()
      await expect(dialog.getByTestId('backup-codes')).toContainText('CODE0-ZZ990')
    })
  })
}
