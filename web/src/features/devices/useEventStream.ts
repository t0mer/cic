import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'

import { isIssued, publishResult, toastFailure } from '@/features/commands/results'
import { authKey, brokerKey, devicesKey } from '@/lib/api'
import { applyDeviceRemoved, applyDeviceUpdate } from '@/lib/device-cache'
import type { Broker, CommandResult, Device } from '@/lib/types'

export type StreamStatus = 'connecting' | 'live' | 'reconnecting'

const MAX_BACKOFF_MS = 15_000

function parse<T>(e: MessageEvent): T | null {
  try {
    return JSON.parse(String(e.data)) as T
  } catch {
    return null
  }
}

/** Connects only while `enabled` (authenticated); closes on logout and never retries into a dead session. */
export function useEventStream(enabled: boolean, isAdmin = false): StreamStatus {
  const qc = useQueryClient()
  const [status, setStatus] = useState<StreamStatus>('connecting')

  // The handler reads this through a ref so a role change never tears the stream down.
  const adminRef = useRef(isAdmin)
  useEffect(() => {
    adminRef.current = isAdmin
  }, [isAdmin])

  useEffect(() => {
    if (!enabled) return
    let es: EventSource | null = null
    let retry: ReturnType<typeof setTimeout> | undefined
    let attempt = 0
    let stopped = false
    let opened = false

    const connect = () => {
      es = new EventSource('/api/events')
      es.onopen = () => {
        attempt = 0
        setStatus('live')
        // The initial fetch covers the first open; only a re-open after a loss needs a resync.
        if (opened) void qc.invalidateQueries({ queryKey: devicesKey })
        opened = true
      }
      es.onerror = () => {
        setStatus('reconnecting')
        // Browser retries on its own unless the connection was closed for good.
        if (es && es.readyState === EventSource.CLOSED && !stopped) {
          es.close()
          // A closed stream is usually a 401: re-check the session, which disables this hook if it is gone.
          void qc.invalidateQueries({ queryKey: authKey })
          const delay = Math.min(MAX_BACKOFF_MS, 1000 * 2 ** attempt++)
          retry = setTimeout(connect, delay)
        }
      }
      const setBroker = (e: Event) => {
        const b = parse<Broker>(e as MessageEvent)
        if (b) qc.setQueryData<Broker>(brokerKey, b)
      }
      es.addEventListener('hello', setBroker)
      es.addEventListener('broker.state', setBroker)
      es.addEventListener('device.updated', (e) => {
        const d = parse<Device>(e as MessageEvent)
        if (!d) return
        applyDeviceUpdate(qc, d)
      })
      es.addEventListener('command.result', (e) => {
        const r = parse<CommandResult>(e as MessageEvent)
        if (!r || r.id === undefined || r.id === null) return
        publishResult({ ...r, id: String(r.id) })
        // Only admins who issued this very command hear about its failure.
        if (adminRef.current && isIssued(String(r.id))) toastFailure(r)
      })
      es.addEventListener('device.removed', (e) => {
        const r = parse<{ key: string }>(e as MessageEvent)
        if (!r) return
        applyDeviceRemoved(qc, r.key)
      })
    }

    connect()
    return () => {
      stopped = true
      setStatus('connecting')
      clearTimeout(retry)
      es?.close()
    }
  }, [qc, enabled])

  return enabled ? status : 'connecting'
}
