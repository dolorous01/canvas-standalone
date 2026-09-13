import type { CanvasHostContext } from '@sub2api/host-context'
import { createSessionAdapter, type SessionAdapter } from './session-adapter'

interface APIEnvelope {
  code?: number
  message?: string
  reason?: string
}

export interface StandaloneHostOptions {
  session?: SessionAdapter
  apiBaseURL?: string
  locale?: string
  theme?: 'light' | 'dark'
  currentPath?: () => string
  navigate?: (path: string) => void
  notify?: CanvasHostContext['notify']
}

export class CanvasRequestError extends Error {
  readonly status: number
  readonly reason: string

  constructor(status: number, message: string, reason = '') {
    super(message)
    this.name = 'CanvasRequestError'
    this.status = status
    this.reason = reason
  }
}

export function createStandaloneHost(options: StandaloneHostOptions = {}): CanvasHostContext {
  const session = options.session ?? createSessionAdapter()
  const apiBaseURL = normalizeBaseURL(options.apiBaseURL ?? canvasAPIBaseForPath(window.location.pathname))
  const navigate = options.navigate ?? ((path: string) => window.location.assign(path))

  async function send(method: string, path: string, body?: unknown, headers?: Record<string, string>, requestOptions?: { signal?: AbortSignal; timeoutMs?: number }): Promise<Response> {
    if (!path.startsWith('/')) throw new Error('Canvas request path must be same-origin')
    const requestHeaders = new Headers(headers)
    let payload: BodyInit | undefined
    if (body instanceof FormData || body instanceof Blob || typeof body === 'string') {
      payload = body
    } else if (body !== undefined) {
      requestHeaders.set('Content-Type', 'application/json')
      payload = JSON.stringify(body)
    }

    const controller = new AbortController()
    const abort = () => controller.abort(requestOptions?.signal?.reason)
    if (requestOptions?.signal?.aborted) abort()
    requestOptions?.signal?.addEventListener('abort', abort, { once: true })
    const timeout = requestOptions?.timeoutMs
      ? window.setTimeout(() => controller.abort(new DOMException('Canvas request timed out', 'TimeoutError')), requestOptions.timeoutMs)
      : undefined
    try {
      return await session.authorizedFetch(path, {
        method,
        headers: requestHeaders,
        body: payload,
        signal: controller.signal
      })
    } finally {
      if (timeout !== undefined) window.clearTimeout(timeout)
      requestOptions?.signal?.removeEventListener('abort', abort)
    }
  }

  async function decode<T>(response: Response): Promise<T> {
    const contentType = response.headers.get('Content-Type')?.toLowerCase() ?? ''
    const value = contentType.startsWith('application/json')
      ? await response.json() as T & APIEnvelope
      : undefined
    if (!response.ok || (value && typeof value.code === 'number' && value.code !== 0)) {
      const message = value?.message?.trim() || `Canvas request failed with HTTP ${response.status}`
      throw new CanvasRequestError(response.status, message, value?.reason ?? '')
    }
    if (value === undefined) throw new CanvasRequestError(response.status, 'Canvas returned a non-JSON response')
    return value
  }

  return {
    apiBaseURL,
    locale: options.locale ?? readLocale(),
    theme: options.theme ?? readTheme(),
    routeMode: 'user',
    async request<T>(
      method: string,
      path: string,
      body?: unknown,
      headers?: Record<string, string>,
      requestOptions?: { signal?: AbortSignal; timeoutMs?: number }
    ) {
      return decode<T>(await send(method, path, body, headers, requestOptions))
    },
    async stream(path, init = {}) {
      const response = await send(init.method ?? 'GET', path, init.body ?? undefined, Object.fromEntries(new Headers(init.headers)), { signal: init.signal ?? undefined })
      if (!response.ok || !response.body) {
        throw new CanvasRequestError(response.status, `Canvas stream failed with HTTP ${response.status}`)
      }
      return response.body
    },
    navigate,
    notify: options.notify ?? ((level, message) => {
      const writer = level === 'error' ? console.error : level === 'warning' ? console.warn : console.info
      writer(`[canvas] ${message}`)
    })
  }
}

export function canvasBaseForPath(pathname: string): '/studio' | '/studio-next' {
  return pathname === '/studio-next' || pathname.startsWith('/studio-next/') ? '/studio-next' : '/studio'
}

export function canvasAPIBaseForPath(pathname: string): '/canvas-api/v1' | '/canvas-api-next/v1' {
  return canvasBaseForPath(pathname) === '/studio-next' ? '/canvas-api-next/v1' : '/canvas-api/v1'
}

function normalizeBaseURL(value: string): string {
  const normalized = value.trim().replace(/\/+$/, '')
  if (!normalized.startsWith('/') || normalized.startsWith('//')) {
    throw new Error('Canvas API base URL must be a same-origin absolute path')
  }
  return normalized
}

function readLocale(): string {
  return localStorage.getItem('infinite-canvas:locale') || navigator.language || 'zh-CN'
}

function readTheme(): 'light' | 'dark' {
  try {
    const saved = JSON.parse(localStorage.getItem('infinite-canvas:theme_store') || '{}') as { state?: { theme?: string } }
    return saved.state?.theme === 'light' ? 'light' : 'dark'
  } catch {
    return 'dark'
  }
}
