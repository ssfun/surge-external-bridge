import { afterEach, beforeEach, vi } from 'vitest'

const values = new Map()
const storage = {
  getItem: (key) => values.has(key) ? values.get(key) : null,
  setItem: (key, value) => values.set(key, String(value)),
  removeItem: (key) => values.delete(key),
  clear: () => values.clear(),
}

beforeEach(() => {
  storage.clear()
  vi.stubGlobal('localStorage', storage)
  vi.stubGlobal('confirm', vi.fn(() => true))
  vi.stubGlobal('prompt', vi.fn(() => null))
  window.scrollTo = vi.fn()
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: vi.fn(async () => {}) } })
})

afterEach(() => {
  document.body.innerHTML = ''
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})
