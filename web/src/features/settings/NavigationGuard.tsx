import { useContext, useEffect } from 'react'
import { UNSAFE_DataRouterContext, useBlocker } from 'react-router-dom'

import { LEAVE_CODES_MESSAGE } from '@/lib/leave-guard'

function Blocker() {
  const blocker = useBlocker(true)
  useEffect(() => {
    if (blocker.state !== 'blocked') return
    if (window.confirm(LEAVE_CODES_MESSAGE)) blocker.proceed()
    else blocker.reset()
  }, [blocker])
  return null
}

/**
 * Blocks in-app route changes, tab (search param) changes and history back/forward behind a
 * confirmation. useBlocker needs a data router; without one (isolated component tests) it is inert.
 */
export function NavigationGuard() {
  return useContext(UNSAFE_DataRouterContext) ? <Blocker /> : null
}
