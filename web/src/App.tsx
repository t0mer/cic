import { lazy, Suspense } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createBrowserRouter, Navigate, RouterProvider, useRouteError } from 'react-router-dom'

import { ErrorBoundary } from '@/components/error-boundary'
import { ThemeProvider } from '@/components/theme-provider'
import { Toaster } from '@/components/ui/sonner'
import { ActivityPage } from '@/features/activity/ActivityPage'
import { AuthGate } from '@/features/auth/AuthGate'
import { AuthProvider } from '@/features/auth/AuthProvider'
import { DevicesPage } from '@/features/devices/DevicesPage'
import { HistoryPage } from '@/features/history/HistoryPage'
import { AppShell } from '@/features/shell/AppShell'
import { SettingsPage } from '@/features/settings/SettingsPage'

// maplibre-gl is large: keep it (and the map page) out of the main bundle.
const MapPage = lazy(() => import('@/features/map/MapPage').then((m) => ({ default: m.MapPage })))

const queryClient = new QueryClient({
  defaultOptions: { queries: { refetchOnWindowFocus: true, staleTime: 10_000 } },
})

// Hands a route's render error to the ErrorBoundary, so a failing page shows the same
// message as before the data router instead of React Router's default error screen.
function RouteError() {
  const error = useRouteError()
  return (
    <ErrorBoundary>
      <Rethrow error={error} />
    </ErrorBoundary>
  )
}

function Rethrow({ error }: { error: unknown }): never {
  throw error
}

// A data router (not BrowserRouter) so useBlocker can guard unsaved one-time data.
const router = createBrowserRouter([
  {
    element: <AppShell />,
    errorElement: <RouteError />,
    children: [
      { index: true, element: <Navigate to="/devices" replace /> },
      { path: '/devices', element: <DevicesPage /> },
      {
        path: '/map',
        element: (
          <ErrorBoundary>
            <Suspense
              fallback={
                <div role="status" className="p-4 text-sm text-muted-foreground">
                  Loading map…
                </div>
              }
            >
              <MapPage />
            </Suspense>
          </ErrorBoundary>
        ),
      },
      { path: '/devices/:prefix/:device/history', element: <HistoryPage /> },
      { path: '/activity', element: <ActivityPage /> },
      { path: '/settings', element: <SettingsPage /> },
      { path: '*', element: <Navigate to="/devices" replace /> },
    ],
  },
])

export default function App() {
  return (
    <ThemeProvider>
      <QueryClientProvider client={queryClient}>
        <AuthProvider>
          <AuthGate>
            <RouterProvider router={router} />
          </AuthGate>
        </AuthProvider>
        <Toaster />
      </QueryClientProvider>
    </ThemeProvider>
  )
}
