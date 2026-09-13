import { nanoid } from 'nanoid'

import type { ReferenceImage } from '@/types/image'
import type { AiConfig } from '@/stores/use-config-store'
import {
  getCanvasModelCapability,
  getSelectedCanvasAPIKeyID,
  modelOptionName
} from '@sub2api/adapters/use-config-store'
import {
  assetIDFromStorageKey,
  assetStorageKey,
  cacheCanvasAssetBlob,
  getCanvasAssetBlob,
  resolveCanvasAssetURL
} from '@sub2api/adapters/asset-runtime'
import {
  getActiveCanvasProjectID,
  registerCanvasTaskRecovery,
  scheduleCanvasTaskRecoveryCleanup
} from '@sub2api/adapters/use-canvas-store'
import { uploadImage } from '@sub2api/adapters/image-storage'
import { uploadMediaFile, type UploadedFile } from '@/services/file-storage'
import {
  createCanvasAPI,
  type CanvasCapability,
  type CanvasMediaTask,
  type CanvasVideoTaskCreate
} from '@sub2api/api/canvas-api'
import { getCanvasRuntimeHost } from '@sub2api/runtime/host-runtime'

type RequestOptions = { signal?: AbortSignal }
type CanvasGenerationConfig = AiConfig & { __sub2apiClientNodeID?: string }

const videoPollIntervalMs = 2500
const videoPollMaxBackoffMs = 10_000
const videoPollDeadlineMs = 31 * 60_000

export type VideoGenerationResult = {
  blob?: Blob
  url?: string
  mimeType?: string
  storageKey?: string
}

export type VideoGenerationTask = {
  id: string
  provider: 'sub2api'
  model: string
}

export type VideoGenerationTaskState =
  | { status: 'pending' }
  | { status: 'completed'; result: VideoGenerationResult }
  | { status: 'failed'; error: string }

export async function requestVideoGeneration(
  config: AiConfig,
  prompt: string,
  references: ReferenceImage[] = [],
  options?: RequestOptions
): Promise<VideoGenerationResult> {
  const task = await createVideoGenerationTask(config, prompt, references, options)
  const deadline = Date.now() + videoPollDeadlineMs
  let pollDelay = videoPollIntervalMs
  try {
    while (Date.now() < deadline) {
      let state: VideoGenerationTaskState
      try {
        state = await pollVideoGenerationTask(config, task, options)
        pollDelay = videoPollIntervalMs
      } catch (error) {
        if (options?.signal?.aborted) throw error
        pollDelay = Math.min(videoPollMaxBackoffMs, pollDelay * 2)
        await delay(Math.min(pollDelay, Math.max(1, deadline - Date.now())), options?.signal)
        continue
      }
      if (state.status === 'completed') {
        scheduleCanvasTaskRecoveryCleanup(task.id)
        return state.result
      }
      if (state.status === 'failed') {
        scheduleCanvasTaskRecoveryCleanup(task.id)
        throw new Error(state.error)
      }
      await delay(Math.min(pollDelay, Math.max(1, deadline - Date.now())), options?.signal)
    }
    await cancelVideoTask(task)
    scheduleCanvasTaskRecoveryCleanup(task.id)
    throw new Error('Video generation timed out')
  } catch (error) {
    if (options?.signal?.aborted) {
      await cancelVideoTask(task)
      scheduleCanvasTaskRecoveryCleanup(task.id)
    }
    throw error
  }
}

export async function createVideoGenerationTask(
  config: AiConfig,
  prompt: string,
  references: ReferenceImage[] = [],
  options?: RequestOptions
): Promise<VideoGenerationTask> {
  assertNotAborted(options?.signal)
  const projectID = getActiveCanvasProjectID()
  const apiKeyID = getSelectedCanvasAPIKeyID()
  const selectedModel = modelOptionName(config.model || config.videoModel)
  const capability = requireCapability(selectedModel, 'video')
  const cleanPrompt = prompt.trim()
  if (!projectID) throw new Error('Open a canvas project before generating a video')
  if (!apiKeyID) throw new Error('Select a Sub2API API key before generating a video')
  if (!cleanPrompt) throw new Error('Enter a prompt before generating a video')
  if (references.length > capability.max_input_images) {
    throw new Error(`This video model accepts at most ${capability.max_input_images} reference image(s)`)
  }

  const referenceAssetIDs = await Promise.all(references.map(ensureReferenceAssetID))
  assertNotAborted(options?.signal)
  const clientNodeID = (config as CanvasGenerationConfig).__sub2apiClientNodeID || `video-${nanoid()}`
  const input: CanvasVideoTaskCreate = {
    project_id: projectID,
    client_node_id: clientNodeID,
    api_key_id: apiKeyID,
    selected_model: selectedModel,
    prompt: cleanPrompt,
    reference_asset_ids: referenceAssetIDs,
    parameters: videoParameters(config, capability)
  }
  const api = createCanvasAPI(getCanvasRuntimeHost())
  const task = await api.createVideoTask(input, `${projectID}:${clientNodeID}:video:${nanoid()}`, options?.signal)
  if (options?.signal?.aborted) {
    await api.cancelMediaTask(task.id).catch(() => undefined)
    throw abortError()
  }
  registerCanvasTaskRecovery(task, 'video')
  return { id: task.id, provider: 'sub2api', model: selectedModel }
}

