const TOKEN_KEY = 'emby_lite_token'
const DEVICE_KEY = 'emby_lite_device_id'

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) || ''
}

export function setToken(token: string) {
  if (token) {
    localStorage.setItem(TOKEN_KEY, token)
  } else {
    localStorage.removeItem(TOKEN_KEY)
  }
}

export function getDeviceId(): string {
  let id = localStorage.getItem(DEVICE_KEY)
  if (!id) {
    id = 'web-' + Math.random().toString(36).slice(2) + Date.now().toString(36)
    localStorage.setItem(DEVICE_KEY, id)
  }
  return id
}

export async function apiFetch<T = any>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers || {})
  const token = getToken()
  if (token) {
    headers.set('X-Emby-Token', token)
  }
  headers.set(
    'X-Emby-Authorization',
    `MediaBrowser Client="Emby-Lite Web", Device="Browser", DeviceId="${getDeviceId()}", Version="0.1.0"`,
  )
  if (init.body && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }

  const res = await fetch(path, { ...init, headers })
  if (res.status === 204) {
    return undefined as T
  }
  const text = await res.text()
  let data: any = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  if (!res.ok) {
    const msg = (data && (data.error || data.Message)) || `HTTP ${res.status}`
    throw new Error(msg)
  }
  return data as T
}

export function itemImageUrl(itemId: string, kind: 'Primary' | 'Backdrop' | 'Thumb' = 'Primary', tag?: string): string {
  const params = new URLSearchParams()
  if (tag) params.set('tag', tag)
  const token = getToken()
  if (token) params.set('api_key', token)
  return `/emby/Items/${encodeURIComponent(itemId)}/Images/${kind}?${params.toString()}`
}
