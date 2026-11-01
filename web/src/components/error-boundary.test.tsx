// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { claimOneShotReload, ErrorBoundary, isChunkLoadError } from './error-boundary'

function Boom({ message }: { message: string }): never {
  throw new Error(message)
}

beforeEach(() => {
  sessionStorage.clear()
  vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('isChunkLoadError', () => {
  it('recognises the browsers chunk-load messages', () => {
    expect(isChunkLoadError(new Error('Failed to fetch dynamically imported module: /a.js'))).toBe(
      true,
    )
    expect(isChunkLoadError(new Error('error loading dynamically imported module'))).toBe(true)
    expect(isChunkLoadError(new Error('Importing a module script failed.'))).toBe(true)
    expect(isChunkLoadError(new Error('x is undefined'))).toBe(false)
  })
})

describe('claimOneShotReload', () => {
  it('allows the first reload only', () => {
    expect(claimOneShotReload()).toBe(true)
    expect(claimOneShotReload()).toBe(false)
  })
  it('refuses when storage throws (cannot guard against a loop)', () => {
    const broken = {
      getItem: () => {
        throw new Error('denied')
      },
      setItem: () => {},
    }
    expect(claimOneShotReload(broken)).toBe(false)
  })
})

describe('ErrorBoundary', () => {
  it('renders children when nothing fails', () => {
    render(
      <ErrorBoundary>
        <p>fine</p>
      </ErrorBoundary>,
    )
    expect(screen.getByText('fine')).toBeTruthy()
  })

  it('shows a message and a Reload button for an ordinary error, without auto-reloading', () => {
    const reload = vi.fn()
    render(
      <ErrorBoundary reload={reload}>
        <Boom message="x is undefined" />
      </ErrorBoundary>,
    )
    expect(screen.getByRole('alert').textContent).toContain('could not load')
    expect(reload).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Reload' }))
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('reloads once automatically on a chunk-load error, then falls back to the message', () => {
    const reload = vi.fn()
    const view = render(
      <ErrorBoundary reload={reload}>
        <Boom message="Failed to fetch dynamically imported module" />
      </ErrorBoundary>,
    )
    expect(reload).toHaveBeenCalledTimes(1)
    view.unmount()
    render(
      <ErrorBoundary reload={reload}>
        <Boom message="Failed to fetch dynamically imported module" />
      </ErrorBoundary>,
    )
    expect(reload).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('button', { name: 'Reload' })).toBeTruthy()
  })
})
