import { LogOut, UserRound } from 'lucide-react'
import { DropdownMenu } from 'radix-ui'

import { useAuth } from './auth-context'

export function UserMenu() {
  const { user, logout } = useAuth()
  if (!user) return null
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger
        aria-label={`User menu: ${user.username} (${user.role})`}
        data-testid="user-menu"
        className="inline-flex size-11 items-center justify-center rounded-md hover:bg-accent focus-visible:ring-[3px] focus-visible:ring-ring focus-visible:outline-none"
      >
        <UserRound className="size-5" aria-hidden="true" />
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          align="end"
          sideOffset={4}
          className="z-50 min-w-48 rounded-md border bg-popover p-1 text-popover-foreground shadow-md"
        >
          <div className="px-3 py-2">
            <div dir="auto" className="truncate text-sm font-medium">
              {user.username}
            </div>
            <div className="text-xs text-muted-foreground capitalize">{user.role}</div>
          </div>
          <DropdownMenu.Separator className="my-1 h-px bg-border" />
          <DropdownMenu.Item
            onSelect={() => void logout()}
            className="flex h-11 cursor-default items-center gap-2 rounded-sm px-3 text-sm outline-none data-[highlighted]:bg-accent"
          >
            <LogOut className="size-4" aria-hidden="true" />
            Sign out
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  )
}
