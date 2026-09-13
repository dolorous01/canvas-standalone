import { nanoid } from 'nanoid'

import type { ReferenceImage } from '@/types/image'
import type { AiConfig, ModelChannel } from '@/stores/use-config-store'
import {
  getCanvasModelCapability,
  getSelectedCanvasAPIKeyID,
  modelOptionName,
  useConfigStore
} from '@sub2api/adapters/use-config-store'
import { assetIDFromStorageKey, assetStorageKey } from '@sub2api/adapters/asset-runtime'
import {
  getActiveCanvasProjectID,
  registerCanvasTaskRecovery,
  scheduleCanvasTaskRecoveryCleanup
} from '@sub2api/adapters/use-canvas-store'
import { uploadImage } from '@sub2api/adapters/image-storage'
import { createCanvasAPI, type CanvasJob, type CanvasJobCreate, type CanvasModelParameters } from '@sub2api/api/canvas-api'
import { createCanvasJobController } from '@sub2api/jobs/canvas-job-controller'
import { getCanvasRuntimeHost } from '@sub2api/runtime/host-runtime'

export type AiTextMessage = {
  role: 'system' | 'user' | 'assistant'
  content: string | Array<{ type: 'text'; text: string } | { type: 'image_url'; image_url: { url: string } }>
}

type RequestOptions = { signal?: AbortSignal }
type CanvasGenerationConfig = AiConfig & { __sub2apiClientNodeID?: string }

export async function requestGeneration(config: AiConfig, prompt: string, options?: RequestOptions) {
  return runImageJob(config as CanvasGenerationConfig, prompt, 'generation', [], undefined, options)
}

export async function requestEdit(
  config: AiConfig,
  prompt: string,
  references: ReferenceImage[],
  mask?: ReferenceImage,
  options?: RequestOptions
) {
  return runImageJob(config as CanvasGenerationConfig, prompt, 'edit', references, mask, options)
}

export async function requestImageQuestion(
  _config: AiConfig,
  _messages: AiTextMessage[],
  _onDelta: (text: string) => void,
  _options?: RequestOptions
): Promise<string> {
  throw new Error('Server-managed text assistance is not available for this Studio model')
}

export async function fetchImageModels(_config: Pick<AiConfig, 'baseUrl' | 'apiKey' | 'apiFormat'>): Promise<string[]> {
  return useConfigStore.getState().config.channels.flatMap((channel) => channel.models.map((model) => model.name))
}

export function fetchChannelModels(channel: ModelChannel): Promise<string[]> {
  return Promise.resolve(channel.models.map((model) => model.name))
}

async function runImageJob(
  config: CanvasGenerationConfig,
  prompt: string,
  operation: 'generation' | 'edit',
  references: ReferenceImage[],
  mask: ReferenceImage | undefined,
  options?: RequestOptions
) {
  const projectID = getActiveCanvasProjectID()
  const apiKeyID = getSelectedCanvasAPIKeyID()
  const selectedModel = modelOptionName(config.model || config.imageModel)
  if (!projectID) throw new Error('Open a canvas project before generating an image')
  if (!apiKeyID) throw new Error('Select a Sub2API API key before generating an image')
  if (!selectedModel) throw new Error('Select an image model before generating an image')
  const inputAssetIDs = await Promise.all(references.map(ensureReferenceAssetID))
  const maskAssetID = mask ? await ensureReferenceAssetID(mask) : undefined
  const request: CanvasJobCreate = {
    project_id: projectID,
    client_node_id: config.__sub2apiClientNodeID || `generation-${nanoid()}`,
    operation,
    api_key_id: apiKeyID,
    selected_model: selectedModel,
    prompt: withCanvasSystemPrompt(config, prompt),
    input_asset_ids: inputAssetIDs,
    mask_asset_id: maskAssetID,
    parameters: buildCanvasImageParameters(config, selectedModel)
  }
  const canvasAPI = createCanvasAPI(getCanvasRuntimeHost())
  const controller = createCanvasJobController(canvasAPI)
  let latest: CanvasJob | undefined
  let registeredJobID: string | undefined
  try {
    const job = await controller.run(request, options?.signal, (value) => {
      latest = value
      if (!registeredJobID) {
        registeredJobID = value.id
        registerCanvasTaskRecovery(value, 'image')
      }
    })
    if (job.status !== 'completed' && job.status !== 'partial') {
      throw new Error(job.error?.message || `Image generation ${job.status}`)
    }
    const images = job.results
      .filter((result) => result.status === 'completed' && result.asset_id)
      .map((result) => ({ id: result.asset_id!, dataUrl: assetStorageKey(result.asset_id!) }))
    if (!images.length) throw new Error(job.error?.message || 'Image generation returned no result')
    scheduleCanvasTaskRecoveryCleanup(job.id)
    return images
  } catch (error) {
    if (options?.signal?.aborted && latest && !isTerminalJob(latest)) {
      void controller.cancel(latest.id).catch(() => undefined)
    }
    if (latest && isTerminalJob(latest)) scheduleCanvasTaskRecoveryCleanup(latest.id)
    throw error
  }
}

