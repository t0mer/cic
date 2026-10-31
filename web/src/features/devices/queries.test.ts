import { describe, expect, it } from 'vitest'

import { DEVICES_REFETCH_MS, devicesQueryOptions } from './queries'

describe('devicesQueryOptions', () => {
  it('re-reads the list on an interval, only while visible', () => {
    expect(devicesQueryOptions.refetchInterval).toBe(DEVICES_REFETCH_MS)
    expect(devicesQueryOptions.refetchIntervalInBackground).toBe(false)
  })
  it('keeps staleTime below the interval so the poll is not doubled', () => {
    expect(devicesQueryOptions.staleTime).toBeGreaterThan(0)
    expect(devicesQueryOptions.staleTime).toBeLessThanOrEqual(DEVICES_REFETCH_MS)
  })
})
