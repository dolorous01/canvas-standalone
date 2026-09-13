import type { CanvasHostContext } from '@sub2api/host-context'

export interface CanvasCapability {
  media_kind?: 'image' | 'video' | 'audio' | 'text' | string
  provider?: 'openai' | 'grok' | string
  dimension_mode?: 'size' | 'aspect_ratio_resolution' | string
  generation: boolean
  edit: boolean
  multi_image: boolean
  mask: boolean
  max_input_images: number
  max_outputs: number
  sizes: string[]
  aspect_ratios?: string[]
  resolutions?: string[]
  qualities?: string[]
  output_formats?: string[]
  backgrounds?: string[]
  output_compression?: boolean
  partial_images?: boolean
  max_partial_images?: number
  custom_size?: {
    min_pixels: number
    max_pixels: number
    max_edge: number
    multiple_of: number
    max_aspect_ratio: number
  }
  experimental_sizes?: string[]
  video_seconds?: number[]
  audio_voices?: string[]
  audio_formats?: string[]
  audio_speed_min?: number
  audio_speed_max?: number
  defaults?: CanvasModelParameters
  presets?: Array<{
    id: string
    label: string
    parameters: CanvasModelParameters
    experimental?: boolean
  }>
}

export interface CanvasModelParameters {
  size?: string
  aspect_ratio?: string
  resolution?: string
  quality?: string
  output_format?: string
  background?: string
  output_compression?: number
}

export interface CanvasAPIKey {
  id: number
  name: string
  group_id: number
  group_name: string
  available?: boolean
  unavailable_reason?: 'key_disabled' | 'key_expired' | 'quota_exhausted' | 'group_missing' | 'group_disabled' | 'image_generation_disabled' | 'no_image_model'
}

export interface CanvasModel {
  model: string
  enabled: boolean
  position: number
  capability: CanvasCapability
}

export interface CanvasConfig {
  enabled: boolean
  api_keys: CanvasAPIKey[]
  selected_api_key_id?: number
  policy_version: number
  models: CanvasModel[]
}

export interface CanvasCredentialCandidate {
  id: number
  name: string
  group_id?: number
  status: string
  quota: number
  quota_used: number
  expires_at?: string
  bound: boolean
  credential_id?: string
  key_hint?: string
}

export interface CanvasCredential {
  id: string
  external_api_key_id: number
  name: string
  key_hint: string
  status: string
  last_verified_at: string
  created_at: string
  updated_at: string
}

export interface CanvasDocument {
  schema_version: 1 | 2
  nodes: CanvasNodeDocument[]
  edges?: CanvasEdgeDocument[]
  connections?: unknown[]
  chat_sessions?: unknown[]
  active_chat_id?: string | null
  background_mode?: string
  show_image_info?: boolean
  viewport?: { x: number; y: number; k: number }
  recoveries?: unknown[]
}

export interface CanvasNodeDocument {
  id: string
  type: 'image' | 'video' | 'audio' | 'text' | 'config' | 'group' | (string & {})
  position: { x: number; y: number }
  size?: { width: number; height: number }
  prompt?: string
  text?: string
  asset_id?: string
  metadata?: Record<string, unknown>
}

export interface CanvasEdgeDocument {
  id: string
  source: string
  target: string
}

export interface CanvasProject {
  id: string
  name: string
  document: CanvasDocument
  version: number
  created_at: string
  updated_at: string
  open_jobs?: CanvasJob[]
  media_tasks?: CanvasMediaTask[]
}

export interface CanvasAsset {
  id: string
  source_type: string
  media_kind: 'image' | 'video' | 'audio'
  file_name?: string
  mime_type: string
  width: number
  height: number
  duration_ms?: number
  byte_size: number
  sha256: string
  url: string
  thumbnail_url?: string
}

export type CanvasLibraryItemKind = 'text' | 'image' | 'video' | 'audio'

export interface CanvasLibraryItem {
  id: string
  client_id: string
  kind: CanvasLibraryItemKind
  asset_id?: string
  asset_url?: string
  thumbnail_url?: string
  title: string
  content?: string
  tags: string[]
  source?: string
  note?: string
  metadata: Record<string, unknown>
  version: number
  created_at: string
  updated_at: string
}

