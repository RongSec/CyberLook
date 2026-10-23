import { defineStore } from 'pinia'

export const useThemeStore = defineStore('theme', {
  state: () => ({
    theme: localStorage.getItem('theme') || 'dark'
  }),
  actions: {
    setTheme(t) {
      this.theme = t
      localStorage.setItem('theme', t)
    },
    toggle() {
      this.setTheme(this.theme === 'dark' ? 'light' : 'dark')
    }
  }
})
