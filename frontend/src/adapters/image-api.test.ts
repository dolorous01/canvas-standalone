import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AiConfig } from '@/stores/use-config-store'

const mocks = vi.hoisted(() => ({
  api: {
    createJob: vi.fn(),
    streamJob: vi.fn()
  },
  capability: vi.fn(),
  registerRecovery: vi.fn(),
  cleanupRecovery: vi.fn()
}))

vi.mock('nanoid', () => ({ nanoid: () => 'nonce-1' }))
vi.mock('@sub2api/runtime/host-runtime', () => ({ getCanvasRuntimeHost: () => ({}) }))
vi.mock('@sub2api/api/canvas-api', () => ({ createCanvasAPI: () => mocks.api }))
vi.mock('@sub2api/adapters/use-canvas-store', () => ({
  getActiveCanvasProjectID: () => 'project-1',
  registerCanvasTaskRecovery: mocks.registerRecovery,
  scheduleCanvasTaskRecoveryCleanup: mocks.cleanupRecovery
}))
vi.mock('@sub2api/adapters/use-config-store', () => ({
  getCanvasModelCapability: mocks.capability,
  getSelectedCanvasAPIKeyID: () => 42,
  modelOptionName: (value: string) => value.includes('::') ? value.split('::')[1] : value,
  useConfigStore: { getState: () => ({ config: { channels: [] } }) }
}))
vi.mock('@sub2api/adapters/asset-runtime', () => ({
  assetIDFromStorageKey: (value?: string) => value?.startsWith('asset:') ? value.slice(6) : undefined,
  assetStorageKey: (value: string) => `asset:${value}`
}))
vi.mock('@sub2api/adapters/image-storage', () => ({
  uploadImage: vi.fn()
}))

import { requestGeneration } from './image-api'

function config(): AiConfig {
  return {
    model: 'channel::gpt-image-1',
    imageModel: 'gpt-image-1',
    count: '1',
    size: '1024x1024',
    quality: 'auto',
    background: 'auto',
    systemPrompt: ''
  } as AiConfig
}

describe('Sub2API image adapter', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.capability.mockReturnValue({
      media_kind: 'image',
      generation: true,
      max_outputs: 1,
      dimension_mode: 'size',
      sizes: ['1024x1024'],
      output_formats: ['png']
    })
    mocks.api.createJob.mockResolvedValue({
      id: 'image-job-1',
      status: 'queued',
      operation: 'generation',
      client_node_id: 'node-1',
      attempt_plan: ['gpt-image-1'],
      requested_count: 1,
      completed_count: 0,
      results: []
    })
    mocks.api.streamJob.mockImplementation(async function* () {
      yield {
        id: 'image-job-1',
        status: 'completed',
        operation: 'generation',
        client_node_id: 'node-1',
        attempt_plan: ['gpt-image-1'],
        requested_count: 1,
        completed_count: 1,
        results: [{ index: 0, status: 'completed', asset_id: 'image-asset', mime_type: 'image/png' }]
      }
    })
  })

  it('records the durable job before returning its stored asset', async () => {
    const result = await requestGeneration({ ...config(), __sub2apiClientNodeID: 'node-1' } as AiConfig, 'A quiet dashboard')

    expect(result).toEqual([{ id: 'image-asset', dataUrl: 'asset:image-asset' }])
    expect(mocks.registerRecovery).toHaveBeenCalledWith(
      expect.objectContaining({ id: 'image-job-1', status: 'queued' }),
      'image'
    )
    expect(mocks.cleanupRecovery).toHaveBeenCalledWith('image-job-1')
  })
})
