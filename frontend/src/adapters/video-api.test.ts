import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AiConfig } from '@/stores/use-config-store'

const mocks = vi.hoisted(() => ({
  api: {
    createVideoTask: vi.fn(),
    getMediaTask: vi.fn(),
    cancelMediaTask: vi.fn()
  },
  uploadImage: vi.fn(),
  uploadMediaFile: vi.fn(),
  getCanvasAssetBlob: vi.fn(),
  resolveCanvasAssetURL: vi.fn(),
  cacheCanvasAssetBlob: vi.fn(),
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
  modelOptionName: (value: string) => value.includes('::') ? value.split('::')[1] : value
}))
vi.mock('@sub2api/adapters/image-storage', () => ({ uploadImage: mocks.uploadImage }))
vi.mock('@/services/file-storage', () => ({ uploadMediaFile: mocks.uploadMediaFile }))
vi.mock('@sub2api/adapters/asset-runtime', () => ({
  assetIDFromStorageKey: (value?: string) => value?.startsWith('asset:') ? value.slice(6) : undefined,
  assetStorageKey: (value: string) => `asset:${value}`,
  cacheCanvasAssetBlob: mocks.cacheCanvasAssetBlob,
  getCanvasAssetBlob: mocks.getCanvasAssetBlob,
  resolveCanvasAssetURL: mocks.resolveCanvasAssetURL
}))

import {
  createVideoGenerationTask,
  pollVideoGenerationTask,
  requestVideoGeneration,
  storeGeneratedVideo
} from './video-api'

function config(overrides: Partial<AiConfig> = {}): AiConfig {
  return {
    model: 'channel::grok-imagine-video',
    videoModel: 'grok-imagine-video',
    videoSeconds: '20',
    size: '1280x720',
    vquality: '720',
    videoGenerateAudio: 'true',
    videoWatermark: 'false',
    ...overrides
  } as AiConfig
}

describe('Sub2API video adapter', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.capability.mockReturnValue({
      media_kind: 'video',
      generation: true,
      max_input_images: 2,
      video_seconds: [6, 10],
      aspect_ratios: ['16:9', '9:16'],
      resolutions: ['480p', '720p'],
      defaults: { aspect_ratio: '16:9', resolution: '720p' }
    })
    mocks.uploadImage.mockResolvedValue({ storageKey: 'asset:uploaded-reference' })
    mocks.api.createVideoTask.mockResolvedValue({ id: 'video-task-1' })
    mocks.api.cancelMediaTask.mockResolvedValue({})
  })

  it('creates a durable task with owned references and server-supported parameters', async () => {
    const task = await createVideoGenerationTask(config() as AiConfig & { __sub2apiClientNodeID?: string }, '  Ocean  ', [
      { id: 'one', name: 'one.png', type: 'image/png', dataUrl: '', storageKey: 'asset:existing' },
      { id: 'two', name: 'two.png', type: 'image/png', dataUrl: 'blob:local' }
    ])

    expect(task).toEqual({ id: 'video-task-1', provider: 'sub2api', model: 'grok-imagine-video' })
    expect(mocks.registerRecovery).toHaveBeenCalledWith({ id: 'video-task-1' }, 'video')
    expect(mocks.uploadImage).toHaveBeenCalledOnce()
    expect(mocks.api.createVideoTask).toHaveBeenCalledWith(
      expect.objectContaining({
        project_id: 'project-1',
        api_key_id: 42,
        selected_model: 'grok-imagine-video',
        prompt: 'Ocean',
        reference_asset_ids: ['existing', 'uploaded-reference'],
        parameters: {
          seconds: 6,
          size: '16:9',
          resolution: '720p',
          generate_audio: true,
          watermark: false
        }
      }),
      expect.stringContaining(':video:'),
      undefined
    )
  })

  it('maps completion to an asset key and reuses the stored video', async () => {
    mocks.api.getMediaTask.mockResolvedValue({
      id: 'video-task-1',
      kind: 'video',
      status: 'completed',
      results: [{ index: 0, asset_id: 'video-asset', mime_type: 'video/mp4' }]
    })
    const state = await pollVideoGenerationTask(config(), { id: 'video-task-1', provider: 'sub2api', model: 'grok-imagine-video' })
    expect(state).toEqual({ status: 'completed', result: { storageKey: 'asset:video-asset', mimeType: 'video/mp4' } })

    const blob = new Blob(['video'])
    mocks.getCanvasAssetBlob.mockResolvedValue(blob)
    mocks.resolveCanvasAssetURL.mockResolvedValue('blob:video')
    const stored = await storeGeneratedVideo(state.status === 'completed' ? state.result : {})
    expect(stored).toMatchObject({ url: 'blob:video', storageKey: 'asset:video-asset', bytes: 5, mimeType: 'video/mp4' })
    expect(mocks.uploadMediaFile).not.toHaveBeenCalled()
    expect(mocks.cacheCanvasAssetBlob).toHaveBeenCalledWith('asset:video-asset', expect.any(Blob))
  })

  it('cancels the durable task when polling is aborted', async () => {
    const controller = new AbortController()
    controller.abort()
    await expect(pollVideoGenerationTask(
      config(),
      { id: 'video-task-1', provider: 'sub2api', model: 'grok-imagine-video' },
      { signal: controller.signal }
    )).rejects.toMatchObject({ name: 'AbortError' })
    expect(mocks.api.cancelMediaTask).toHaveBeenCalledWith('video-task-1')
    expect(mocks.api.getMediaTask).not.toHaveBeenCalled()
  })

  it('retries a temporary polling failure before returning the durable result', async () => {
    vi.useFakeTimers()
    try {
      mocks.api.getMediaTask
        .mockRejectedValueOnce(new Error('temporary network failure'))
        .mockResolvedValueOnce({
          id: 'video-task-1',
          kind: 'video',
          status: 'completed',
          results: [{ index: 0, asset_id: 'video-asset', mime_type: 'video/mp4' }]
        })

      const pending = requestVideoGeneration(config(), 'Ocean')
      await vi.runAllTimersAsync()

      await expect(pending).resolves.toEqual({
        storageKey: 'asset:video-asset',
        mimeType: 'video/mp4'
      })
      expect(mocks.api.getMediaTask).toHaveBeenCalledTimes(2)
      expect(mocks.api.cancelMediaTask).not.toHaveBeenCalled()
    } finally {
      vi.useRealTimers()
    }
  })
})
