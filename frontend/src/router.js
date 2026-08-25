import { createRouter, createWebHashHistory } from 'vue-router'
import OverviewView from '@/views/OverviewView.vue'
import ProvidersView from '@/views/ProvidersView.vue'
import NodesView from '@/views/NodesView.vue'
import ConnectionsView from '@/views/ConnectionsView.vue'
import LogsView from '@/views/LogsView.vue'
import SettingsView from '@/views/SettingsView.vue'

export function clearDocumentSelection() {
  window.getSelection?.()?.removeAllRanges()
}

export function finalizeNavigation(failure) {
  if (failure) return
  clearDocumentSelection()
  window.scrollTo({ top: 0, behavior: 'instant' })
}

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', name: 'overview', component: OverviewView },
    { path: '/providers', name: 'providers', component: ProvidersView },
    { path: '/nodes', name: 'nodes', component: NodesView },
    { path: '/connections', name: 'connections', component: ConnectionsView },
    { path: '/logs', name: 'logs', component: LogsView },
    { path: '/settings', name: 'settings', component: SettingsView },
  ],
})

router.afterEach((_to, _from, failure) => finalizeNavigation(failure))

export default router
