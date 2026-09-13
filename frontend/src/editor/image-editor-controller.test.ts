import { describe, expect, it, vi } from 'vitest'
import type {
  CanvasAPI,
  CanvasAsset,
  CanvasJob,
  ImageEditorDocument,
  ImageEditorDocumentV1
} from '@sub2api/api/canvas-api'
import type { CanvasJobController } from '@sub2api/jobs/canvas-job-controller'
import { createImageEditorController } from './image-editor-controller'

const documentV1 = (selectedRevisionID?: string): ImageEditorDocumentV1 => ({
  schema_version: 1,
  viewport: { zoom: 1, x: 0, y: 0 },
  canvas: { width: 1024, height: 768, background: 'transparent' },
  ...(selectedRevisionID ? { selected_revision_id: selectedRevisionID } : {})
})

const asset = (id: string): CanvasAsset => ({
  id,
  source_type: 'upload',
  media_kind: 'image',
  mime_type: 'image/png',
  width: 1024,
  height: 768,
  byte_size: 100,
  sha256: `${id}-hash`,
  url: `/assets/${id}`
})

function editor(version = 3, value = documentV1()): ImageEditorDocument {
  return {
    id: 'editor-1',
    project_id: 'project-1',
    node_id: 'node-1',
    base_asset: asset('asset-1'),
    current_asset: asset('asset-1'),
    document: value,
    version,
    asset_references: [],
    revisions: [],
    created_at: '2026-08-26T00:00:00Z',
    updated_at: '2026-08-26T00:00:00Z'
  }
}

const hydrateInput = {
  project_id: 'project-1',
  node_id: 'node-1',
  base_asset_id: 'asset-1',
  document: documentV1()
}

function completedJob(status: CanvasJob['status'] = 'completed'): CanvasJob {
  return {
    id: 'job-1',
    status,
    operation: 'edit',
    attempt_plan: ['gpt-image-1'],
    requested_count: 1,
    completed_count: status === 'completed' ? 1 : 0,
    results: status === 'completed'
      ? [{ index: 0, status: 'completed', asset_id: 'asset-2' }]
      : [],
    ...(status === 'failed'
      ? { error: { type: 'upstream', code: 'failed', message: 'Provider failed', retryable: true } }
      : {})
  }
}

