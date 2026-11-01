import { Component, type ErrorInfo, type ReactNode } from 'react'

import { Button } from '@/components/ui/button'

const RELOAD_FLAG = 'cic:chunk-reload'

/** A lazy chunk that failed to load (stale tab after a deploy, flaky network). */
export function isChunkLoadError(error: unknown): boolean {
  const msg = error instanceof Error ? `${error.name} ${error.message}` : String(error)
  return /ChunkLoadError|Loading chunk|dynamically imported module|Importing a module script failed/i.test(
    msg,
  )
}

/**
 * Allows exactly one automatic reload per browser session. Returns false when it already
 * happened, or when the flag cannot be stored (then a reload loop could not be prevented).
 */
export function claimOneShotReload(storage?: Pick<Storage, 'getItem' | 'setItem'>): boolean {
  try {
    const s = storage ?? window.sessionStorage
    if (s.getItem(RELOAD_FLAG)) return false
    s.setItem(RELOAD_FLAG, '1')
    return true
  } catch {
    return false
  }
}

interface Props {
  children: ReactNode
  /** Injectable for tests. */
  reload?: () => void
}

/** Catches render/lazy-load failures below it so the rest of the app (shell, nav) stays usable. */
export class ErrorBoundary extends Component<Props, { error: Error | null }> {
  state: { error: Error | null } = { error: null }

  static getDerivedStateFromError(error: unknown) {
    return { error: error instanceof Error ? error : new Error(String(error)) }
  }

  private reload = () => (this.props.reload ?? (() => window.location.reload()))()

  componentDidCatch(error: unknown, info: ErrorInfo) {
    console.error('Page failed to render', error, info.componentStack)
    if (isChunkLoadError(error) && claimOneShotReload()) this.reload()
  }

  render() {
    if (!this.state.error) return this.props.children
    return (
      <div
        role="alert"
        className="mx-auto flex max-w-sm flex-col items-center gap-3 rounded-xl border p-6 text-center"
      >
        <h2 className="text-lg font-semibold">This page could not load</h2>
        <p className="text-sm text-muted-foreground">
          Part of the app failed to load, possibly because a new version was released or the
          connection dropped. Reloading usually fixes it.
        </p>
        <Button className="h-11 px-6" onClick={this.reload}>
          Reload
        </Button>
      </div>
    )
  }
}
