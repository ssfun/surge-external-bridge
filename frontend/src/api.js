let tokenPrompt = null

export function managementToken() {
  return localStorage.getItem('surgeeb-management-token') || ''
}

async function promptForToken() {
  if (!tokenPrompt) {
    tokenPrompt = Promise.resolve(window.prompt('请输入 Management Token')).finally(() => { tokenPrompt = null })
  }
  return tokenPrompt
}

export async function api(path, options = {}, retried = false) {
  const headers = {
    Accept: 'application/json',
    ...(options.body ? { 'Content-Type': 'application/json' } : {}),
    ...(options.headers || {}),
  }
  if (managementToken()) headers.Authorization = `Bearer ${managementToken()}`
  const response = await fetch(path, { ...options, headers })
  if (response.status === 401 && !retried) {
    const value = await promptForToken()
    if (value !== null) {
      localStorage.setItem('surgeeb-management-token', value)
      return api(path, options, true)
    }
  }
  if (!response.ok) {
    let message = `HTTP ${response.status}`
    try { message = (await response.json()).error || message } catch {}
    throw new Error(message)
  }
  return response.status === 204 ? null : response.json()
}

export function encodeID(value) {
  return encodeURIComponent(value)
}
