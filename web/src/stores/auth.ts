import { defineStore } from 'pinia'
import { apiFetch, getToken, setToken } from '../api/client'

export interface EmbyUser {
  Id: string
  Name: string
  Policy?: {
    IsAdministrator?: boolean
    EnableContentDownloading?: boolean
  }
}

export const useAuthStore = defineStore('auth', {
  state: () => ({
    initialized: true as boolean,
    bootstrapped: false as boolean,
    serverName: 'Emby-Lite',
    user: null as EmbyUser | null,
    token: getToken(),
  }),
  getters: {
    isAuthenticated: (s) => Boolean(s.token && s.user),
    isAdmin: (s) => Boolean(s.user?.Policy?.IsAdministrator),
  },
  actions: {
    async bootstrap() {
      const data = await apiFetch<{ initialized: boolean; server_name: string }>('/api/web/bootstrap')
      this.initialized = data.initialized
      this.serverName = data.server_name || 'Emby-Lite'
      this.bootstrapped = true
      if (this.initialized && this.token) {
        try {
          this.user = await apiFetch<EmbyUser>('/emby/Users/Me')
        } catch {
          this.clearAuth()
        }
      }
    },
    async setup(username: string, password: string, serverName: string) {
      const res = await apiFetch<{ access_token: string }>('/api/web/setup', {
        method: 'POST',
        body: JSON.stringify({ username, password, server_name: serverName }),
      })
      setToken(res.access_token)
      this.token = res.access_token
      this.initialized = true
      if (serverName) this.serverName = serverName
      this.user = await apiFetch<EmbyUser>('/emby/Users/Me')
    },
    async login(username: string, password: string) {
      const res = await apiFetch<{ AccessToken: string; User: EmbyUser }>('/emby/Users/AuthenticateByName', {
        method: 'POST',
        body: JSON.stringify({ Username: username, Pw: password }),
      })
      setToken(res.AccessToken)
      this.token = res.AccessToken
      this.user = res.User
    },
    async logout() {
      try {
        await apiFetch('/emby/Sessions/Logout', { method: 'POST' })
      } catch {
        // ignore network errors on logout
      }
      this.clearAuth()
    },
    clearAuth() {
      setToken('')
      this.token = ''
      this.user = null
    },
  },
})