export interface CanvasLibraryItemWrite {
  client_id?: string
  version?: number
  kind: CanvasLibraryItemKind
  asset_id?: string
  title: string
  content?: string
  tags: string[]
  source?: string
  note?: string
  metadata?: Record<string, unknown>
}

export interface ImageEditorDocumentV1 {
  schema_version: 1
  viewport: { zoom: number; x: number; y: number }
  canvas: {
    width: number
    height: number
    background: 'transparent' | 'white' | 'black'
  }
  selected_revision_id?: string
}

export interface ImageEditorAssetReference {
  asset_id: string
  role: 'source' | 'layer' | 'mask' | 'result'
  element_id: string
}

export type ImageEditorOperation = 'crop' | 'mask_edit' | 'background_replace' | 'outpaint' | 'revision_select'

export interface ImageEditorRevision {
  id: string
  version: number
  asset: CanvasAsset
  operation: ImageEditorOperation
  parameters: Record<string, unknown>
  created_at: string
}

export interface ImageEditorDocument {
  id: string
  project_id: string
  node_id: string
  base_asset: CanvasAsset
  current_asset: CanvasAsset
  document: ImageEditorDocumentV1
  version: number
  asset_references: ImageEditorAssetReference[]
  revisions: ImageEditorRevision[]
  created_at: string
  updated_at: string
}

export interface ImageEditorDocumentCreate {
  project_id: string
  node_id: string
  base_asset_id: string
  document: ImageEditorDocumentV1
  asset_references?: ImageEditorAssetReference[]
}

export interface ImageEditorDocumentUpdate {
  version: number
  document: ImageEditorDocumentV1
  current_asset_id?: string
  operation?: ImageEditorOperation
  parameters?: Record<string, unknown>
  asset_references?: ImageEditorAssetReference[]
}

export type CanvasJobStatus =
  | 'queued'
  | 'running'
  | 'partial'
  | 'completed'
  | 'failed'
  | 'canceled'
  | 'indeterminate'
  | 'expired'

export interface CanvasJobResult {
  index: number
  status: string
  asset_id?: string
  url?: string
  mime_type?: string
  size?: string
}

export interface CanvasJob {
  id: string
  status: CanvasJobStatus
  phase?: 'preflight' | 'upstream' | 'falling_back' | 'saving' | string
  operation: 'generation' | 'edit'
  client_node_id?: string
  selected_model?: string
  successful_model?: string
  policy_version?: number
  attempt_plan: string[]
  attempt_position?: number
  requested_count: number
  completed_count: number
  results: CanvasJobResult[]
  error?: { type: string; code: string; message: string; retryable: boolean }
  created_at?: number
  updated_at?: number
}

export interface CanvasJobCreate {
  project_id: string
  client_node_id: string
  operation: 'generation' | 'edit'
  api_key_id: number
  selected_model: string
  prompt: string
  input_asset_ids: string[]
  mask_asset_id?: string
  parameters: {
    size?: string
    aspect_ratio?: string
    resolution?: string
    n: number
    quality?: string
    output_format?: string
    background?: string
    output_compression?: number
  }
}

export interface CanvasMediaTaskResult {
  index: number
  asset_id?: string
  url?: string
  mime_type?: string
}

export interface CanvasMediaTask {
  id: string
  kind: 'video' | 'audio'
  status: CanvasJobStatus
  phase?: string
  project_id: string
  client_node_id: string
  selected_model: string
  successful_model?: string
  results: CanvasMediaTaskResult[]
  error?: { code: string; message: string; retryable: boolean }
  created_at?: number
  updated_at?: number
}

export interface CanvasVideoTaskCreate {
  project_id: string
  client_node_id: string
  api_key_id: number
  selected_model: string
  prompt: string
  reference_asset_ids: string[]
  parameters: {
    seconds: number
    size: string
    resolution: string
    generate_audio: boolean
    watermark: boolean
  }
}

export interface CanvasAudioGenerate {
  project_id: string
  client_node_id: string
  api_key_id: number
  selected_model: string
  prompt: string
  parameters: {
    voice: string
    format: string
    speed: number
    instructions: string
  }
}

