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

export function formatDateTime(value) {
  if (!value || value === '—') return '—'
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? String(value) : date.toLocaleString()
}

export function providerTypeLabel(type) {
  return ({ http: 'HTTP 订阅', file: '私有文件', inline: 'Inline' })[type] || type
}

export function nodeConnectionStats(node, connections) {
  const names = new Set([node.name, node.proxy_name].filter(Boolean))
  const matches = (connections || []).filter((connection) => (connection.chains || []).some((name) => names.has(name)))
  return {
    count: matches.length,
    upload: matches.reduce((total, item) => total + (Number(item.upload) || 0), 0),
    download: matches.reduce((total, item) => total + (Number(item.download) || 0), 0),
    chains: [...new Set(matches.flatMap((item) => item.chains || []))],
  }
}

export function nodeHistory(node) {
  const values = (Array.isArray(node.history) ? node.history : []).filter((item) => item.delay).slice(-4)
  if (!values.length) return '—'
  const current = values.at(-1)
  const previous = values.slice(0, -1).map((item) => `${item.delay} ms`)
  return `当前 ${current.delay} ms${previous.length ? ` · 历史 ${previous.join(' / ')}` : ''}`
}

export function logText(item) {
  return `${item.level || ''} ${item.message || ''} ${(item.fields || []).map((field) => `${field.key || ''}=${field.value || ''}`).join(' ')}`.toLowerCase()
}

export function randomToken() {
  const bytes = new Uint8Array(32)
  crypto.getRandomValues(bytes)
  return [...bytes].map((value) => value.toString(16).padStart(2, '0')).join('')
}
