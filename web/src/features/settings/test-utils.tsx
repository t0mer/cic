import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import type { ReactNode } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { vi } from 'vitest'

import { AuthContext, type AuthValue } from '@/features/auth/auth-context'
import type { Role } from '@/lib/types'
import { RevealProvider } from './reveal'

export function auth(role: Role | null, username = 'tomer'): AuthValue {
  return {
    status: role ? 'authenticated' : 'anonymous',
    user: role ? { username, role, must_change_password: false, csrf_token: 't' } : null,
    isAdmin: role === 'admin',
    mustChangePassword: false,
    error: null,
    retry: () => {},
    login: () => Promise.resolve(),
    logout: () => Promise.resolve(),
    changePassword: () => Promise.resolve(),
  }
}

export interface Call {
  method: string
  url: string
  body: unknown
}

type Handler = (
  call: Call,
) =>
  | { status?: number; json?: unknown; headers?: Record<string, string> }
  | Promise<{ status?: number; json?: unknown; headers?: Record<string, string> }>

/** Stubs fetch with a handler. Returns the recorded calls. */
export function stubApi(handler: Handler): Call[] {
  const calls: Call[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const call: Call = {
        method: init?.method ?? 'GET',
        url,
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      }
      calls.push(call)
      const r = await handler(call)
      const status = r.status ?? 200
      return new Response(status === 204 ? null : JSON.stringify(r.json ?? {}), {
        status,
        headers: { 'content-type': 'application/json', ...r.headers },
      })
    }),
  )
  return calls
}

export function renderWith(ui: ReactNode, role: Role | null = 'admin', qc = new QueryClient()) {
  const view = render(
    <QueryClientProvider client={qc}>
      <AuthContext.Provider value={auth(role)}>
        <MemoryRouter>
          <RevealProvider>{ui}</RevealProvider>
        </MemoryRouter>
      </AuthContext.Provider>
    </QueryClientProvider>,
  )
  return { qc, ...view }
}