export interface CanvasAPI {
  listCredentialCandidates(): Promise<CanvasCredentialCandidate[]>
  bindCredential(externalAPIKeyID: number): Promise<CanvasCredential>
  deleteCredential(id: string): Promise<void>
  getConfig(apiKeyID?: number): Promise<CanvasConfig>
  listProjects(): Promise<CanvasProject[]>
  createProject(name: string, document: CanvasDocument, id?: string): Promise<CanvasProject>
  getProject(id: string): Promise<CanvasProject>
  updateProject(id: string, version: number, name: string, document: CanvasDocument): Promise<CanvasProject>
  deleteProject(id: string): Promise<void>
  uploadAsset(file: File, projectID?: string, metadata?: { width?: number; height?: number; durationMs?: number }): Promise<CanvasAsset>
  listLibraryItems(): Promise<CanvasLibraryItem[]>
  createLibraryItem(input: CanvasLibraryItemWrite): Promise<CanvasLibraryItem>
  updateLibraryItem(id: string, input: CanvasLibraryItemWrite): Promise<CanvasLibraryItem>
  deleteLibraryItem(id: string): Promise<void>
  createJob(input: CanvasJobCreate, idempotencyKey: string): Promise<CanvasJob>
  getJob(id: string): Promise<CanvasJob>
  cancelJob(id: string): Promise<CanvasJob>
  streamJob(id: string, signal?: AbortSignal): AsyncGenerator<CanvasJob>
  createVideoTask(input: CanvasVideoTaskCreate, idempotencyKey: string, signal?: AbortSignal): Promise<CanvasMediaTask>
  generateAudio(input: CanvasAudioGenerate, idempotencyKey: string, signal?: AbortSignal): Promise<CanvasMediaTask>
  getMediaTask(id: string, signal?: AbortSignal): Promise<CanvasMediaTask>
  cancelMediaTask(id: string): Promise<CanvasMediaTask>
  getAssetBlob(id: string, signal?: AbortSignal, thumbnail?: boolean): Promise<Blob>
  createEditorDocument(input: ImageEditorDocumentCreate): Promise<ImageEditorDocument>
  getEditorDocument(id: string): Promise<ImageEditorDocument>
  updateEditorDocument(id: string, input: ImageEditorDocumentUpdate): Promise<ImageEditorDocument>
  uploadEditorDerivedAsset(id: string, file: File, parentAssetID: string): Promise<CanvasAsset>
}

type APIEnvelope<T> = { code: number; message: string; data: T }

function unwrap<T>(value: T | APIEnvelope<T>): T {
  if (value && typeof value === 'object' && 'data' in value && 'code' in value) {
    return (value as APIEnvelope<T>).data
  }
  return value as T
}