export async function pollVideoGenerationTask(
  _config: AiConfig,
  task: VideoGenerationTask,
  options?: RequestOptions
): Promise<VideoGenerationTaskState> {
  if (options?.signal?.aborted) {
    await cancelVideoTask(task)
    throw abortError()
  }
  let value: CanvasMediaTask
  try {
    value = await createCanvasAPI(getCanvasRuntimeHost()).getMediaTask(task.id, options?.signal)
  } catch (error) {
    if (options?.signal?.aborted) await cancelVideoTask(task)
    throw error
  }
  if (options?.signal?.aborted) {
    await cancelVideoTask(task)
    throw abortError()
  }
  return videoTaskState(value)
}

export async function storeGeneratedVideo(result: VideoGenerationResult): Promise<UploadedFile> {
  const assetID = assetIDFromStorageKey(result.storageKey)
  if (assetID) {
    const storageKey = assetStorageKey(assetID)
    const storedBlob = await getCanvasAssetBlob(storageKey)
    if (!storedBlob) throw new Error('The generated video asset is unavailable')
    const blob = storedBlob.type ? storedBlob : new Blob([storedBlob], { type: result.mimeType || 'video/mp4' })
    if (blob !== storedBlob) cacheCanvasAssetBlob(storageKey, blob)
    return {
      url: await resolveCanvasAssetURL(storageKey, result.url),
      storageKey,
      bytes: blob.size,
      mimeType: result.mimeType || blob.type || 'video/mp4'
    }
  }
  if (result.blob) return uploadMediaFile(result.blob, 'video')
  if (result.url) return uploadMediaFile(result.url, 'video')
  throw new Error('Video generation returned no playable result')
}

function videoTaskState(task: CanvasMediaTask): VideoGenerationTaskState {
  if (task.kind !== 'video') return { status: 'failed', error: 'The server returned an invalid video task' }
  if (task.status === 'completed' || task.status === 'partial') {
    const result = task.results.find((item) => item.asset_id)
    if (!result?.asset_id) {
      return { status: 'failed', error: task.error?.message || 'Video generation returned no stored asset' }
    }
    return {
      status: 'completed',
      result: {
        storageKey: assetStorageKey(result.asset_id),
        mimeType: result.mime_type || 'video/mp4'
      }
    }
  }
  if (['failed', 'canceled', 'indeterminate', 'expired'].includes(task.status)) {
    return { status: 'failed', error: task.error?.message || `Video generation ${task.status}` }
  }
  return { status: 'pending' }
}

function videoParameters(config: AiConfig, capability: CanvasCapability): CanvasVideoTaskCreate['parameters'] {
  const requestedSeconds = Math.floor(Number(config.videoSeconds) || 0)
  const allowedSeconds = capability.video_seconds || []
  const seconds = allowedSeconds.includes(requestedSeconds)
    ? requestedSeconds
    : allowedSeconds[0] || Math.max(1, Math.min(20, requestedSeconds || 6))
  const requestedRatio = videoAspectRatio(config.size)
  const size = allowedValue(requestedRatio, capability.aspect_ratios) ||
    allowedValue(capability.defaults?.aspect_ratio, capability.aspect_ratios) ||
    capability.aspect_ratios?.[0] || requestedRatio || '16:9'
  const requestedResolution = videoResolution(config.vquality)
  const resolution = allowedValue(requestedResolution, capability.resolutions) ||
    allowedValue(capability.defaults?.resolution, capability.resolutions) ||
    capability.resolutions?.[0] || requestedResolution
  return {
    seconds,
    size,
    resolution,
    generate_audio: config.videoGenerateAudio !== 'false',
    watermark: config.videoWatermark === 'true'
  }
}

async function ensureReferenceAssetID(reference: ReferenceImage): Promise<string> {
  const existing = assetIDFromStorageKey(reference.storageKey)
  if (existing) return existing
  const uploaded = await uploadImage(reference.dataUrl || reference.url || '')
  const assetID = assetIDFromStorageKey(uploaded.storageKey)
  if (!assetID) throw new Error('Video reference image could not be stored')
  return assetID
}

function requireCapability(model: string, kind: 'video'): CanvasCapability {
  if (!model) throw new Error('Select a video model before generating a video')
  const capability = getCanvasModelCapability(model)
  if (!capability || capability.media_kind !== kind || !capability.generation) {
    throw new Error(`The selected model does not support ${kind} generation`)
  }
  return capability
}

function videoAspectRatio(value: string): string {
  if (/^\d+:\d+$/.test(value)) return value
  const match = value.match(/^(\d+)x(\d+)$/i)
  if (!match) return ''
  const width = Number(match[1])
  const height = Number(match[2])
  if (!width || !height) return ''
  const divisor = greatestCommonDivisor(width, height)
  return `${width / divisor}:${height / divisor}`
}

function greatestCommonDivisor(left: number, right: number): number {
  while (right) [left, right] = [right, left % right]
  return left
}

function videoResolution(value: string): string {
  const normalized = value.trim().toLowerCase()
  if (normalized === 'low') return '480p'
  if (['auto', 'medium', 'high'].includes(normalized)) return '720p'
  const numeric = normalized.replace(/p$/, '')
  return /^\d+$/.test(numeric) ? `${numeric}p` : '720p'
}

function allowedValue(value: string | undefined, allowed?: string[]): string | undefined {
  if (!value) return undefined
  return allowed?.find((item) => item.toLowerCase() === value.toLowerCase())
}

async function cancelVideoTask(task: VideoGenerationTask): Promise<void> {
  await createCanvasAPI(getCanvasRuntimeHost()).cancelMediaTask(task.id).catch(() => undefined)
}

function delay(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(abortError())
      return
    }
    const onAbort = () => {
      clearTimeout(timer)
      reject(abortError())
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

function assertNotAborted(signal?: AbortSignal): void {
  if (signal?.aborted) throw abortError()
}

function abortError(): DOMException {
  return new DOMException('Aborted', 'AbortError')
}
