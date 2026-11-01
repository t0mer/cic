export const LEAVE_CODES_MESSAGE = 'Leave without saving your backup codes?'

// Number of backup-code screens that are still unacknowledged (they are shown only once).
let unsaved = 0

/** Marks codes as unsaved; the returned function clears the mark. */
export function markUnsavedCodes(): () => void {
  unsaved++
  let done = false
  return () => {
    if (!done) {
      done = true
      unsaved--
    }
  }
}

export const hasUnsavedCodes = () => unsaved > 0

/** True when it is fine to leave: nothing unsaved, or the user confirmed discarding the codes. */
export const confirmLeave = () => !hasUnsavedCodes() || window.confirm(LEAVE_CODES_MESSAGE)
