import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'

import { ThemeProvider } from '@/components/theme-provider'
import { Toaster } from '@/components/ui/sonner'
import { ActivityPage } from '@/features/activity/ActivityPage'
import { AuthGate } from '@/features/auth/AuthGate'
import { AuthProvider } from '@/features/auth/AuthProvider'
import { DevicesPage } from '@/features/devices/DevicesPage'
import { AppShell } from '@/features/shell/AppShell'
import { ComingSoon } from '@/features/shell/ComingSoon'

const queryClient = new QueryClient({
  defaultOptions: { queries: { refetchOnWindowFocus: true, staleTime: 10_000 } },
})

export default function App() {
  return (
    <ThemeProvider>
      <QueryClientProvider client={queryClient}>
        <AuthProvider>
          <AuthGate>
            <BrowserRouter>
              <Routes>
                <Route element={<AppShell />}>
                  <Route index element={<Navigate to="/devices" replace />} />
                  <Route path="/devices" element={<DevicesPage />} />
                  <Route path="/map" element={<ComingSoon title="Map" />} />
                  <Route path="/activity" element={<ActivityPage />} />
                  <Route path="/settings" element={<ComingSoon title="Settings" />} />
                  <Route path="*" element={<Navigate to="/devices" replace />} />
                </Route>
              </Routes>
            </BrowserRouter>
          </AuthGate>
        </AuthProvider>
        <Toaster />
      </QueryClientProvider>
    </ThemeProvider>
  )
}
