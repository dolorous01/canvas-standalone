import { nanoid } from 'nanoid'

import { audioMimeType } from '@/lib/audio-generation'
import type { AiConfig } from '@/stores/use-config-store'
import {
  getCanvasModelCapability,
  getSelectedCanvasAPIKeyID,
  modelOptionName
} from '@sub2api/adapters/use-config-store'
import {
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
import { uploadMediaFile, type UploadedFile } from '@/services/file-storage'
import {
  createCanvasAPI,
  type CanvasAudioGenerate,
  type CanvasCapability,
  type CanvasMediaTask
} from '@sub2api/api/canvas-api'
import { getCanvasRuntimeHost } from '@sub2api/runtime/host-runtime'

type RequestOptions = { signal?: AbortSignal }
type CanvasGenerationConfig = AiConfig & { __sub2apiClientNodeID?: string }

const generatedAudioFiles = new WeakMap<Blob, UploadedFile>()

export async function requestAudioGeneration(
  config: AiConfig,
  prompt: string,
  options?: RequestOptions
): Promise<Blob> {
  assertNotAborted(options?.signal)
  const projectID = getActiveCanvasProjectID()
  const apiKeyID = getSelectedCanvasAPIKeyID()
  const selectedModel = modelOptionName(config.model || config.audioModel)
  const capability = requireAudioCapability(selectedModel)
  const cleanPrompt = prompt.trim()
  if (!projectID) throw new Error('Open a canvas project before generating audio')
  if (!apiKeyID) throw new Error('Select a Sub2API API key before generating audio')
  if (!cleanPrompt) throw new Error('Enter text before generating audio')

  const clientNodeID = (config as CanvasGenerationConfig).__sub2apiClientNodeID || `audio-${nanoid()}`
  const input: CanvasAudioGenerate = {
    project_id: projectID,
    client_node_id: clientNodeID,
    api_key_id: apiKeyID,
    selected_model: selectedModel,
    prompt: cleanPrompt,
    parameters: audioParameters(config, capability)
  }
  const api = createCanvasAPI(getCanvasRuntimeHost())
  let task = await api.generateAudio(input, `${projectID}:${clientNodeID}:audio:${nanoid()}`, options?.signal)
  registerCanvasTaskRecovery(task, 'audio')
  try {
    task = await waitForAudioTask(task, options?.signal)
    const result = task.results.find((item) => item.asset_id)
    if (!result?.asset_id) throw new Error(task.error?.message || 'Audio generation returned no stored asset')
    const storageKey = assetStorageKey(result.asset_id)
    const storedBlob = await getCanvasAssetBlob(storageKey)
    if (!storedBlob) throw new Error('The generated audio asset is unavailable')
    assertNotAborted(options?.signal)
    const mimeType = result.mime_type || storedBlob.type || audioMimeType(input.parameters.format)
    const blob = storedBlob.type ? storedBlob : new Blob([storedBlob], { type: mimeType })
    if (blob !== storedBlob) cacheCanvasAssetBlob(storageKey, blob)
    generatedAudioFiles.set(blob, {
      url: await resolveCanvasAssetURL(storageKey),
      storageKey,
      bytes: blob.size,
      mimeType
    })
    scheduleCanvasTaskRecoveryCleanup(task.id)
    return blob
  } catch (error) {
    if (options?.signal?.aborted && !isTerminalTask(task)) {
      await api.cancelMediaTask(task.id).catch(() => undefined)
      scheduleCanvasTaskRecoveryCleanup(task.id)
    }
    throw error
  }
}

export function storeGeneratedAudio(blob: Blob, format = 'mp3'): Promise<UploadedFile> {
  const existing = generatedAudioFiles.get(blob)
  if (existing) return Promise.resolve(existing)
  const audio = blob.type.startsWith('audio/') ? blob : new Blob([blob], { type: audioMimeType(format) })
  return uploadMediaFile(audio, 'audio')
}

async function waitForAudioTask(task: CanvasMediaTask, signal?: AbortSignal): Promise<CanvasMediaTask> {
  const api = createCanvasAPI(getCanvasRuntimeHost())
  let current = task
  for (let attempt = 0; attempt < 600; attempt += 1) {
    assertNotAborted(signal)
    if (current.kind !== 'audio') throw new Error('The server returned an invalid audio task')
    if (current.status === 'completed' || current.status === 'partial') return current
    if (isTerminalTask(current)) throw new Error(current.error?.message || `Audio generation ${current.status}`)
    await delay(1000, signal)
    current = await api.getMediaTask(current.id, signal)
  }
  throw new Error('Audio generation timed out')
}

function audioParameters(config: AiConfig, capability: CanvasCapability): CanvasAudioGenerate['parameters'] {
  const voice = allowedValue(config.audioVoice, capability.audio_voices) || capability.audio_voices?.[0] || 'alloy'
  const format = allowedValue(config.audioFormat, capability.audio_formats) || capability.audio_formats?.[0] || 'mp3'
  const minimum = capability.audio_speed_min && capability.audio_speed_min > 0 ? capability.audio_speed_min : 0.25
  const maximum = capability.audio_speed_max && capability.audio_speed_max >= minimum ? capability.audio_speed_max : 4
  const requestedSpeed = Number(config.audioSpeed)
  const speed = Math.max(minimum, Math.min(maximum, Number.isFinite(requestedSpeed) ? requestedSpeed : 1))
  return {
    voice,
    format,
    speed,
    instructions: config.audioInstructions.trim()
  }
}

function requireAudioCapability(model: string): CanvasCapability {
  if (!model) throw new Error('Select an audio model before generating audio')
  const capability = getCanvasModelCapability(model)
  if (!capability || capability.media_kind !== 'audio' || !capability.generation) {
    throw new Error('The selected model does not support audio generation')
  }
  return capability
}

function allowedValue(value: string | undefined, allowed?: string[]): string | undefined {
  if (!value) return undefined
  return allowed?.find((item) => item.toLowerCase() === value.toLowerCase())
}

function isTerminalTask(task: CanvasMediaTask): boolean {
  return ['completed', 'partial', 'failed', 'canceled', 'indeterminate', 'expired'].includes(task.status)
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
