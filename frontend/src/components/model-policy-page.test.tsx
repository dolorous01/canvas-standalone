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
