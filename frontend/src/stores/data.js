import { defineStore } from 'pinia'
import { api, authState, encodeID, login } from '@/api.js'

const resourcePaths = {
  overview: '/api/overview',
  providers: '/api/providers',
  policyPaths: '/api/policy-paths',
  nodes: '/api/nodes',
  events: '/api/events',
  settings: '/api/settings',
  service: '/api/service',
}

export const routeResources = {
  overview: ['overview', 'providers', 'nodes'],
  providers: ['providers', 'nodes'],
  policyPaths: ['policyPaths', 'providers', 'nodes'],
  nodes: ['nodes'],
  connections: [],
  logs: ['events', 'providers', 'nodes'],
  settings: ['settings', 'service'],
}

export const backgroundResources = {
  overview: ['overview', 'providers', 'nodes'],
  providers: ['providers', 'nodes'],
  policyPaths: ['policyPaths', 'providers', 'nodes'],
  nodes: ['nodes'],
  connections: [],
  logs: ['events'],
  settings: [],
}

const requests = new Map()
const snapshots = new Map()
let requestGeneration = 0

export const useDataStore = defineStore('data', {
  state: () => ({
    overview: null,
    providers: [],
    policyPaths: [],
    nodes: [],
    events: [],
    settings: null,
    service: null,
    loaded: {},
    loadedAt: {},
    resourceErrors: {},
    pendingRefresh: [],
  }),
  actions: {
    resetSession() {
      requestGeneration += 1
      requests.clear()
      snapshots.clear()
      this.$reset()
    },
    async loadResource(name, { background = false } = {}) {
      if (background && Date.now() - (this.loadedAt[name] || 0) < 5000) return false
      if (requests.has(name)) return requests.get(name)
      const generation = requestGeneration
      const request = (async () => {
        let value
        try {
          value = await api(resourcePaths[name])
        } catch (error) {
          if (generation !== requestGeneration) return false
          this.resourceErrors[name] = error.message || '连接失败'
          if (name === 'service') value = { error: error.message || '服务状态检测失败' }
          else throw error
        }
        if (generation !== requestGeneration) return false
        if (!value?.error) delete this.resourceErrors[name]
        this.pendingRefresh = this.pendingRefresh.filter((item) => item !== name)
        const snapshot = JSON.stringify(value)
        const changed = snapshots.get(name) !== snapshot
        snapshots.set(name, snapshot)
        this.loaded[name] = true
        this.loadedAt[name] = Date.now()
        if (!changed) return false
        if (name === 'providers') {
          const runtime = new Map(this.providers.map((provider) => [provider.stable_id, {
            runtime: provider.runtime,
            runtimeError: provider.runtimeError,
          }]))
          value = value.map((provider) => ({ ...provider, ...runtime.get(provider.stable_id) }))
        }
        this[name] = value
        return true
      })().finally(() => { if (requests.get(name) === request) requests.delete(name) })
      requests.set(name, request)
      return request
    },
    async refreshRoute(routeName, { background = false } = {}) {
      const names = [...(background ? backgroundResources[routeName] || [] : routeResources[routeName] || [])]
      if (!names.includes('overview')) names.unshift('overview')
      for (const name of this.pendingRefresh) if (!names.includes(name)) names.push(name)
      const results = await Promise.allSettled(names.map((name) => this.loadResource(name, { background })))
      const failure = results.find((result) => result.status === 'rejected')
      if (failure) throw failure.reason
    },
    async reloadResources(names) {
      const results = await Promise.allSettled(names.map(async (name) => {
        if (requests.has(name)) await requests.get(name).catch(() => {})
        return this.loadResource(name)
      }))
      const failure = results.find((result) => result.status === 'rejected')
      if (failure) throw failure.reason
    },
    async refreshAfterMutation(names) {
      try {
        await this.reloadResources(names)
        return true
      } catch {
        this.pendingRefresh = [...new Set([...this.pendingRefresh, ...names.filter((name) => this.resourceErrors[name])])]
        // The write has already succeeded. Retry only reads, never the mutation.
        return false
      }
    },
    async retryPendingRefresh() {
      return this.refreshAfterMutation([...this.pendingRefresh])
    },
    async loadProviderRuntime(id, { quiet = false } = {}) {
      const key = `provider-runtime:${id}`
      if (requests.has(key)) return requests.get(key)
      const generation = requestGeneration
      const request = api(`/api/providers/${encodeID(id)}/runtime`).then((runtime) => {
        if (generation !== requestGeneration) return
        const provider = this.providers.find((item) => item.stable_id === id)
        if (!provider) return
        provider.runtime = runtime
        provider.runtimeError = ''
      }).catch((error) => {
        if (generation !== requestGeneration) return
        const provider = this.providers.find((item) => item.stable_id === id)
        if (provider) provider.runtimeError = error.message
        if (!quiet) throw error
      }).finally(() => { if (requests.get(key) === request) requests.delete(key) })
      requests.set(key, request)
      return request
    },
    async saveProvider(provider, id = '', file = null) {
      const body = file ? new FormData() : JSON.stringify(provider)
      if (file) {
        body.append('provider', JSON.stringify(provider))
        body.append('file', file)
      }
      await api(id ? `/api/providers/${encodeID(id)}` : '/api/providers', {
        method: id ? 'PUT' : 'POST',
        body,
      })
      await this.refreshAfterMutation(['providers', 'nodes', 'overview'])
    },
    async deleteProvider(id) {
      await api(`/api/providers/${encodeID(id)}`, { method: 'DELETE' })
      await this.refreshAfterMutation(['providers', 'nodes', 'overview'])
    },
    async reorderProviders(providerIDs) {
      await api('/api/providers/order', {
        method: 'PUT',
        body: JSON.stringify({ provider_ids: providerIDs }),
      })
      await this.refreshAfterMutation(['providers', 'nodes', 'overview'])
    },
    async savePolicyPath(path, id = '') {
      await api(id ? `/api/policy-paths/${encodeID(id)}` : '/api/policy-paths', {
        method: id ? 'PUT' : 'POST',
        body: JSON.stringify(path),
      })
      await this.refreshAfterMutation(['policyPaths', 'overview'])
    },
    async deletePolicyPath(id) {
      await api(`/api/policy-paths/${encodeID(id)}`, {
        method: 'DELETE',
        headers: { 'X-SurgeEB-Confirm': 'delete-policy-path' },
      })
      await this.refreshAfterMutation(['policyPaths', 'overview'])
    },
    async regeneratePolicyPathToken(id) {
      await api(`/api/policy-paths/${encodeID(id)}/token`, {
        method: 'POST',
        headers: { 'X-SurgeEB-Confirm': 'regenerate-policy-path-token' },
      })
      await this.refreshAfterMutation(['policyPaths', 'overview', 'settings'])
    },
    async refreshProvider(id) {
      await api(`/api/providers/${encodeID(id)}/refresh`, { method: 'POST' })
      await this.refreshAfterMutation(['providers', 'nodes', 'overview'])
      await this.loadProviderRuntime(id, { quiet: true })
    },
    async healthCheckProvider(id) {
      await api(`/api/providers/${encodeID(id)}/healthcheck`, { method: 'POST' })
      await this.refreshAfterMutation(['providers', 'nodes', 'overview'])
      await this.loadProviderRuntime(id, { quiet: true })
    },
    async updateSettings(body) {
      const result = await api('/api/settings', { method: 'PUT', body: JSON.stringify(body) })
      if (body.management_token && !result.reconnect) {
        try { await login(body.management_token) }
        catch {
          authState.authenticated = false
          authState.required = true
          result.sessionExpired = true
          result.refreshed = false
          return result
        }
      }
      if (!result.reconnect) result.refreshed = await this.refreshAfterMutation(['settings', 'overview', 'providers', 'nodes'])
      return result
    },
  },
})
