import { useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'

import {
  allDevicesKey,
  ApiError,
  createPlace,
  deleteDevice,
  deletePlace,
  devicesKey,
  fetchAllDevices,
  fetchGeocodeUsage,
  fetchPlaces,
  fetchUsers,
  geocodeUsageKey,
  patchDevice,
  patchPlace,
  placesKey,
  patchUser,
  usersKey,
} from '@/lib/api'
import { applyDeviceRemoved, applyDeviceUpdate } from '@/lib/device-cache'
import { commandErrorMessage } from '@/lib/errors'
import type { AdminUser, Device, DevicePatch, Place, PlaceInput } from '@/lib/types'

const noRetryOnHttp = (count: number, err: Error) => !(err instanceof ApiError) && count < 2

/** Every device including hidden ones. Separate key from the Cards list. */
export const useAllDevices = () =>
  useQuery({
    queryKey: allDevicesKey,
    queryFn: fetchAllDevices,
    staleTime: 10_000,
    refetchInterval: 15_000,
    refetchIntervalInBackground: false,
    retry: noRetryOnHttp,
  })

export const useUsers = () =>
  useQuery({ queryKey: usersKey, queryFn: fetchUsers, staleTime: 10_000, retry: noRetryOnHttp })

/** A 404 means geocoding has not shipped on this server: callers show "not available". */
export const useGeocodeUsage = (enabled: boolean) =>
  useQuery({
    queryKey: geocodeUsageKey,
    queryFn: fetchGeocodeUsage,
    enabled,
    staleTime: 30_000,
    retry: false,
  })

const nameOf = (d: Pick<Device, 'full_name' | 'device'>) => d.full_name || d.device

/** A device that no longer exists: drop it from both lists and reload them. */
const isGone = (err: unknown) =>
  err instanceof ApiError && (err.code === 'unknown_device' || err.code === 'not_found')

function dropGone(qc: QueryClient, key: string) {
  applyDeviceRemoved(qc, key)
  void qc.invalidateQueries({ queryKey: devicesKey })
}

export function useUpdateDevice(device: Device, onDone?: () => void) {
  const qc = useQueryClient()
  return useMutation({
    retry: 0,
    mutationFn: (patch: DevicePatch) => patchDevice(device, patch),
    onSuccess: async (d) => {
      // `devicesKey` is a prefix of both lists: an in-flight refetch must not overwrite this.
      await qc.cancelQueries({ queryKey: devicesKey })
      applyDeviceUpdate(qc, d)
      toast.success(`Saved ${nameOf(d)}`)
      onDone?.()
    },
    onError: (err) => {
      if (isGone(err)) dropGone(qc, device.key)
    },
  })
}

export function useDeleteDevice(device: Device, onDone?: () => void) {
  const qc = useQueryClient()
  return useMutation({
    retry: 0,
    mutationFn: () => deleteDevice(device),
    onSuccess: async () => {
      await qc.cancelQueries({ queryKey: devicesKey })
      applyDeviceRemoved(qc, device.key)
      toast.success(`Deleted ${nameOf(device)}`)
      onDone?.()
    },
    onError: (err) => {
      if (isGone(err)) dropGone(qc, device.key)
    },
  })
}

interface UserPatchVars {
  id: AdminUser['id']
  patch: { role?: string; disabled?: boolean }
}

export function useUpdateUser() {
  const qc = useQueryClient()
  return useMutation({
    retry: 0,
    mutationFn: ({ id, patch }: UserPatchVars) => patchUser(id, patch),
    onSuccess: (u) => {
      qc.setQueryData<AdminUser[]>(usersKey, (old) => old?.map((x) => (x.id === u.id ? u : x)))
      void qc.invalidateQueries({ queryKey: usersKey })
    },
    onError: (err) => {
      toast.error(commandErrorMessage(err))
      if (err instanceof ApiError && err.code === 'not_found')
        void qc.invalidateQueries({ queryKey: usersKey })
    },
  })
}

/**
 * Runs an async action with a pending flag and a double-submit guard, WITHOUT a query/mutation
 * cache: results that carry one-time secrets pass through the caller's callback only.
 */
export function useAction<A extends unknown[], R>(
  fn: (...args: A) => Promise<R>,
  /** Called when the target no longer exists (`not_found`), so the caller can refresh its list. */
  onNotFound?: () => void,
) {
  const notFound = useRef(onNotFound)
  useEffect(() => {
    notFound.current = onNotFound
  })
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const busy = useRef(false)
  const run = useCallback(
    async (...args: A): Promise<R | undefined> => {
      if (busy.current) return undefined
      busy.current = true
      setPending(true)
      setError(null)
      try {
        return await fn(...args)
      } catch (e) {
        setError(commandErrorMessage(e))
        if (e instanceof ApiError && e.code === 'not_found') notFound.current?.()
        return undefined
      } finally {
        busy.current = false
        setPending(false)
      }
    },
    [fn],
  )
  const reset = useCallback(() => setError(null), [])
  return { run, pending, error, reset }
}

/**
 * Synchronous double-submit guard: `isPending` only flips after a re-render, so two submits in the
 * same tick could both start. `once` runs `start` unless a previous one has not called `done`.
 */
export function useOnce() {
  const busy = useRef(false)
  return useCallback((start: (done: () => void) => void) => {
    if (busy.current) return
    busy.current = true
    start(() => {
      busy.current = false
    })
  }, [])
}

export const usePlaces = () =>
  useQuery({ queryKey: placesKey, queryFn: fetchPlaces, staleTime: 10_000, retry: noRetryOnHttp })

/** Places and the devices' `safe_place` both change when a place is added, moved or removed. */
function refreshPlaces(qc: QueryClient) {
  void qc.invalidateQueries({ queryKey: placesKey })
  void qc.invalidateQueries({ queryKey: devicesKey })
}

/** Creates a place, or patches only `patch` when `place` is given. */
export function useSavePlace(place: Place | null, onDone?: () => void) {
  const qc = useQueryClient()
  return useMutation({
    retry: 0,
    mutationFn: (v: PlaceInput | Partial<PlaceInput>) =>
      place ? patchPlace(place.id, v) : createPlace(v as PlaceInput),
    onSuccess: (p) => {
      refreshPlaces(qc)
      toast.success(`Saved ${p.name}`)
      onDone?.()
    },
    onError: (err) => {
      if (err instanceof ApiError && err.code === 'not_found') refreshPlaces(qc)
    },
  })
}

export function useDeletePlace(place: Place, onDone?: () => void) {
  const qc = useQueryClient()
  return useMutation({
    retry: 0,
    mutationFn: () => deletePlace(place.id),
    onSuccess: () => {
      refreshPlaces(qc)
      toast.success(`Deleted ${place.name}`)
      onDone?.()
    },
    onError: (err) => {
      if (err instanceof ApiError && err.code === 'not_found') refreshPlaces(qc)
    },
  })
}
