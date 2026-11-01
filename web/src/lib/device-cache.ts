import type { QueryClient } from '@tanstack/react-query'

import { allDevicesKey, devicesKey } from './api'
import type { Device } from './types'

const upsert = (list: Device[], d: Device): Device[] => {
  const i = list.findIndex((x) => x.key === d.key)
  if (i < 0) return [...list, d]
  const next = list.slice()
  next[i] = d
  return next
}

/** A list that was never loaded must not be seeded with a partial view: reload it fully instead. */
function refreshAbsent(qc: QueryClient) {
  for (const queryKey of [devicesKey, allDevicesKey])
    if (qc.getQueryData(queryKey) === undefined)
      void qc.invalidateQueries({ queryKey, exact: true })
}

/**
 * Applies one device snapshot to both lists. The Cards/Map list never holds hidden devices;
 * the admin list (Settings) keeps them. A list that was never loaded is left alone (and
 * invalidated so it loads fully on next mount).
 */
export function applyDeviceUpdate(qc: QueryClient, d: Device) {
  qc.setQueryData<Device[]>(devicesKey, (old) => {
    if (!old) return old
    return d.hidden ? old.filter((x) => x.key !== d.key) : upsert(old, d)
  })
  qc.setQueryData<Device[]>(allDevicesKey, (old) => (old ? upsert(old, d) : old))
  refreshAbsent(qc)
}

export function applyDeviceRemoved(qc: QueryClient, key: string) {
  qc.setQueryData<Device[]>(devicesKey, (old) => old?.filter((x) => x.key !== key))
  qc.setQueryData<Device[]>(allDevicesKey, (old) => old?.filter((x) => x.key !== key))
  refreshAbsent(qc)
}
