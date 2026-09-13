import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSessionAdapter, officialSessionStorageKeys } from './session-adapter'

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' }
  })
}

describe('session adapter', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('adds only the current official bearer token', async () => {
    localStorage.setItem(officialSessionStorageKeys.accessToken, 'access-one')
    const fetcher = vi.fn<Fetcher>(async () => jsonResponse({ code: 0, data: {} }))
    const adapter = createSessionAdapter({ storage: localStorage, fetcher })

    await adapter.authorizedFetch('/canvas-api/v1/projects')

    const init = fetcher.mock.calls[0]?.[1] as RequestInit
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer access-one')
    expect(localStorage.getItem('canvas_token')).toBeNull()
  })

  it('refreshes once, persists the official pair, and retries', async () => {
    localStorage.setItem(officialSessionStorageKeys.accessToken, 'expired-access')
    localStorage.setItem(officialSessionStorageKeys.refreshToken, 'refresh-one')
    const fetcher = vi
      .fn<Fetcher>()
      .mockResolvedValueOnce(jsonResponse({ code: 401 }, 401))
      .mockResolvedValueOnce(
        jsonResponse({
          code: 0,
          message: 'success',
          data: {
            access_token: 'access-two',
            refresh_token: 'refresh-two',
            expires_in: 3600,
            token_type: 'Bearer'
          }
        })
      )
      .mockResolvedValueOnce(jsonResponse({ code: 0, data: [] }))
    const adapter = createSessionAdapter({ storage: localStorage, fetcher })

    const response = await adapter.authorizedFetch('/canvas-api/v1/projects')

    expect(response.ok).toBe(true)
    expect(fetcher).toHaveBeenCalledTimes(3)
    expect(fetcher.mock.calls[1]?.[0]).toBe('/api/v1/auth/refresh')
    expect(localStorage.getItem(officialSessionStorageKeys.accessToken)).toBe('access-two')
    expect(localStorage.getItem(officialSessionStorageKeys.refreshToken)).toBe('refresh-two')
    const retryInit = fetcher.mock.calls[2]?.[1] as RequestInit
    expect(new Headers(retryInit.headers).get('Authorization')).toBe('Bearer access-two')
  })

  it('redirects to official login when no session exists', async () => {
    const redirect = vi.fn()
    const fetcher = vi.fn<Fetcher>()
    const adapter = createSessionAdapter({
      storage: localStorage,
      fetcher,
      currentPath: () => '/studio/projects/p-1?tab=edit',
      redirect
    })

    await expect(adapter.authorizedFetch('/canvas-api/v1/projects')).rejects.toThrow(
      'Official session is unavailable'
    )
    expect(fetcher).not.toHaveBeenCalled()
    expect(redirect).toHaveBeenCalledWith(
      '/login?redirect=%2Fstudio%2Fprojects%2Fp-1%3Ftab%3Dedit'
    )
  })

  it('does not loop when refresh or the retried request is rejected', async () => {
    localStorage.setItem(officialSessionStorageKeys.accessToken, 'expired-access')
    localStorage.setItem(officialSessionStorageKeys.refreshToken, 'refresh-one')
    const redirect = vi.fn()
    const fetcher = vi
      .fn<Fetcher>()
      .mockResolvedValueOnce(jsonResponse({}, 401))
      .mockResolvedValueOnce(jsonResponse({}, 401))
    const adapter = createSessionAdapter({ storage: localStorage, fetcher, redirect })

    await expect(adapter.authorizedFetch('/canvas-api/v1/projects')).rejects.toThrow(
      'Official session expired'
    )
    expect(fetcher).toHaveBeenCalledTimes(2)
    expect(redirect).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem(officialSessionStorageKeys.accessToken)).toBeNull()
  })
})

type Fetcher = typeof fetch
