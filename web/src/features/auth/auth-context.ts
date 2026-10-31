import { createContext, useContext } from 'react'

import type { Me } from '@/lib/types'

export type AuthStatus = 'loading' | 'authenticated' | 'anonymous' | 'error'

export interface AuthValue {
  status: AuthStatus
  user: Me | null
  isAdmin: boolean
  mustChangePassword: boolean
  error: Error | null
  retry: () => void
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
  changePassword: (current: string, next: string) => Promise<void>
}

const reject = () => Promise.reject(new Error('AuthProvider missing'))

/** Default (no provider): anonymous, so no admin-only UI can ever render by accident. */
export const AuthContext = createContext<AuthValue>({
  status: 'anonymous',
  user: null,
  isAdmin: false,
  mustChangePassword: false,
  error: null,
  retry: () => {},
  login: reject,
  logout: reject,
  changePassword: reject,
})

export const useAuth = () => useContext(AuthContext)
