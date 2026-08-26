import { defineStore } from 'pinia'

export const routeStreams = {
  overview: ['connections', 'traffic', 'memory'],
  providers: [],
  nodes: ['connections'],
  connections: ['connections', 'traffic', 'memory'],
  logs: ['logs'],
  settings: [],
}

const sockets = new Map()
const reconnectTimers = new Map()
const deliveryTimers = new Map()
const pendingPayloads = new Map()
let activeRoute = 'overview'
let enabled = true
let nextLogID = 1

function streamPath(name, logLevel) {
  const paths = {
    connections: '/api/mihomo/connections',
    traffic: '/api/mihomo/traffic',
    memory: '/api/mihomo/memory',
    logs: `/api/mihomo/logs?level=${logLevel === 'debug' ? 'debug' : 'info'}`,
  }
  return paths[name]
}

export const useRealtimeStore = defineStore('realtime', {
  state: () => ({
    connections: { connections: [] },
    traffic: { up: 0, down: 0 },
    memory: { inuse: 0 },
    logs: [],
    paused: false,
    reduced: localStorage.getItem('surgeeb-reduced-updates') === '1',
    logLevel: '',
  }),
  actions: {
    applyPayload(name, data) {
      if (name === 'logs') {
        const items = Array.isArray(data) ? data : [data]
        this.logs.push(...items.map((item) => ({ ...item, _ui_id: nextLogID++ })))
        if (this.logs.length > 500) this.logs.splice(0, this.logs.length - 500)
      } else this[name] = data
    },
    queuePayload(name, data) {
      if (!this.reduced) return this.applyPayload(name, data)
      if (name === 'logs') pendingPayloads.set(name, [...(pendingPayloads.get(name) || []), data])
      else pendingPayloads.set(name, data)
      if (deliveryTimers.has(name)) return
      deliveryTimers.set(name, window.setTimeout(() => {
        deliveryTimers.delete(name)
        const pending = pendingPayloads.get(name)
        pendingPayloads.delete(name)
        if (pending !== undefined && !this.paused) this.applyPayload(name, pending)
      }, 1500))
    },
    setReduced(value) {
      this.reduced = value
      localStorage.setItem('surgeeb-reduced-updates', value ? '1' : '0')
      if (!value) {
        if (!this.paused) {
          for (const [name, pending] of pendingPayloads) this.applyPayload(name, pending)
        }
        pendingPayloads.clear()
        for (const timer of deliveryTimers.values()) window.clearTimeout(timer)
        deliveryTimers.clear()
      }
    },
    setPaused(value) {
      this.paused = value
      if (value) {
        for (const timer of deliveryTimers.values()) window.clearTimeout(timer)
        deliveryTimers.clear()
        pendingPayloads.clear()
      }
      this.sync(activeRoute)
    },
    setLogLevel(value) {
      const changed = (this.logLevel === 'debug') !== (value === 'debug')
      this.logLevel = value
      if (changed && sockets.has('logs')) {
        sockets.get('logs').close()
        sockets.delete('logs')
        this.sync(activeRoute)
      }
    },
    clearLogs() {
      this.logs.splice(0)
      pendingPayloads.delete('logs')
    },
    sync(routeName) {
      enabled = true
      activeRoute = routeName
      const desired = new Set(document.hidden || this.paused ? [] : routeStreams[routeName] || [])
      for (const [name, socket] of sockets) {
        if (desired.has(name)) continue
        socket.close()
        sockets.delete(name)
      }
      for (const name of desired) this.connect(name)
    },
    connect(name) {
      const existing = sockets.get(name)
      if (existing && existing.readyState < 2) return
      const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
      const socket = new WebSocket(`${protocol}//${location.host}${streamPath(name, this.logLevel)}`)
      sockets.set(name, socket)
      socket.onmessage = (event) => {
        if (this.paused) return
        try {
          const data = JSON.parse(event.data)
          this.queuePayload(name, data)
        } catch {}
      }
      socket.onclose = () => {
        if (sockets.get(name) === socket) sockets.delete(name)
        window.clearTimeout(reconnectTimers.get(name))
        reconnectTimers.set(name, window.setTimeout(() => {
          if (enabled && !document.hidden && !this.paused && (routeStreams[activeRoute] || []).includes(name)) this.connect(name)
        }, 3000))
      }
    },
    stop() {
      enabled = false
      for (const socket of sockets.values()) socket.close()
      sockets.clear()
      for (const timer of reconnectTimers.values()) window.clearTimeout(timer)
      reconnectTimers.clear()
      for (const timer of deliveryTimers.values()) window.clearTimeout(timer)
      deliveryTimers.clear()
      pendingPayloads.clear()
    },
  },
})
