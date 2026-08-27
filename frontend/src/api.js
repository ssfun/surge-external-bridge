import { reactive } from 'vue'

const legacyTokenKey = 'surgeeb-management-token'

export const authState = reactive({ checking: true, required: false, authenticated: false })

function legacyToken() {
  return localStorage.getItem(legacyTokenKey) || ''
}

function clearLegacyToken() {
  localStorage.removeItem(legacyTokenKey)
}

async function responseError(response) {
  let message = `HTTP ${response.status}`
  let code = ''
  try {
    const body = await response.json()
    message = body.error || message
    code = body.code || ''
  } catch {}
  const error = new Error(message)
  error.status = response.status
  error.code = code
  return error
}

export async function initializeAuth() {
  authState.checking = true
  try {
    const token = legacyToken()
    const headers = token ? { Authorization: `Bearer ${token}` } : {}
    const response = await fetch('/api/session', { headers })
    if (!response.ok) throw await responseError(response)
    const session = await response.json()
    authState.required = Boolean(session.required)
    authState.authenticated = Boolean(session.authenticated)
    if (authState.authenticated) clearLegacyToken()
  } finally {
    authState.checking = false
  }
}

export async function login(token) {
  const response = await fetch('/api/session', {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({ token }),
  })
  if (!response.ok) {
    authState.authenticated = false
    throw await responseError(response)
  }
  clearLegacyToken()
  authState.required = true
  authState.authenticated = true
}

export async function logout() {
  const response = await fetch('/api/session', { method: 'DELETE', headers: { Accept: 'application/json' } })
  if (!response.ok) throw await responseError(response)
  clearLegacyToken()
  authState.authenticated = false
}

export async function api(path, options = {}) {
  const token = legacyToken()
  const headers = {
    Accept: 'application/json',
    ...(options.body ? { 'Content-Type': 'application/json' } : {}),
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
    ...(options.headers || {}),
  }
  const response = await fetch(path, { ...options, headers })
  if (!response.ok) {
    if (response.status === 401) {
      authState.required = true
      authState.authenticated = false
    }
    throw await responseError(response)
  }
  if (token) clearLegacyToken()
  if (authState.required) authState.authenticated = true
  return response.status === 204 ? null : response.json()
}

export function encodeID(value) {
  return encodeURIComponent(value)
}