async function ensureReferenceAssetID(reference: ReferenceImage): Promise<string> {
  const existing = assetIDFromStorageKey(reference.storageKey)
  if (existing) return existing
  const uploaded = await uploadImage(reference.dataUrl || reference.url || '')
  const assetID = assetIDFromStorageKey(uploaded.storageKey)
  if (!assetID) throw new Error('Image reference could not be stored')
  return assetID
}

export function buildCanvasImageParameters(config: AiConfig, model: string): CanvasJobCreate['parameters'] {
  const capability = getCanvasModelCapability(model)
  const count = Math.max(1, Math.min(capability?.max_outputs || 4, Math.floor(Math.abs(Number(config.count)) || 1)))
  const parameters: CanvasModelParameters & { n: number } = { n: count }
  const requestedSize = config.size.trim()
  if (capability?.dimension_mode === 'aspect_ratio_resolution') {
    parameters.aspect_ratio = allowedValue(requestedSize, capability.aspect_ratios) || capability.defaults?.aspect_ratio
    const requestedResolution = config.quality === 'high' || config.quality === 'medium' ? '2k' : config.quality === 'low' ? '1k' : ''
    parameters.resolution = allowedValue(requestedResolution, capability.resolutions) || capability.defaults?.resolution
  } else {
    parameters.size = allowedValue(canvasPixelSize(requestedSize), capability?.sizes) || capability?.defaults?.size || '1024x1024'
  }
  parameters.quality = allowedValue(config.quality, capability?.qualities) || capability?.defaults?.quality
  parameters.background = allowedValue(config.background, capability?.backgrounds) || capability?.defaults?.background
  parameters.output_format = allowedValue('png', capability?.output_formats) || capability?.defaults?.output_format || 'png'
  return parameters
}

function canvasPixelSize(size: string): string {
  if (/^\d+x\d+$/i.test(size)) return size
  if (size === '16:9' || size === '3:2' || size === '4:3') return '1536x1024'
  if (size === '9:16' || size === '2:3' || size === '3:4') return '1024x1536'
  return '1024x1024'
}

function allowedValue(value: string | undefined, allowed?: string[]): string | undefined {
  if (!value) return undefined
  return allowed?.some((item) => item.toLowerCase() === value.toLowerCase()) ? value : undefined
}

export function withCanvasSystemPrompt(config: AiConfig, prompt: string): string {
  const systemPrompt = config.systemPrompt.trim()
  return systemPrompt ? `${systemPrompt}\n\n${prompt}` : prompt
}

function isTerminalJob(job: CanvasJob): boolean {
  return ['completed', 'partial', 'failed', 'canceled', 'indeterminate', 'expired'].includes(job.status)
}
