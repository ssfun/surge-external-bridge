import { afterEach, describe, expect, it, vi } from 'vitest'
import { copyText } from '@/utils.js'

function setSecureContext(value) {
  Object.defineProperty(window, 'isSecureContext', { configurable: true, value })
}

describe('copyText', () => {
  afterEach(() => {
    delete window.isSecureContext
  })

  it('uses the Clipboard API in a secure context', async () => {
    setSecureContext(true)

    await copyText('secure value')

    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('secure value')
  })

  it('falls back to execCommand on a LAN HTTP origin', async () => {
    setSecureContext(false)
    const execCommand = vi.fn(() => true)
    Object.defineProperty(document, 'execCommand', { configurable: true, value: execCommand })
    const input = document.createElement('input')
    document.body.appendChild(input)
    input.focus()

    await copyText('gateway value')

    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
    expect(execCommand).toHaveBeenCalledWith('copy')
    expect(document.querySelector('textarea')).toBeNull()
    expect(document.activeElement).toBe(input)
  })

  it('falls back when the Clipboard API rejects the write', async () => {
    setSecureContext(true)
    navigator.clipboard.writeText.mockRejectedValueOnce(new Error('denied'))
    const execCommand = vi.fn(() => true)
    Object.defineProperty(document, 'execCommand', { configurable: true, value: execCommand })

    await copyText('fallback value')

    expect(execCommand).toHaveBeenCalledWith('copy')
  })
})
