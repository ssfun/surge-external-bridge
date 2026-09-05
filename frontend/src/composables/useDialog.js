import { nextTick, onBeforeUnmount, watch } from 'vue'

// Shared keyboard behavior for the two editor dialogs.
export function useDialog(isOpen, dialog, close) {
  let returnFocus = null
  const focusable = () => [...(dialog.value?.querySelectorAll('button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),summary,a[href],[tabindex]:not([tabindex="-1"])') || [])].filter((element) => {
    if (element.closest('[hidden]') || getComputedStyle(element).display === 'none') return false
    for (let parent = element.parentElement; parent && parent !== dialog.value; parent = parent.parentElement) {
      if (parent.tagName === 'DETAILS' && !parent.open && !parent.querySelector('summary')?.contains(element)) return false
    }
    return true
  })
  function keydown(event) {
    if (!isOpen()) return
    if (event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      close()
    } else if (event.key === 'Tab') {
      const items = focusable()
      const index = items.indexOf(document.activeElement)
      if (index < 0 || (event.shiftKey ? index === 0 : index === items.length - 1)) {
        event.preventDefault()
        ;(event.shiftKey ? items.at(-1) : items[0])?.focus()
      }
    }
  }
  watch(isOpen, async (open) => {
    if (open) {
      returnFocus = document.activeElement
      document.addEventListener('keydown', keydown, true)
      await nextTick()
      if (isOpen()) (dialog.value?.querySelector('input') || focusable()[0])?.focus({ preventScroll: true })
    } else {
      document.removeEventListener('keydown', keydown, true)
      await nextTick()
      if (!isOpen() && returnFocus?.isConnected) returnFocus.focus({ preventScroll: true })
    }
  }, { immediate: true })
  onBeforeUnmount(() => document.removeEventListener('keydown', keydown, true))
}