export function createCanvasAPI(host: CanvasHostContext): CanvasAPI {
  const baseURL = host.apiBaseURL?.startsWith('/canvas-api')
    ? host.apiBaseURL
    : '/image-canvas'
  const request = async <T>(
    method: string,
    path: string,
    body?: unknown,
    headers?: Record<string, string>,
    options?: { signal?: AbortSignal; timeoutMs?: number }
  ) => unwrap(await (options
    ? host.request<T | APIEnvelope<T>>(method, `${baseURL}${path}`, body, headers, options)
    : host.request<T | APIEnvelope<T>>(method, `${baseURL}${path}`, body, headers)))

  return {
    async listCredentialCandidates() {
      const result = await request<{ items: CanvasCredentialCandidate[] }>('GET', '/credentials/candidates')
      return result.items
    },
    bindCredential: (externalAPIKeyID) => request('POST', '/credentials', { external_api_key_id: externalAPIKeyID }),
    deleteCredential: (id) => request('DELETE', `/credentials/${encodeURIComponent(id)}`),
    getConfig: (apiKeyID) => request('GET', `/config${apiKeyID ? `?api_key_id=${apiKeyID}` : ''}`),
    async listProjects() {
      const result = await request<{ items: CanvasProject[] }>('GET', '/projects')
      return result.items
    },
    createProject: (name, document, id) => request('POST', '/projects', { id, name, document }),
    getProject: (id) => request('GET', `/projects/${encodeURIComponent(id)}`),
    updateProject: (id, version, name, document) =>
      request('PATCH', `/projects/${encodeURIComponent(id)}`, { version, name, document }),
    deleteProject: (id) => request('DELETE', `/projects/${encodeURIComponent(id)}`),
    async uploadAsset(file, projectID, metadata) {
      const form = new FormData()
      form.append('file', file)
      if (projectID) form.append('project_id', projectID)
      if (metadata?.width) form.append('width', String(metadata.width))
      if (metadata?.height) form.append('height', String(metadata.height))
      if (metadata?.durationMs) form.append('duration_ms', String(metadata.durationMs))
      return request('POST', '/assets', form)
    },
    async listLibraryItems() {
      const result = await request<{ items: CanvasLibraryItem[] }>('GET', '/library-items')
      return result.items
    },
    createLibraryItem: (input) => request('POST', '/library-items', input),
    updateLibraryItem: (id, input) => request('PATCH', `/library-items/${encodeURIComponent(id)}`, input),
    deleteLibraryItem: (id) => request('DELETE', `/library-items/${encodeURIComponent(id)}`),
    createJob: (input, key) => request('POST', '/jobs', input, { 'Idempotency-Key': key }),
    getJob: (id) => request('GET', `/jobs/${encodeURIComponent(id)}`),
    cancelJob: (id) => request('DELETE', `/jobs/${encodeURIComponent(id)}`),
    async *streamJob(id, signal) {
      const stream = await host.stream(`${baseURL}/jobs/${encodeURIComponent(id)}/events`, { signal })
      for await (const event of parseCanvasSSE(stream)) {
        if (event.data) yield JSON.parse(event.data) as CanvasJob
      }
    },
    createVideoTask: (input, key, signal) => request('POST', '/media/video/tasks', input, { 'Idempotency-Key': key }, { signal }),
    generateAudio: (input, key, signal) => request('POST', '/media/audio', input, { 'Idempotency-Key': key }, { signal, timeoutMs: 10 * 60_000 }),
    getMediaTask: (id, signal) => request('GET', `/media/tasks/${encodeURIComponent(id)}`, undefined, undefined, { signal }),
    cancelMediaTask: (id) => request('DELETE', `/media/tasks/${encodeURIComponent(id)}`),
    async getAssetBlob(id, signal, thumbnail = false) {
      const suffix = thumbnail ? '?thumbnail=true' : ''
      const stream = await host.stream(`${baseURL}/assets/${encodeURIComponent(id)}${suffix}`, { signal })
      return new Response(stream).blob()
    },
    createEditorDocument: (input) => request('POST', '/editor-documents', input),
    getEditorDocument: (id) => request('GET', `/editor-documents/${encodeURIComponent(id)}`),
    updateEditorDocument: (id, input) => request('PATCH', `/editor-documents/${encodeURIComponent(id)}`, input),
    async uploadEditorDerivedAsset(id, file, parentAssetID) {
      const form = new FormData()
      form.append('file', file)
      form.append('parent_asset_id', parentAssetID)
      return request('POST', `/editor-documents/${encodeURIComponent(id)}/assets`, form)
    }
  }
}

export async function* parseCanvasSSE(
  stream: ReadableStream<Uint8Array>
): AsyncGenerator<{ event: string; data: string }> {
  const reader = stream.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  try {
    while (true) {
      const { value, done } = await reader.read()
      buffer += decoder.decode(value, { stream: !done })
      const pendingCarriageReturn = !done && buffer.endsWith('\r')
      const normalizable = pendingCarriageReturn ? buffer.slice(0, -1) : buffer
      buffer = normalizable.replace(/\r\n?|\n/g, '\n') + (pendingCarriageReturn ? '\r' : '')
      let boundary = buffer.indexOf('\n\n')
      while (boundary >= 0) {
        const block = buffer.slice(0, boundary)
        buffer = buffer.slice(boundary + 2)
        let event = 'message'
        const data: string[] = []
        for (const line of block.split('\n')) {
          if (!line || line.startsWith(':')) continue
          if (line.startsWith('event:')) event = line.slice(6).trim()
          if (line.startsWith('data:')) data.push(line.slice(5).trimStart())
        }
        if (data.length) yield { event, data: data.join('\n') }
        boundary = buffer.indexOf('\n\n')
      }
      if (done) break
    }
  } finally {
    reader.releaseLock()
  }
}
