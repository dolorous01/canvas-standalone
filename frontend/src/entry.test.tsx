import { act } from 'react'
import { describe, expect, it, vi } from 'vitest'
import type { CanvasHostContext } from './host-context'

;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const configState = vi.hoisted(() => ({
  canvasConfig: {
    enabled: true,
    api_keys: [{ id: 1, name: 'Studio key', group_id: 1, group_name: 'images', available: true }],
    policy_version: 1,
    models: []
  } as import('./api/canvas-api').CanvasConfig | undefined,
  canvasConfigLoading: false,
  canvasConfigError: ''
}))

const initializeConfig = vi.hoisted(() => vi.fn().mockResolvedValue(undefined))

vi.mock('@/pages/canvas', () => ({
  default: () => <div data-canvas-test-root />
}))

vi.mock('@/pages/canvas/project', () => ({
  default: () => <div data-canvas-project-test-root />
}))

vi.mock('@sub2api/adapters/use-canvas-store', () => ({
  initializeCanvasProjectStore: vi.fn().mockResolvedValue(undefined),
  resetCanvasProjectStore: vi.fn()
}))

vi.mock('@sub2api/adapters/use-config-store', () => ({
  initializeCanvasConfigStore: initializeConfig,
  resetCanvasConfigStore: vi.fn(),
  useConfigStore: (selector: (state: typeof configState) => unknown) => selector(configState)
}))

vi.mock('@sub2api/adapters/asset-runtime', () => ({
  resetCanvasAssetRuntime: vi.fn()
}))

import { mountCanvas } from './entry'

function hostContext(): CanvasHostContext {
  return {
    apiBaseURL: '/api/v1',
    locale: 'en',
    theme: 'light',
    routeMode: 'user',
    request: async () => undefined as never,
    stream: async () => new ReadableStream<Uint8Array>(),
    navigate: vi.fn(),
    notify: vi.fn()
  }
}

describe('mountCanvas', () => {
  it('updates context and unmounts without leaving a React root', async () => {
    configState.canvasConfigError = ''
    configState.canvasConfig = {
      enabled: true,
      api_keys: [{ id: 1, name: 'Studio key', group_id: 1, group_name: 'images', available: true }],
      policy_version: 1,
      models: []
    }
    const element = document.createElement('div')
    let handle: ReturnType<typeof mountCanvas>

    await act(async () => {
      handle = mountCanvas(element, hostContext())
    })
    expect(element.querySelector('[data-canvas-test-root]')).not.toBeNull()
    expect(element.querySelector('.sub2api-canvas-app')).not.toBeNull()

    await act(async () => {
      handle.updateContext({ ...hostContext(), locale: 'zh-CN', theme: 'dark' })
      handle.unmount()
    })
    expect(element.childElementCount).toBe(0)
    expect(() => handle.unmount()).not.toThrow()
  })

  it('shows the create-key empty state instead of the canvas when the account has no keys', async () => {
    configState.canvasConfigError = ''
    configState.canvasConfig = { enabled: true, api_keys: [], policy_version: 1, models: [] }
    const element = document.createElement('div')
    const context = hostContext()
    let handle: ReturnType<typeof mountCanvas>

    await act(async () => {
      handle = mountCanvas(element, context)
    })

    expect(element.textContent).toContain('Create API key')
    await act(async () => {
      element.querySelector('button')?.click()
    })
    expect(context.navigate).toHaveBeenCalledWith('/studio/credentials?create=1')

    await act(async () => handle.unmount())
  })

  it('shows a management action when every key is unavailable', async () => {
    configState.canvasConfigError = ''
    configState.canvasConfig = {
      enabled: true,
      api_keys: [{ id: 1, name: 'Blocked key', group_id: 1, group_name: 'text', available: false }],
      policy_version: 1,
      models: []
    }
    const element = document.createElement('div')
    const context = hostContext()
    let handle: ReturnType<typeof mountCanvas>

    await act(async () => {
      handle = mountCanvas(element, context)
    })

    expect(element.textContent).toContain('No API key can use Studio')
    await act(async () => element.querySelector('button')?.click())
    expect(context.navigate).toHaveBeenCalledWith('/studio/credentials')

    await act(async () => handle.unmount())
  })

  it('shows a retry action when the initial configuration request fails', async () => {
    initializeConfig.mockClear()
    configState.canvasConfig = undefined
    configState.canvasConfigLoading = false
    configState.canvasConfigError = 'temporary outage'
    const element = document.createElement('div')
    let handle: ReturnType<typeof mountCanvas>

    await act(async () => {
      handle = mountCanvas(element, hostContext())
    })

    expect(element.textContent).toContain('Studio configuration could not be loaded')
    expect(element.textContent).toContain('temporary outage')
    await act(async () => element.querySelector('button')?.click())
    expect(initializeConfig).toHaveBeenCalledTimes(2)

    await act(async () => handle.unmount())
  })
})
