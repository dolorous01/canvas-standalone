import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AiConfig } from '@/stores/use-config-store'

const mocks = vi.hoisted(() => ({
  api: {
    generateAudio: vi.fn(),
    getMediaTask: vi.fn(),
    cancelMediaTask: vi.fn()
  },
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
vi.mock('@/services/file-storage', () => ({ uploadMediaFile: mocks.uploadMediaFile }))
vi.mock('@sub2api/adapters/asset-runtime', () => ({
  assetStorageKey: (value: string) => `asset:${value}`,
  cacheCanvasAssetBlob: mocks.cacheCanvasAssetBlob,
  getCanvasAssetBlob: mocks.getCanvasAssetBlob,
  resolveCanvasAssetURL: mocks.resolveCanvasAssetURL
}))

import { requestAudioGeneration, storeGeneratedAudio } from './audio-api'

function config(): AiConfig {
  return {
    model: 'channel::gpt-4o-mini-tts',
    audioModel: 'gpt-4o-mini-tts',
    audioVoice: 'unsupported',
    audioFormat: 'wav',
    audioSpeed: '9',
    audioInstructions: ' Speak softly '
  } as AiConfig
}

describe('Sub2API audio adapter', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.capability.mockReturnValue({
      media_kind: 'audio',
      generation: true,
      audio_voices: ['alloy', 'nova'],
      audio_formats: ['mp3', 'wav'],
      audio_speed_min: 0.5,
      audio_speed_max: 2
    })
    mocks.api.generateAudio.mockResolvedValue({
      id: 'audio-task-1',
      kind: 'audio',
      status: 'completed',
      results: [{ index: 0, asset_id: 'audio-asset', mime_type: 'audio/wav' }]
    })
    mocks.getCanvasAssetBlob.mockResolvedValue(new Blob(['audio']))
    mocks.resolveCanvasAssetURL.mockResolvedValue('blob:audio')
  })

  it('returns the durable asset Blob and does not upload it again', async () => {
    const blob = await requestAudioGeneration(config(), '  Read this  ')
    const stored = await storeGeneratedAudio(blob, 'wav')

    expect(mocks.api.generateAudio).toHaveBeenCalledWith(
      expect.objectContaining({
        project_id: 'project-1',
        api_key_id: 42,
        selected_model: 'gpt-4o-mini-tts',
        prompt: 'Read this',
        parameters: { voice: 'alloy', format: 'wav', speed: 2, instructions: 'Speak softly' }
      }),
      expect.stringContaining(':audio:'),
      undefined
    )
    expect(stored).toEqual({
      url: 'blob:audio',
      storageKey: 'asset:audio-asset',
      bytes: 5,
      mimeType: 'audio/wav'
    })
    expect(mocks.cacheCanvasAssetBlob).toHaveBeenCalledWith('asset:audio-asset', blob)
    expect(mocks.uploadMediaFile).not.toHaveBeenCalled()
    expect(mocks.registerRecovery).toHaveBeenCalledWith(
      expect.objectContaining({ id: 'audio-task-1' }),
      'audio'
    )
    expect(mocks.cleanupRecovery).toHaveBeenCalledWith('audio-task-1')
  })

  it('polls an accepted audio task until the worker stores its result', async () => {
    vi.useFakeTimers()
    try {
      mocks.api.generateAudio.mockResolvedValue({
        id: 'audio-task-1',
        kind: 'audio',
        status: 'queued',
        results: []
      })
      mocks.api.getMediaTask.mockResolvedValue({
        id: 'audio-task-1',
        kind: 'audio',
        status: 'completed',
        results: [{ index: 0, asset_id: 'audio-asset', mime_type: 'audio/wav' }]
      })

      const pending = requestAudioGeneration(config(), 'Read this')
      await vi.advanceTimersByTimeAsync(1000)

      await expect(pending).resolves.toBeInstanceOf(Blob)
      expect(mocks.api.getMediaTask).toHaveBeenCalledWith('audio-task-1', undefined)
    } finally {
      vi.useRealTimers()
    }
  })

  it('rejects a model whose server capability is not audio', async () => {
    mocks.capability.mockReturnValue({ media_kind: 'image', generation: true })
    await expect(requestAudioGeneration(config(), 'Read this')).rejects.toThrow('does not support audio')
    expect(mocks.api.generateAudio).not.toHaveBeenCalled()
  })
})
