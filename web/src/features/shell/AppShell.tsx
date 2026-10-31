import {
  Activity,
  Map as MapIcon,
  Monitor,
  Moon,
  Settings,
  Smartphone,
  Sun,
  UserRound,
} from 'lucide-react'
import { NavLink, Outlet } from 'react-router-dom'

import { useTheme, type Theme } from '@/components/theme-provider'
import { useBroker } from '@/features/devices/queries'
import { useEventStream } from '@/features/devices/useEventStream'
import { cn } from '@/lib/utils'

const NAV = [
  { to: '/map', label: 'Map', icon: MapIcon },
  { to: '/devices', label: 'Devices', icon: Smartphone },
  { to: '/activity', label: 'Activity', icon: Activity },
  { to: '/settings', label: 'Settings', icon: Settings },
] as const

const focus = 'focus-visible:ring-[3px] focus-visible:ring-ring focus-visible:outline-none'

function LogoMark() {
  return (
    <svg
      viewBox="0 0 32 32"
      className="size-8"
      aria-hidden="true"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
    >
      <circle cx="16" cy="16" r="13" />
      <circle cx="16" cy="16" r="7" />
      <path d="M16 16 26 9" strokeLinecap="round" />
      <circle cx="16" cy="16" r="1.5" fill="currentColor" />
    </svg>
  )
}

const THEME_NEXT: Record<Theme, Theme> = { system: 'light', light: 'dark', dark: 'system' }
const THEME_ICON = { system: Monitor, light: Sun, dark: Moon } as const

function ThemeToggle() {
  const { theme, setTheme } = useTheme()
  const Icon = THEME_ICON[theme]
  return (
    <button
      type="button"
      onClick={() => setTheme(THEME_NEXT[theme])}
      aria-label={`Theme: ${theme}. Switch to ${THEME_NEXT[theme]}`}
      title={`Theme: ${theme}`}
      data-testid="theme-toggle"
      className={cn(
        'inline-flex size-11 items-center justify-center rounded-md hover:bg-accent',
        focus,
      )}
    >
      <Icon className="size-5" aria-hidden="true" />
    </button>
  )
}

function BrokerDot() {
  const { data, isPending } = useBroker()
  const state = isPending ? 'unknown' : data?.connected ? 'connected' : 'disconnected'
  const title =
    state === 'unknown'
      ? 'Broker status loading'
      : data?.connected
        ? `Connected to ${data.host}`
        : `Disconnected${data?.last_error ? `: ${data.last_error}` : ''}`
  return (
    <span
      role="img"
      tabIndex={0}
      title={title}
      aria-label={`Broker: ${title}`}
      data-testid="broker-dot"
      data-state={state}
      className={cn('inline-flex size-11 items-center justify-center rounded-md', focus)}
    >
      <span
        className={cn(
          'size-3 rounded-full',
          state === 'connected' && 'bg-green-600 dark:bg-green-400',
          state === 'disconnected' && 'bg-red-600 dark:bg-red-400',
          state === 'unknown' && 'bg-zinc-400',
        )}
      />
    </span>
  )
}

function Notices() {
  const stream = useEventStream()
  const { data } = useBroker()
  return (
    <>
      {data && !data.connected ? (
        <div
          role="alert"
          className="border-b bg-red-100 px-4 py-2 text-sm text-red-950 dark:bg-red-950 dark:text-red-100"
        >
          MQTT broker disconnected{data.last_error ? `: ${data.last_error}` : ''}. Device data may
          be stale.
        </div>
      ) : null}
      {stream === 'reconnecting' ? (
        <div
          role="status"
          className="border-b bg-amber-100 px-4 py-2 text-sm text-amber-950 dark:bg-amber-950 dark:text-amber-100"
        >
          Live updates reconnecting…
        </div>
      ) : null}
    </>
  )
}

export function AppShell() {
  return (
    <div className="min-h-dvh">
      <header className="sticky top-0 z-20 flex h-14 items-center gap-2 border-b bg-background px-3 md:ps-60">
        <div className="flex items-center gap-2 md:hidden">
          <LogoMark />
          <span className="font-semibold">CIC</span>
        </div>
        <div className="ms-auto flex items-center">
          <BrokerDot />
          <ThemeToggle />
          <button
            type="button"
            disabled
            title="Sign-in arrives with the auth step"
            aria-label="User menu (sign-in arrives with the auth step)"
            className="inline-flex size-11 items-center justify-center rounded-md text-muted-foreground disabled:opacity-60"
          >
            <UserRound className="size-5" aria-hidden="true" />
          </button>
        </div>
      </header>

      <nav
        aria-label="Primary"
        data-testid="sidebar"
        className="fixed inset-y-0 start-0 z-30 hidden w-56 flex-col gap-1 border-e bg-background p-3 md:flex"
      >
        <div className="mb-4 flex h-8 items-center gap-2 px-2">
          <LogoMark />
          <span className="font-semibold">CIC</span>
        </div>
        {NAV.map(({ to, label, icon: Icon }) => (
          <NavLink
            key={to}
            to={to}
            className={({ isActive }) =>
              cn(
                'flex h-11 items-center gap-3 rounded-md px-3 text-sm font-medium hover:bg-accent',
                isActive && 'bg-accent text-accent-foreground',
                focus,
              )
            }
          >
            <Icon className="size-5" aria-hidden="true" />
            {label}
          </NavLink>
        ))}
      </nav>

      <nav
        aria-label="Primary"
        data-testid="tabbar"
        className="fixed inset-x-0 bottom-0 z-30 grid grid-cols-4 border-t bg-background pb-[env(safe-area-inset-bottom)] md:hidden"
      >
        {NAV.map(({ to, label, icon: Icon }) => (
          <NavLink
            key={to}
            to={to}
            className={({ isActive }) =>
              cn(
                'flex h-14 flex-col items-center justify-center gap-0.5 text-xs font-medium',
                isActive ? 'text-foreground' : 'text-muted-foreground',
                focus,
              )
            }
          >
            <Icon className="size-5" aria-hidden="true" />
            {label}
          </NavLink>
        ))}
      </nav>

      <div className="md:ps-56">
        <Notices />
        <main className="mx-auto max-w-[1600px] p-4 pb-24 md:p-6 md:pb-6">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