describe('createImageEditorController', () => {
  it('hydrates and advances the optimistic version after a save', async () => {
    const initial = editor()
    const savedDocument = documentV1('revision-1')
    const updateEditorDocument = vi.fn().mockResolvedValue(editor(4, savedDocument))
    const api = {
      createEditorDocument: vi.fn().mockResolvedValue(initial),
      updateEditorDocument
    } as unknown as CanvasAPI
    const controller = createImageEditorController(api)

    await controller.hydrate(hydrateInput)
    controller.updateDraft((value) => ({ ...value, selected_revision_id: 'revision-1' }))
    await controller.save()

    expect(updateEditorDocument).toHaveBeenCalledWith('editor-1', {
      version: 3,
      document: savedDocument
    })
    expect(controller.getState()).toMatchObject({
      loadState: 'ready',
      saveState: 'saved',
      document: { version: 4 },
      draft: savedDocument
    })
  })

  it('keeps the local draft and records the newest server copy on a 409', async () => {
    const localDraft = { ...documentV1(), viewport: { zoom: 2, x: 20, y: 30 } }
    const serverCopy = editor(4, { ...documentV1(), viewport: { zoom: 1.5, x: 5, y: 8 } })
    const savedCopy = { ...serverCopy, version: 5, current_asset: asset('asset-2'), document: localDraft }
    const updateEditorDocument = vi.fn()
      .mockRejectedValueOnce({ status: 409, code: 'editor_version_conflict' })
      .mockResolvedValueOnce(savedCopy)
    const api = {
      createEditorDocument: vi.fn().mockResolvedValue(editor()),
      updateEditorDocument,
      getEditorDocument: vi.fn().mockResolvedValue(serverCopy)
    } as unknown as CanvasAPI
    const controller = createImageEditorController(api)
    await controller.hydrate(hydrateInput)
    controller.updateDraft(() => localDraft)

    await expect(controller.save({
      currentAssetID: 'asset-2',
      operation: 'mask_edit',
      parameters: { job_id: 'job-1' }
    })).rejects.toMatchObject({ status: 409 })

    expect(controller.getState()).toMatchObject({
      saveState: 'conflict',
      document: { version: 4 },
      draft: localDraft,
      error: { kind: 'conflict', retryable: true }
    })

    await controller.retrySave()

    expect(updateEditorDocument).toHaveBeenNthCalledWith(2, 'editor-1', expect.objectContaining({
      version: 4,
      current_asset_id: 'asset-2',
      operation: 'mask_edit',
      parameters: { job_id: 'job-1' }
    }))
    expect(controller.getState()).toMatchObject({ saveState: 'saved', document: { version: 5 } })
  })

  it('commits a successful terminal job and leaves the prior revision intact on failure', async () => {
    const updated = { ...editor(4), current_asset: asset('asset-2') }
    const run = vi.fn()
      .mockImplementationOnce(async (_input, _signal, onUpdate) => {
        onUpdate(completedJob('running'))
        return completedJob('completed')
      })
      .mockResolvedValueOnce(completedJob('failed'))
    const jobs = { run } as unknown as CanvasJobController
    const updateEditorDocument = vi.fn().mockResolvedValue(updated)
    const api = {
      createEditorDocument: vi.fn().mockResolvedValue(editor()),
      updateEditorDocument
    } as unknown as CanvasAPI
    const controller = createImageEditorController(api, jobs)
    await controller.hydrate(hydrateInput)
    const input = {
      project_id: 'project-1',
      client_node_id: 'node-1',
      operation: 'edit' as const,
      api_key_id: 7,
      selected_model: 'gpt-image-1',
      prompt: 'Replace the background',
      input_asset_ids: ['asset-1'],
      parameters: { n: 1 }
    }

    await controller.runJob(input, { operation: 'background_replace', parameters: { prompt: input.prompt } })

    expect(updateEditorDocument).toHaveBeenCalledWith('editor-1', expect.objectContaining({
      version: 3,
      current_asset_id: 'asset-2',
      operation: 'background_replace',
      parameters: { prompt: input.prompt, job_id: 'job-1' }
    }))
    expect(controller.getState()).toMatchObject({ jobState: 'completed', document: { current_asset: { id: 'asset-2' } } })

    await controller.runJob(input, { operation: 'background_replace' })

    expect(updateEditorDocument).toHaveBeenCalledTimes(1)
    expect(controller.getState()).toMatchObject({
      jobState: 'failed',
      document: { current_asset: { id: 'asset-2' } },
      error: { message: 'Provider failed', retryable: true }
    })
  })

  it('cancels the server job only through the explicit command', async () => {
    let release!: () => void
    const running = completedJob('running')
    const run = vi.fn((_input, signal: AbortSignal, onUpdate: (job: CanvasJob) => void) => {
      onUpdate(running)
      return new Promise<CanvasJob>((_resolve, reject) => {
        release = () => reject(signal.reason)
        signal.addEventListener('abort', release, { once: true })
      })
    })
    const canceled = completedJob('canceled')
    const cancel = vi.fn().mockResolvedValue(canceled)
    const jobs = { run, cancel } as unknown as CanvasJobController
    const api = { createEditorDocument: vi.fn().mockResolvedValue(editor()) } as unknown as CanvasAPI
    const controller = createImageEditorController(api, jobs)
    await controller.hydrate(hydrateInput)

    const task = controller.runJob({
      project_id: 'project-1',
      client_node_id: 'node-1',
      operation: 'edit',
      api_key_id: 7,
      selected_model: 'gpt-image-1',
      prompt: 'Edit',
      input_asset_ids: ['asset-1'],
      parameters: { n: 1 }
    }, { operation: 'mask_edit' })
    await controller.cancelJob()
    await expect(task).rejects.toBeTruthy()

    expect(cancel).toHaveBeenCalledWith('job-1')
    expect(controller.getState()).toMatchObject({ jobState: 'canceled', error: undefined })
    expect(release).toBeTypeOf('function')
  })
})
