import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'

import { brokerKey, devicesKey } from '@/lib/api'
import type { Broker, Device } from '@/lib/types'

export type StreamStatus = 'connecting' | 'live' | 'reconnecting'

const MAX_BACKOFF_MS = 15_000

function parse<T>(e: MessageEvent): T | null {
  try {
    return JSON.parse(String(e.data)) as T
  } catch {
    return null
  }
}

export function useEventStream(): StreamStatus {
  const qc = useQueryClient()
  const [status, setStatus] = useState<StreamStatus>('connecting')

  useEffect(() => {
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
        qc.setQueryData<Device[]>(devicesKey, (old) => {
          if (d.hidden) return old?.filter((x) => x.key !== d.key)
          if (!old) return [d]
          const i = old.findIndex((x) => x.key === d.key)
          if (i < 0) return [...old, d]
          const next = old.slice()
          next[i] = d
          return next
        })
      })
      es.addEventListener('device.removed', (e) => {
        const r = parse<{ key: string }>(e as MessageEvent)
        if (!r) return
        qc.setQueryData<Device[]>(devicesKey, (old) => old?.filter((x) => x.key !== r.key))
      })
    }

    connect()
    return () => {
      stopped = true
      clearTimeout(retry)
      es?.close()
    }
  }, [qc])

  return status
}
