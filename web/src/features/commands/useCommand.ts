import { useMutation } from '@tanstack/react-query'
import { useCallback, useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'

import { sendCommand, type CommandAction } from '@/lib/api'
import { commandErrorMessage, isOutcomeUnknown } from '@/lib/errors'
import type { CommandAck, Device } from '@/lib/types'
import { registerIssued } from './results'

interface CommandOptions {
  /**
   * Runs when the 202 ack arrives. It is a mutation-level callback, so it still fires if the
   * component that started the request has unmounted (dialog closed, card gone).
   */
  onAck?: (ack: CommandAck) => void
  /** Runs when the outcome is unknown: the command MAY still be delivered. */
  onUnknown?: () => void
  /** Runs after success or failure (mutation-level, so it also survives an unmount). */
  onSettled?: () => void
}

/** One mutation per button, so each button disables only while its own request is in flight. */
export function useCommand(device: Device, action: CommandAction, opts: CommandOptions = {}) {
  return useMutation({
    mutationFn: async (body?: unknown) => {
      const ack = await sendCommand(device, action, body)
      registerIssued(String(ack.id))
      return ack
    },
    onSuccess: (ack) => opts.onAck?.(ack),
    onSettled: () => opts.onSettled?.(),
    onError: (err) => {
      const msg = commandErrorMessage(err)
      if (isOutcomeUnknown(err)) {
        toast.warning(msg, { duration: 10_000 })
        opts.onUnknown?.()
      } else toast.error(msg)
    },
  })
}

/** A boolean that turns itself off after `ms`. */
export function useFlag(ms: number) {
  const [on, setOn] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const clear = useCallback(() => {
    clearTimeout(timer.current)
    setOn(false)
  }, [])
  const trigger = useCallback(() => {
    clearTimeout(timer.current)
    setOn(true)
    timer.current = setTimeout(() => setOn(false), ms)
  }, [ms])
  useEffect(() => () => clearTimeout(timer.current), [])
  return [on, trigger, clear] as const
}
