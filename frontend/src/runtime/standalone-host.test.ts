import { describe, expect, it, vi } from 'vitest'
import i18n from '@/i18n'
import type { SessionAdapter } from './session-adapter'
import { CanvasRequestError, canvasAPIBaseForPath, canvasBaseForPath, createStandaloneHost, resolveStandaloneRole } from './standalone-host'

function sessionWith(response: Response): SessionAdapter {
  return {
    accessToken: () => 'token',
    authorizedFetch: vi.fn().mockResolvedValue(response),
    redirectToLogin: vi.fn()
  }
}

describe('standalone host', () => {
  it('explains rejected image output formats in Chinese without losing the reason code', async () => {
    await i18n.changeLanguage('zh-CN')
    const session = sessionWith(new Response(JSON.stringify({ code: 400, message: 'Unsupported format', reason: 'job_parameter_output_format' }), {
      status: 400, headers: { 'Content-Type': 'application/json' }
    }))
    const host = createStandaloneHost({ session })
    await expect(host.request('POST', '/canvas-api/v1/jobs', {})).rejects.toEqual(
      expect.objectContaining({ status: 400, reason: 'job_parameter_output_format', message: expect.stringContaining('图片输出格式与模型配置不一致') })
    )
  })

  it('uses the authenticated server session to determine administrator access', async () => {
    for (const role of ['user', 'admin']) {
      const session = sessionWith(new Response(JSON.stringify({ code: 0, data: { role, status: 'active' } }), {
        headers: { 'Content-Type': 'application/json' }
      }))
      const host = await resolveStandaloneRole(createStandaloneHost({ session, apiBaseURL: '/canvas-api-next/v1' }))
      expect(host.routeMode).toBe(role)
      expect(session.authorizedFetch).toHaveBeenCalledWith('/canvas-api-next/v1/session', expect.objectContaining({ method: 'GET' }))
    }
    const session = sessionWith(new Response(JSON.stringify({ code: 0, data: { role: 'admin', status: 'disabled' } }), {
      headers: { 'Content-Type': 'application/json' }
    }))
    await expect(resolveStandaloneRole(createStandaloneHost({ session }))).rejects.toThrow('Invalid Canvas session')
  })

  it('selects stable and candidate same-origin prefixes from the entry path', () => {
    expect(canvasBaseForPath('/studio/canvas/project-1')).toBe('/studio')
    expect(canvasAPIBaseForPath('/studio/canvas/project-1')).toBe('/canvas-api/v1')
    expect(canvasBaseForPath('/studio-next/canvas/project-1')).toBe('/studio-next')
    expect(canvasAPIBaseForPath('/studio-next/canvas/project-1')).toBe('/canvas-api-next/v1')
    expect(canvasBaseForPath('/studio-next-evil')).toBe('/studio')
  })

  it('serializes JSON and delegates bearer handling only to the session adapter', async () => {
    const session = sessionWith(new Response(JSON.stringify({ code: 0, message: 'ok', data: { id: 'p-1' } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' }
    }))
    const host = createStandaloneHost({ session, apiBaseURL: '/canvas-api/v1' })

    await host.request('POST', '/canvas-api/v1/projects', { name: 'Test' }, { 'Idempotency-Key': 'one' })

    expect(session.authorizedFetch).toHaveBeenCalledWith('/canvas-api/v1/projects', expect.objectContaining({ method: 'POST', body: JSON.stringify({ name: 'Test' }) }))
    const init = vi.mocked(session.authorizedFetch).mock.calls[0]?.[1]
    const headers = new Headers(init?.headers)
    expect(headers.get('Content-Type')).toBe('application/json')
    expect(headers.get('Authorization')).toBeNull()
  })

  it('turns a redacted API error envelope into a typed error', async () => {
    const session = sessionWith(new Response(JSON.stringify({ code: 503, message: 'Writes are disabled', reason: 'writes_disabled' }), {
      status: 503,
      headers: { 'Content-Type': 'application/json' }
    }))
    const host = createStandaloneHost({ session, apiBaseURL: '/canvas-api/v1' })

    await expect(host.request('POST', '/canvas-api/v1/projects', {})).rejects.toEqual(
      expect.objectContaining<Partial<CanvasRequestError>>({ status: 503, reason: 'writes_disabled' })
    )
  })
})
