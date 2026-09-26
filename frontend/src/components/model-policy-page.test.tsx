import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { App } from 'antd'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import upstreamI18n from '@/i18n'
import { CanvasHostProvider, createCanvasHostStore, type CanvasHostContext } from '@sub2api/host-context'
import { CanvasRequestError } from '@sub2api/runtime/standalone-host'
import { defaultImageCapability } from './model-policy-form'
import ModelPolicyPage from './model-policy-page'
import { ModelPolicyLink } from './model-policy-link'

const refresh = vi.hoisted(() => vi.fn().mockResolvedValue(undefined))
vi.mock('@sub2api/adapters/use-config-store', () => ({ refreshCanvasConfigStore: refresh }))
;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

describe('model policy management', () => {
  let root: Root
  const policy = { enabled: true, version: 7, models: [{ model: 'gpt-image-2', enabled: true, position: 0, capability: defaultImageCapability }] }
  const request = vi.fn()
  const navigate = vi.fn()

  beforeEach(async () => {
    vi.clearAllMocks()
    const getStyle = window.getComputedStyle.bind(window)
    vi.stubGlobal('getComputedStyle', (element: Element) => getStyle(element))
    vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
    vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: false, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {} })))
    await upstreamI18n.changeLanguage('en-US')
    request.mockImplementation(async (method: string) => ({ code: 0, data: { ...policy, version: method === 'PUT' ? 8 : 7 } }))
    const container = document.createElement('div')
    document.body.append(container)
    root = createRoot(container)
  })

  afterEach(async () => {
    await act(async () => root.unmount())
    document.body.replaceChildren()
    vi.unstubAllGlobals()
  })

  async function mount(role: 'user' | 'admin', link = false) {
    const host: CanvasHostContext = {
      apiBaseURL: '/canvas-api-next/v1', locale: 'en-US', theme: 'light', routeMode: role,
      request, navigate, notify: vi.fn(), stream: async () => new ReadableStream<Uint8Array>()
    }
    await act(async () => root.render(
      <CanvasHostProvider store={createCanvasHostStore(host)}><App><MemoryRouter>
        {link ? <ModelPolicyLink /> : <ModelPolicyPage />}
      </MemoryRouter></App></CanvasHostProvider>
    ))
  }

  function button(label: string, scope: ParentNode = document) {
    const result = Array.from(scope.querySelectorAll('button')).find((item) => item.textContent?.trim() === label)
    expect(result, `button ${label}`).toBeDefined()
    return result!
  }



  it('loads keys again when the add dialog is reopened', async () => {
    let calls = 0
    request.mockImplementation(async (_method: string, path: string) => {
      if (path.endsWith('/credentials/candidates')) {
        calls++
        return { code: 0, data: { items: calls === 1 ? [] : [{ id: 32, name: 'New binding', bound: true, status: 'active', quota: 0, quota_used: 0 }] } }
      }
      if (path.includes('/model-discovery?')) return { code: 0, data: { api_key_id: 32, total: 0, models: [] } }
      return { code: 0, data: policy }
    })
    await mount('admin')
    await act(async () => button('Add model').click())
    expect(document.body.textContent).toContain('No eligible bound key.')
    await act(async () => button('Cancel', document.querySelector('.ant-modal')!).click())
    await act(async () => button('Add model').click())
    expect(calls).toBe(2)
    expect(document.body.textContent).toContain('No compatible image models recognized.')
  })
  it('automatically discovers a single bound key and imports the selected image model', async () => {
    const discovered = { model: 'gpt-image-2.5-flare', capability: defaultImageCapability, configured: false, parameter_source: 'basic' }
    request.mockImplementation(async (method: string, path: string, body: unknown) => {
      if (path.endsWith('/credentials/candidates')) return { code: 0, data: { items: [{ id: 32, name: 'Studio', bound: true, status: 'active', quota: 0, quota_used: 0 }] } }
      if (path.includes('/model-discovery?')) return { code: 0, data: { api_key_id: 32, total: 3, models: [discovered, { ...discovered, model: 'gpt-image-2', configured: true }] } }
      return { code: 0, data: method === 'PUT' ? { ...(body as object), version: 8 } : policy }
    })
    await mount('admin')
    await act(async () => button('Add model').click())
    expect(request.mock.calls.some(([, path]) => path.endsWith('/admin/model-discovery?api_key_id=32'))).toBe(true)
    const select = document.querySelector<HTMLSelectElement>('select[aria-label="Select a discovered image model"]')!
    expect(select).not.toBeNull()
    expect(select.querySelector<HTMLOptionElement>('option[value="gpt-image-2"]')?.disabled).toBe(true)
    await act(async () => { select.value = discovered.model; select.dispatchEvent(new Event('change', { bubbles: true })) })
    await act(async () => button('Save', document.querySelector('.ant-modal')!).click())
    const put = request.mock.calls.find(([method]) => method === 'PUT')!
    expect(put[2].version).toBe(7)
    expect(put[2].models).toEqual([...policy.models, { model: discovered.model, capability: defaultImageCapability, enabled: true, position: 1 }])
    expect(refresh).toHaveBeenCalledOnce()
  })

  it('discards late results from the previous key and clears selection on a new probe', async () => {
    let finishFirst!: (value: unknown) => void
    request.mockImplementation(async (_method: string, path: string) => {
      if (path.endsWith('/credentials/candidates')) return { code: 0, data: { items: [32, 33].map((id) => ({ id, name: String(id), bound: true, status: 'active', quota: 0, quota_used: 0 })) } }
      if (path.endsWith('api_key_id=32')) return new Promise((resolve) => { finishFirst = resolve })
      if (path.endsWith('api_key_id=33')) return { code: 0, data: { api_key_id: 33, total: 1, models: [{ model: 'gpt-image-2.5-sunburst', capability: defaultImageCapability, configured: false }] } }
      return { code: 0, data: policy }
    })
    await mount('admin')
    await act(async () => button('Add model').click())
    const key = document.querySelector<HTMLSelectElement>('select[aria-label="Select a bound API key"]')!
    await act(async () => { key.value = '32'; key.dispatchEvent(new Event('change', { bubbles: true })) })
    await act(async () => { key.value = '33'; key.dispatchEvent(new Event('change', { bubbles: true })) })
    await act(async () => finishFirst({ code: 0, data: { api_key_id: 32, total: 1, models: [{ model: 'stale-image', capability: defaultImageCapability }] } }))
    expect(document.body.textContent).not.toContain('stale-image')
    const select = document.querySelector<HTMLSelectElement>('select[aria-label="Select a discovered image model"]')!
    await act(async () => { select.value = 'gpt-image-2.5-sunburst'; select.dispatchEvent(new Event('change', { bubbles: true })) })
    expect(button('Save', document.querySelector('.ant-modal')!).disabled).toBe(false)
    await act(async () => { key.value = '32'; key.dispatchEvent(new Event('change', { bubbles: true })) })
    expect(button('Save', document.querySelector('.ant-modal')!).disabled).toBe(true)
    expect(document.querySelector('select[aria-label="Select a discovered image model"]')).toBeNull()
  })

  it('keeps save disabled when model discovery fails', async () => {
    request.mockImplementation(async (_method: string, path: string) => {
      if (path.endsWith('/credentials/candidates')) return { code: 0, data: { items: [{ id: 32, name: 'Studio', bound: true, status: 'active', quota: 0, quota_used: 0 }] } }
      if (path.includes('/model-discovery?')) throw new CanvasRequestError(403, 'Forbidden')
      return { code: 0, data: policy }
    })
    await mount('admin')
    await act(async () => button('Add model').click())
    expect(document.body.textContent).toContain('Discovery failed.')
    expect(button('Save', document.querySelector('.ant-modal')!).disabled).toBe(true)
  })
  it('denies ordinary users without requesting or displaying the admin policy', async () => {
    await mount('user')
    expect(document.body.textContent).toContain('Only administrators can manage models')
    expect(document.body.textContent).not.toContain('gpt-image-2')
    expect(request).not.toHaveBeenCalled()
  })

  it('keeps the candidate management link in the candidate environment', async () => {
    await mount('admin', true)
    await act(async () => button('Model management').click())
    expect(navigate).toHaveBeenCalledWith('/studio-next/admin/models')
  })

  it('saves with the original version and refreshes the model selection after success', async () => {
    await mount('admin')
    await act(async () => button('Edit model').click())
    await act(async () => button('Save', document.querySelector('.ant-modal')!).click())
    expect(request).toHaveBeenCalledWith('PUT', '/canvas-api-next/v1/admin/model-policy', policy, undefined)
    expect(refresh).toHaveBeenCalledOnce()
    expect(document.body.textContent).toContain('Configuration version 8')
  })

  it('keeps a conflicting draft without automatically retrying or refreshing configuration', async () => {
    request.mockImplementation(async (method: string) => {
      if (method === 'PUT') throw new CanvasRequestError(409, 'Conflict', 'policy_version_conflict')
      return { code: 0, data: policy }
    })
    await mount('admin')
    await act(async () => button('Edit model').click())
    await act(async () => button('Save', document.querySelector('.ant-modal')!).click())
    expect(document.querySelector('.ant-modal')?.textContent).toContain('Another administrator changed')
    expect(button('Save', document.querySelector('.ant-modal')!).disabled).toBe(true)
    expect(request.mock.calls.filter(([method]) => method === 'PUT')).toHaveLength(1)
    expect(refresh).not.toHaveBeenCalled()
  })
})
