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
    `MediaBrowser Client="Emby-Lite Web", Device="Browser", DeviceId="${getDeviceId()}", Version="0.3.16"`,
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

// Lightweight in-memory SWR cache so back-navigation & tab switches render instantaneously.
interface CacheEntry {
  data: any
  ts: number
}
const swrCache = new Map<string, CacheEntry>()
const inflight = new Map<string, Promise<any>>()

export function getCachedData<T = any>(key: string, maxAgeMs = 120000): T | undefined {
  const hit = swrCache.get(key)
  if (!hit) return undefined
  if (Date.now() - hit.ts > maxAgeMs) return undefined
  return hit.data as T
}

export function setCachedData(key: string, data: any) {
  if (swrCache.size > 160) {
    const oldestKey = swrCache.keys().next().value
    if (oldestKey) swrCache.delete(oldestKey)
  }
  swrCache.set(key, { data, ts: Date.now() })
}

export function invalidateApiCache(prefix?: string) {
  if (!prefix) {
    swrCache.clear()
    return
  }
  for (const k of swrCache.keys()) {
    if (k.startsWith(prefix)) {
      swrCache.delete(k)
    }
  }
}

export async function apiFetchCached<T = any>(
  path: string,
  options: { ttlMs?: number; force?: boolean; onUpdate?: (fresh: T) => void } = {},
): Promise<T> {
  const ttlMs = options.ttlMs ?? 30000
  const hit = swrCache.get(path)
  const now = Date.now()

  if (hit && !options.force) {
    if (now - hit.ts < ttlMs) {
      return hit.data as T
    }
    // Stale-while-revalidate: return stale immediately and refresh silently in background.
    if (!inflight.has(path)) {
      const bg = apiFetch<T>(path)
        .then((fresh) => {
          setCachedData(path, fresh)
          options.onUpdate?.(fresh)
          return fresh
        })
        .catch(() => hit.data as T)
        .finally(() => inflight.delete(path))
      inflight.set(path, bg)
    }
    return hit.data as T
  }

  if (inflight.has(path)) {
    return inflight.get(path) as Promise<T>
  }

  const req = apiFetch<T>(path)
    .then((fresh) => {
      setCachedData(path, fresh)
      return fresh
    })
    .finally(() => inflight.delete(path))
  inflight.set(path, req)
  return req
}

export function itemImageUrl(
  itemId: string,
  kind: 'Primary' | 'Backdrop' | 'Thumb' | 'Logo' | 'Banner' = 'Primary',
  tag?: string,
  maxWidth?: number,
): string {
  const params = new URLSearchParams()
  if (tag) params.set('tag', tag)
  if (maxWidth && maxWidth > 0) params.set('maxWidth', String(maxWidth))
  const token = getToken()
  if (token) params.set('api_key', token)
  return `/emby/Items/${encodeURIComponent(itemId)}/Images/${kind}?${params.toString()}`
}
