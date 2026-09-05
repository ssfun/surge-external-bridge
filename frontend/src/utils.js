export function formatBytes(value) {
  const number = Number(value) || 0
  if (number >= 1073741824) return `${(number / 1073741824).toFixed(2)} GiB`
  if (number >= 1048576) return `${(number / 1048576).toFixed(1)} MiB`
  if (number >= 1024) return `${(number / 1024).toFixed(1)} KiB`
  return `${number} B`
}

export function formatRate(value) {
  return `${formatBytes(value)}/s`
}

export function formatDuration(value) {
  const seconds = Number(value) || 0
  if (seconds >= 86400 && seconds % 86400 === 0) return `${seconds / 86400} 天`
  if (seconds >= 3600 && seconds % 3600 === 0) return `${seconds / 3600} 小时`
  if (seconds >= 60 && seconds % 60 === 0) return `${seconds / 60} 分钟`
  return `${seconds} 秒`
}

export function formatDateTime(value) {
  if (!value || value === '—') return '—'
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? String(value) : date.toLocaleString()
}

export function providerTypeLabel(type) {
  return ({ http: 'HTTP 订阅', file: '私有文件', inline: '粘贴配置' })[type] || type
}

export function latestHealth(history) {
  const record = Array.isArray(history) ? history.at(-1) : null
  if (!record) return { label: '尚未测速', time: '' }
  const delay = Number(record.delay)
  return { label: Number.isFinite(delay) && delay > 0 ? `${delay} ms` : '测速失败', time: formatDateTime(record.time) }
}

export function nodeConnectionStats(node, connections) {
  const names = new Set([node.name, node.proxy_name].filter(Boolean))
  const matches = (connections || []).filter((connection) => {
    const providerIDs = Array.isArray(connection.providerIDs) ? connection.providerIDs : []
    const providerMatches = !providerIDs.length || providerIDs.includes(node.provider_id)
    return providerMatches && (connection.chains || []).some((name) => names.has(name))
  })
  return {
    count: matches.length,
    upload: matches.reduce((total, item) => total + (Number(item.upload) || 0), 0),
    download: matches.reduce((total, item) => total + (Number(item.download) || 0), 0),
    chains: [...new Set(matches.flatMap((item) => item.chains || []))],
  }
}

export function logText(item) {
  return `${item.level || ''} ${item.message || ''} ${(item.fields || []).map((field) => `${field.key || ''}=${field.value || ''}`).join(' ')}`.toLowerCase()
}

export function randomToken() {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_'
  const bytes = new Uint8Array(24)
  crypto.getRandomValues(bytes)
  return [...bytes].map((value) => alphabet[value & 63]).join('')
}

function legacyCopyText(value) {
  const activeElement = document.activeElement
  const selection = document.getSelection()
  const ranges = selection ? Array.from({ length: selection.rangeCount }, (_, index) => selection.getRangeAt(index).cloneRange()) : []
  const textarea = document.createElement('textarea')
  textarea.value = value
  textarea.setAttribute('readonly', '')
  textarea.style.position = 'fixed'
  textarea.style.left = '-9999px'
  textarea.style.opacity = '0'
  document.body.appendChild(textarea)

  let copied = false
  try {
    textarea.focus({ preventScroll: true })
    textarea.select()
    textarea.setSelectionRange(0, textarea.value.length)
    copied = document.execCommand?.('copy') === true
  } finally {
    textarea.remove()
    try { activeElement?.focus?.({ preventScroll: true }) } catch {}
    if (selection) {
      selection.removeAllRanges()
      for (const range of ranges) selection.addRange(range)
    }
  }
  return copied
}

export async function copyText(value) {
  const text = String(value ?? '')
  if (window.isSecureContext && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return
    } catch {}
  }
  if (!legacyCopyText(text)) throw new Error('复制失败，请手动选择并复制')
}
