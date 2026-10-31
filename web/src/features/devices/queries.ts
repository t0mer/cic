import { useQuery } from '@tanstack/react-query'

import { brokerKey, devicesKey, fetchBroker, fetchDevices } from '@/lib/api'

// The backend computes `online` at read time and sends no timeout event, so a
// silent phone only flips to Offline when the list is re-read.
export const DEVICES_REFETCH_MS = 15_000

export const devicesQueryOptions = {
  queryKey: devicesKey,
  queryFn: fetchDevices,
  staleTime: 10_000,
  refetchInterval: DEVICES_REFETCH_MS,
  refetchIntervalInBackground: false,
  retry: (count: number, err: Error) => !(err as { status?: number }).status && count < 2,
}

export const useDevices = () => useQuery(devicesQueryOptions)

export const useBroker = () => useQuery({ queryKey: brokerKey, queryFn: fetchBroker, retry: 1 })
