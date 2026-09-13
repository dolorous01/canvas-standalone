import { describe, expect, it, vi } from 'vitest'
import type { CanvasHostContext } from '@sub2api/host-context'
import { createCanvasAPI, parseCanvasSSE } from './canvas-api'

function chunkedStream(...chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder()
  return new ReadableStream({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(encoder.encode(chunk))
      controller.close()
    }
  })
}

describe('parseCanvasSSE', () => {
  it('parses comments, multiline data, and CRLF split across chunks', async () => {
    const stream = chunkedStream(
      ': keepalive\r',
      '\nevent: progress\r\ndata: {"status":"running",\r',
      '\ndata: "completed_count":1}\r\n\r',
      '\nevent: completed\ndata: {"status":"completed"}\n\n'
    )

    const events = []
    for await (const event of parseCanvasSSE(stream)) events.push(event)

    expect(events).toEqual([
      { event: 'progress', data: '{"status":"running",\n"completed_count":1}' },
      { event: 'completed', data: '{"status":"completed"}' }
    ])
  })
})

describe('createCanvasAPI', () => {
  it('creates a job through the host bridge with the idempotency header', async () => {
    const request = vi.fn().mockResolvedValue({ id: 'job-1', status: 'queued' })
    const host = { apiBaseURL: '/canvas-api/v1', request } as unknown as CanvasHostContext
    const api = createCanvasAPI(host)

    await api.createJob({
      project_id: 'project-1',
      client_node_id: 'node-1',
      operation: 'generation',
      api_key_id: 42,
      selected_model: 'gpt-image-1',
      prompt: 'Product image',
      input_asset_ids: [],
      parameters: { n: 1, size: '1024x1024' }
    }, 'project-1:node-1:nonce-1')

    expect(request).toHaveBeenCalledWith(
      'POST',
      '/canvas-api/v1/jobs',
      expect.objectContaining({ api_key_id: 42, selected_model: 'gpt-image-1' }),
      { 'Idempotency-Key': 'project-1:node-1:nonce-1' }
    )
  })

  it('uses the authenticated media task routes and idempotency headers', async () => {
    const request = vi.fn().mockResolvedValue({ id: 'media-1', status: 'queued', results: [] })
    const host = { apiBaseURL: '/canvas-api/v1', request } as unknown as CanvasHostContext
    const api = createCanvasAPI(host)
    const controller = new AbortController()

    await api.createVideoTask({
      project_id: 'project-1',
      client_node_id: 'node-video',
      api_key_id: 42,
      selected_model: 'grok-imagine-video',
      prompt: 'Ocean at sunrise',
      reference_asset_ids: ['asset-1'],
      parameters: {
        seconds: 6,
        size: '16:9',
        resolution: '720p',
        generate_audio: true,
        watermark: false
      }
    }, 'video-key', controller.signal)
    await api.generateAudio({
      project_id: 'project-1',
      client_node_id: 'node-audio',
      api_key_id: 42,
      selected_model: 'gpt-4o-mini-tts',
      prompt: 'Read this',
      parameters: { voice: 'alloy', format: 'mp3', speed: 1, instructions: '' }
    }, 'audio-key', controller.signal)
    await api.getMediaTask('task/id', controller.signal)
    await api.cancelMediaTask('task/id')

    expect(request).toHaveBeenNthCalledWith(
      1,
      'POST',
      '/canvas-api/v1/media/video/tasks',
      expect.objectContaining({ selected_model: 'grok-imagine-video' }),
      { 'Idempotency-Key': 'video-key' },
      { signal: controller.signal }
    )
    expect(request).toHaveBeenNthCalledWith(
      2,
      'POST',
      '/canvas-api/v1/media/audio',
      expect.objectContaining({ selected_model: 'gpt-4o-mini-tts' }),
      { 'Idempotency-Key': 'audio-key' },
      { signal: controller.signal, timeoutMs: 10 * 60_000 }
    )
    expect(request).toHaveBeenNthCalledWith(
      3,
      'GET',
      '/canvas-api/v1/media/tasks/task%2Fid',
      undefined,
      undefined,
      { signal: controller.signal }
    )
    expect(request).toHaveBeenNthCalledWith(4, 'DELETE', '/canvas-api/v1/media/tasks/task%2Fid', undefined, undefined)
  })

  it('uses editor document routes and unwraps response envelopes', async () => {
    const editor = {
      id: 'editor-1',
      project_id: 'project-1',
      node_id: 'node-1',
      base_asset: { id: 'asset-1' },
      current_asset: { id: 'asset-1' },
      document: {
        schema_version: 1,
        viewport: { zoom: 1, x: 0, y: 0 },
        canvas: { width: 1024, height: 768, background: 'transparent' }
      },
      version: 3,
      asset_references: [],
      revisions: []
    }
    const request = vi.fn().mockResolvedValue({ code: 0, message: 'ok', data: editor })
    const host = { apiBaseURL: '/canvas-api/v1', request } as unknown as CanvasHostContext
    const api = createCanvasAPI(host)

    const created = await api.createEditorDocument({
      project_id: 'project-1',
      node_id: 'node-1',
      base_asset_id: 'asset-1',
      document: editor.document as never
    })
    await api.getEditorDocument('editor/id')
    await api.updateEditorDocument('editor/id', {
      version: 3,
      document: editor.document as never,
      current_asset_id: 'asset-2',
      operation: 'crop',
      parameters: { x: 0.1 }
    })

    expect(created).toBe(editor)
    expect(request).toHaveBeenNthCalledWith(1, 'POST', '/canvas-api/v1/editor-documents', expect.objectContaining({ base_asset_id: 'asset-1' }), undefined)
    expect(request).toHaveBeenNthCalledWith(2, 'GET', '/canvas-api/v1/editor-documents/editor%2Fid', undefined, undefined)
    expect(request).toHaveBeenNthCalledWith(3, 'PATCH', '/canvas-api/v1/editor-documents/editor%2Fid', expect.objectContaining({ version: 3, operation: 'crop' }), undefined)
  })

  it('uploads a derived editor asset as multipart data', async () => {
    const request = vi.fn().mockResolvedValue({ id: 'asset-2' })
    const host = { apiBaseURL: '/canvas-api/v1', request } as unknown as CanvasHostContext
    const api = createCanvasAPI(host)
    const file = new File(['pixels'], 'crop.png', { type: 'image/png' })

    await api.uploadEditorDerivedAsset('editor/id', file, 'asset-1')

    expect(request).toHaveBeenCalledWith(
      'POST',
      '/canvas-api/v1/editor-documents/editor%2Fid/assets',
      expect.any(FormData),
      undefined
    )
    const form = request.mock.calls[0][2] as FormData
    expect(form.get('file')).toBe(file)
    expect(form.get('parent_asset_id')).toBe('asset-1')
  })

  it('uses the authenticated library item routes', async () => {
    const item = {
      id: 'library-1', client_id: 'local-1', kind: 'text', title: 'Prompt', content: 'Hello',
      tags: [], metadata: {}, version: 1, created_at: '2026-08-28T00:00:00Z', updated_at: '2026-08-28T00:00:00Z'
    }
    const request = vi.fn()
      .mockResolvedValueOnce({ items: [item] })
      .mockResolvedValueOnce(item)
      .mockResolvedValueOnce({ ...item, version: 2 })
      .mockResolvedValueOnce(undefined)
    const api = createCanvasAPI({ apiBaseURL: '/canvas-api/v1', request } as unknown as CanvasHostContext)

    expect(await api.listLibraryItems()).toEqual([item])
    await api.createLibraryItem({ client_id: 'local-1', kind: 'text', title: 'Prompt', content: 'Hello', tags: [] })
    await api.updateLibraryItem('library/id', { version: 1, kind: 'text', title: 'Prompt 2', content: 'Hello', tags: [] })
    await api.deleteLibraryItem('library/id')

    expect(request).toHaveBeenNthCalledWith(1, 'GET', '/canvas-api/v1/library-items', undefined, undefined)
    expect(request).toHaveBeenNthCalledWith(2, 'POST', '/canvas-api/v1/library-items', expect.objectContaining({ client_id: 'local-1' }), undefined)
    expect(request).toHaveBeenNthCalledWith(3, 'PATCH', '/canvas-api/v1/library-items/library%2Fid', expect.objectContaining({ version: 1 }), undefined)
    expect(request).toHaveBeenNthCalledWith(4, 'DELETE', '/canvas-api/v1/library-items/library%2Fid', undefined, undefined)
  })

  it('requests the authenticated thumbnail stream when needed', async () => {
    const stream = chunkedStream('thumbnail')
    const host = { apiBaseURL: '/canvas-api/v1', request: vi.fn(), stream: vi.fn().mockResolvedValue(stream) } as unknown as CanvasHostContext

    const blob = await createCanvasAPI(host).getAssetBlob('asset/id', undefined, true)

    expect(blob.size).toBe('thumbnail'.length)
    expect(host.stream).toHaveBeenCalledWith('/canvas-api/v1/assets/asset%2Fid?thumbnail=true', { signal: undefined })
  })
})
