import type { Broker, Device } from './types'

export class ApiError extends Error {
  readonly status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    let msg = `Request failed (${res.status})`
    try {
      const body: unknown = await res.json()
      if (body && typeof body === 'object' && 'error' in body && typeof body.error === 'string') {
        msg = body.error
      }
    } catch {
      /* non-JSON error body */
    }
    throw new ApiError(res.status, msg)
  }
  return (await res.json()) as T
}

export const fetchDevices = () => getJSON<Device[]>('/api/devices')
export const fetchBroker = () => getJSON<Broker>('/api/broker')

export const devicesKey = ['devices'] as const
export const brokerKey = ['broker'] as const
