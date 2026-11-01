import { defineConfig } from '@playwright/test'

const port = Number(process.env.E2E_PORT ?? 5199)
const origin = `http://127.0.0.1:${port}`

export default defineConfig({
  testDir: './e2e',
  timeout: 30_000,
  fullyParallel: true,
  reporter: 'list',
  use: {
    baseURL: origin,
    colorScheme: 'light',
    // Software GL so the WebGL map can start in headless Chromium.
    launchOptions: {
      args: [
        '--use-gl=angle',
        '--use-angle=swiftshader',
        '--enable-unsafe-swiftshader',
        '--ignore-gpu-blocklist',
      ],
    },
  },
  webServer: {
    command: `npx vite --host 127.0.0.1 --port ${port} --strictPort`,
    url: origin,
    // Never adopt a foreign process that happens to hold the port.
    reuseExistingServer: false,
    timeout: 60_000,
  },
})
