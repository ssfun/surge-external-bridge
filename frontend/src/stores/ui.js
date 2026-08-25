import { defineStore } from 'pinia'

let nextToastID = 1

export const useUIStore = defineStore('ui', {
  state: () => ({ toasts: [], announcement: '' }),
  actions: {
    toast(message, bad = false) {
      const id = nextToastID++
      this.toasts.push({ id, message, bad })
      this.announcement = message
      window.setTimeout(() => {
        this.toasts = this.toasts.filter((item) => item.id !== id)
      }, bad ? 6500 : 4500)
    },
  },
})
