import * as React from 'react'

import { cn } from '@/lib/utils'

const base =
  'block w-full rounded-md border border-input bg-background px-3 text-base text-foreground shadow-xs outline-none placeholder:text-muted-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:border-ring aria-invalid:border-destructive disabled:opacity-50'

export function Input({ className, ...props }: React.ComponentProps<'input'>) {
  return <input className={cn(base, 'h-11', className)} {...props} />
}

export function Textarea({ className, ...props }: React.ComponentProps<'textarea'>) {
  return <textarea className={cn(base, 'min-h-28 py-2', className)} {...props} />
}

export function Label({ className, ...props }: React.ComponentProps<'label'>) {
  return <label className={cn('text-sm font-medium', className)} {...props} />
}

export function FieldError({ id, children }: { id: string; children: React.ReactNode }) {
  return (
    <p id={id} role="alert" className="text-sm text-destructive">
      {children}
    </p>
  )
}
