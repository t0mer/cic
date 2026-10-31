import type { ReactNode } from 'react'

export function AuthShell({ title, children }: { title: string; children: ReactNode }) {
  return (
    <main className="flex min-h-dvh items-center justify-center bg-background p-4">
      <div className="w-full max-w-sm rounded-xl border bg-card p-6 text-card-foreground shadow-sm">
        <div className="mb-6 flex items-center gap-2">
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
          <span className="font-semibold">CIC</span>
        </div>
        <h1 className="mb-4 text-xl font-semibold">{title}</h1>
        {children}
      </div>
    </main>
  )
}
