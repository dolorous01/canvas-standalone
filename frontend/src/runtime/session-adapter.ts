const ACCESS_TOKEN_KEY = 'auth_token'
const REFRESH_TOKEN_KEY = 'refresh_token'
const EXPIRES_AT_KEY = 'token_expires_at'

type Fetcher = typeof fetch

export interface SessionAdapterOptions {
  storage?: Storage
  fetcher?: Fetcher
  apiBaseURL?: string
  currentPath?: () => string
  redirect?: (target: string) => void
}

export interface OfficialTokenPair {
  access_token: string
  refresh_token: string
  expires_in: number
  token_type: string
}

interface Envelope<T> {
  code: number
  message: string
  data?: T
}

export interface SessionAdapter {
  accessToken(): string | null
  authorizedFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response>
  redirectToLogin(): void
}

export function createSessionAdapter(options: SessionAdapterOptions = {}): SessionAdapter {
  const storage = options.storage ?? window.localStorage
  const fetcher = options.fetcher ?? window.fetch.bind(window)
  const apiBaseURL = (options.apiBaseURL ?? '/api/v1').replace(/\/$/, '')
  const currentPath = options.currentPath ?? (() => `${window.location.pathname}${window.location.search}${window.location.hash}`)
  const redirect = options.redirect ?? ((target) => window.location.assign(target))
  let refreshInFlight: Promise<OfficialTokenPair> | null = null

  function accessToken(): string | null {
    const token = storage.getItem(ACCESS_TOKEN_KEY)?.trim()
    return token || null
  }

  function redirectToLogin(): void {
    redirect(`/login?redirect=${encodeURIComponent(currentPath())}`)
  }

  async function refresh(): Promise<OfficialTokenPair> {
    if (refreshInFlight) return refreshInFlight
    const refreshToken = storage.getItem(REFRESH_TOKEN_KEY)?.trim()
    if (!refreshToken) throw new Error('No refresh token available')

    const pending = (async () => {
      const response = await fetcher(`${apiBaseURL}/auth/refresh`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ refresh_token: refreshToken }),
        credentials: 'same-origin'
      })
      if (!response.ok) throw new Error('Official session refresh failed')
      const contentType = response.headers.get('Content-Type') ?? ''
      if (!contentType.toLowerCase().startsWith('application/json')) {
        throw new Error('Official session refresh returned an invalid response')
      }
      const envelope = (await response.json()) as Envelope<OfficialTokenPair>
      const pair = envelope.data
      if (
        envelope.code !== 0 ||
        !pair ||
        typeof pair.access_token !== 'string' ||
        !pair.access_token ||
        typeof pair.refresh_token !== 'string' ||
        !pair.refresh_token ||
        !Number.isFinite(pair.expires_in) ||
        pair.expires_in <= 0 ||
        pair.token_type.toLowerCase() !== 'bearer'
      ) {
        throw new Error('Official session refresh contract changed')
      }
      if (storage.getItem(REFRESH_TOKEN_KEY)?.trim() !== refreshToken) {
        throw new Error('Official session changed during refresh')
      }
      storage.setItem(ACCESS_TOKEN_KEY, pair.access_token)
      storage.setItem(EXPIRES_AT_KEY, String(Date.now() + pair.expires_in * 1000))
      storage.setItem(REFRESH_TOKEN_KEY, pair.refresh_token)
      return pair
    })()
    refreshInFlight = pending
    try {
      return await pending
    } finally {
      if (refreshInFlight === pending) refreshInFlight = null
    }
  }

  async function send(input: RequestInfo | URL, init: RequestInit, token: string): Promise<Response> {
    const headers = new Headers(init.headers)
    headers.set('Authorization', `Bearer ${token}`)
    headers.set('Accept', headers.get('Accept') ?? 'application/json')
    return fetcher(input, { ...init, headers, credentials: 'same-origin' })
  }

  async function authorizedFetch(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
    const initialToken = accessToken()
    if (!initialToken) {
      redirectToLogin()
      throw new Error('Official session is unavailable')
    }
    let response = await send(input, init, initialToken)
    if (response.status !== 401) return response
    try {
      const pair = await refresh()
      response = await send(input, init, pair.access_token)
      if (response.status === 401) {
        redirectToLogin()
      }
      return response
    } catch {
      storage.removeItem(ACCESS_TOKEN_KEY)
      storage.removeItem(REFRESH_TOKEN_KEY)
      storage.removeItem(EXPIRES_AT_KEY)
      redirectToLogin()
      throw new Error('Official session expired')
    }
  }

  return { accessToken, authorizedFetch, redirectToLogin }
}

export const officialSessionStorageKeys = Object.freeze({
  accessToken: ACCESS_TOKEN_KEY,
  refreshToken: REFRESH_TOKEN_KEY,
  expiresAt: EXPIRES_AT_KEY
})

