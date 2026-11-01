import { Tabs } from 'radix-ui'
import { useSearchParams } from 'react-router-dom'

import { useAuth } from '@/features/auth/auth-context'
import { cn } from '@/lib/utils'
import { AccountSection } from './AccountSection'
import { DevicesSection } from './DevicesSection'
import { GeocodingSection } from './GeocodingSection'
import { PlacesSection } from './PlacesSection'
import { SystemSection } from './SystemSection'
import { UsersSection } from './UsersSection'

export const TABS = [
  { id: 'devices', label: 'Devices', admin: true, Panel: DevicesSection },
  { id: 'users', label: 'Users', admin: true, Panel: UsersSection },
  { id: 'places', label: 'Safe places', admin: true, Panel: PlacesSection },
  { id: 'geocoding', label: 'Geocoding', admin: true, Panel: GeocodingSection },
  { id: 'system', label: 'System', admin: true, Panel: SystemSection },
  { id: 'account', label: 'Account', admin: false, Panel: AccountSection },
] as const

/** Tab ids a user may see. Viewers get Account only; admin tabs are not even mounted for them. */
export const visibleTabs = (isAdmin: boolean) => TABS.filter((t) => isAdmin || !t.admin)

export function SettingsPage() {
  const { isAdmin } = useAuth()
  const [params, setParams] = useSearchParams()
  const tabs = visibleTabs(isAdmin)
  const wanted = params.get('tab')
  const active = tabs.find((t) => t.id === wanted) ?? tabs[0]

  return (
    <div className="flex flex-col gap-4">
      <h1 className="text-xl font-semibold">Settings</h1>
      <Tabs.Root
        value={active.id}
        onValueChange={(v) =>
          setParams(
            (prev) => {
              const next = new URLSearchParams(prev)
              next.set('tab', v)
              return next
            },
            { replace: true },
          )
        }
        className="flex flex-col gap-4"
      >
        <Tabs.List
          aria-label="Settings sections"
          className="-mx-4 flex gap-1 overflow-x-auto border-b px-4 md:mx-0 md:px-0"
        >
          {tabs.map((t) => (
            <Tabs.Trigger
              key={t.id}
              value={t.id}
              className={cn(
                'inline-flex h-11 shrink-0 items-center border-b-2 border-transparent px-4 text-sm font-medium text-muted-foreground hover:text-foreground',
                'data-[state=active]:border-primary data-[state=active]:text-foreground',
                'focus-visible:ring-[3px] focus-visible:ring-ring focus-visible:outline-none',
              )}
            >
              {t.label}
            </Tabs.Trigger>
          ))}
        </Tabs.List>
        {tabs.map(({ id, Panel }) => (
          <Tabs.Content key={id} value={id} className="outline-none">
            <Panel />
          </Tabs.Content>
        ))}
      </Tabs.Root>
    </div>
  )
}
