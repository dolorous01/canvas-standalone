import type {
  CanvasAPI,
  CanvasAsset,
  CanvasJob,
  ImageEditorAssetReference,
  ImageEditorDocument,
  ImageEditorDocumentCreate,
  ImageEditorDocumentV1,
  ImageEditorOperation
} from '@sub2api/api/canvas-api'
import {
  createCanvasJobController,
  type CanvasGeneration,
  type CanvasJobController
} from '@sub2api/jobs/canvas-job-controller'

export type ImageEditorLoadState = 'idle' | 'hydrating' | 'ready' | 'error'
export type ImageEditorSaveState = 'idle' | 'saving' | 'saved' | 'conflict' | 'error'
export type ImageEditorJobState = 'idle' | 'running' | 'completed' | 'failed' | 'canceled'

export interface ImageEditorControllerError {
  kind: 'hydrate' | 'save' | 'conflict' | 'job' | 'cancel'
  message: string
  retryable: boolean
}

export interface ImageEditorControllerState {
  loadState: ImageEditorLoadState
  saveState: ImageEditorSaveState
  jobState: ImageEditorJobState
  document?: ImageEditorDocument
  draft?: ImageEditorDocumentV1
  job?: CanvasJob
  error?: ImageEditorControllerError
}

export interface ImageEditorSaveOptions {
  currentAssetID?: string
  operation?: ImageEditorOperation
  parameters?: Record<string, unknown>
  assetReferences?: ImageEditorAssetReference[]
}

export interface ImageEditorJobCompletion {
  operation: Extract<ImageEditorOperation, 'mask_edit' | 'background_replace' | 'outpaint'>
  parameters?: Record<string, unknown>
  assetReferences?: ImageEditorAssetReference[]
  canvas?: { width: number; height: number }
}

export interface ImageEditorController {
  getState(): ImageEditorControllerState
  subscribe(listener: () => void): () => void
  hydrate(input: ImageEditorDocumentCreate): Promise<ImageEditorDocument>
  reload(): Promise<ImageEditorDocument>
  updateDraft(update: (document: ImageEditorDocumentV1) => ImageEditorDocumentV1): void
  save(options?: ImageEditorSaveOptions): Promise<ImageEditorDocument>
  retrySave(): Promise<ImageEditorDocument>
  selectRevision(revisionID: string): Promise<ImageEditorDocument>
  commitDerivedAsset(
    file: File,
    parentAssetID: string,
    operation: Extract<ImageEditorOperation, 'crop' | 'outpaint'>,
    parameters?: Record<string, unknown>
  ): Promise<{ asset: CanvasAsset; document: ImageEditorDocument }>
  runJob(input: CanvasGeneration, completion: ImageEditorJobCompletion): Promise<CanvasJob>
  resumeJob(jobID: string, completion: ImageEditorJobCompletion): Promise<CanvasJob>
  cancelJob(): Promise<CanvasJob | undefined>
  clearError(): void
  destroy(): void
}

export function createImageEditorController(
  api: CanvasAPI,
  jobs: CanvasJobController = createCanvasJobController(api)
): ImageEditorController {
  let state: ImageEditorControllerState = {
    loadState: 'idle',
    saveState: 'idle',
    jobState: 'idle'
  }
  let destroyed = false
  let jobAborter: AbortController | undefined
  let explicitCancellation = false
  let retainedSaveOptions: ImageEditorSaveOptions | undefined
  const listeners = new Set<() => void>()
  const emit = () => listeners.forEach((listener) => listener())
  const setState = (patch: Partial<ImageEditorControllerState>) => {
    if (destroyed) return
    state = { ...state, ...patch }
    emit()
  }

  const requireDocument = () => {
    if (!state.document || !state.draft) throw new Error('Image editor is not hydrated')
    return { document: state.document, draft: state.draft }
  }

  const save = async (options: ImageEditorSaveOptions = {}): Promise<ImageEditorDocument> => {
    const current = requireDocument()
    const savingDraft = options.currentAssetID && options.operation !== 'revision_select'
      ? withoutSelectedRevision(current.draft)
      : current.draft
    if (savingDraft !== current.draft) setState({ draft: savingDraft })
    setState({ saveState: 'saving', error: undefined })
    try {
      const updated = await api.updateEditorDocument(current.document.id, {
        version: current.document.version,
        document: savingDraft,
        ...(options.currentAssetID ? { current_asset_id: options.currentAssetID } : {}),
        ...(options.operation ? { operation: options.operation } : {}),
        ...(options.parameters ? { parameters: options.parameters } : {}),
        ...(options.assetReferences ? { asset_references: options.assetReferences } : {})
      })
      const draftChangedDuringSave = state.draft !== savingDraft
      setState({
        document: updated,
        draft: draftChangedDuringSave ? state.draft : updated.document,
        saveState: draftChangedDuringSave ? 'idle' : 'saved',
        error: undefined
      })
      retainedSaveOptions = undefined
      return updated
    } catch (error) {
      retainedSaveOptions = { ...options }
      if (isEditorConflict(error)) {
        let latest = state.document
        try {
          latest = await api.getEditorDocument(current.document.id)
        } catch {
          // The local draft remains usable even if refreshing the server copy fails.
        }
        setState({
          document: latest,
          draft: state.draft || savingDraft,
          saveState: 'conflict',
          error: { kind: 'conflict', message: readableError(error, 'The editor changed elsewhere'), retryable: true }
        })
      } else {
        setState({
          draft: state.draft || savingDraft,
          saveState: 'error',
          error: { kind: 'save', message: readableError(error, 'Editor save failed'), retryable: true }
        })
      }
      throw error
    }
  }

  const finishJob = async (job: CanvasJob, completion: ImageEditorJobCompletion): Promise<CanvasJob> => {
    if (job.status === 'canceled') {
      setState({ job, jobState: 'canceled', error: undefined })
      return job
    }
    if (job.status !== 'completed' && job.status !== 'partial') {
      const error = job.error
      setState({
        job,
        jobState: 'failed',
        error: {
          kind: 'job',
          message: error?.message || `Image edit ${job.status}`,
          retryable: error?.retryable !== false
        }
      })
      return job
    }
    const result = job.results.find((candidate) => candidate.status === 'completed' && candidate.asset_id)
    if (!result?.asset_id) {
      setState({
        job,
        jobState: 'failed',
        error: { kind: 'job', message: 'Image edit returned no usable result', retryable: true }
      })
      return job
    }
    if (completion.canvas) {
      const current = requireDocument()
      setState({
        draft: {
          ...current.draft,
          canvas: { ...current.draft.canvas, ...completion.canvas }
        }
      })
    }
    await save({
      currentAssetID: result.asset_id,
      operation: completion.operation,
      parameters: { ...completion.parameters, job_id: job.id },
      assetReferences: completion.assetReferences
    })
    setState({ job, jobState: 'completed', error: undefined })
    return job
  }

  const monitorJob = async (
    start: (signal: AbortSignal, onUpdate: (job: CanvasJob) => void) => Promise<CanvasJob>,
    completion: ImageEditorJobCompletion
  ): Promise<CanvasJob> => {
    if (jobAborter) throw new Error('An image edit is already running')
    explicitCancellation = false
    const aborter = new AbortController()
    jobAborter = aborter
    setState({ job: undefined, jobState: 'running', error: undefined })
    try {
      const job = await start(aborter.signal, (snapshot) => setState({ job: snapshot, jobState: 'running' }))
      return await finishJob(job, completion)
    } catch (error) {
      if (explicitCancellation || aborter.signal.aborted) {
        setState({ jobState: 'canceled', error: undefined })
      } else if (state.saveState === 'conflict' || state.error?.kind === 'save') {
        setState({ jobState: state.job?.status === 'completed' || state.job?.status === 'partial' ? 'completed' : 'failed' })
      } else {
        setState({
          jobState: 'failed',
          error: { kind: 'job', message: readableError(error, 'Image edit failed'), retryable: true }
        })
      }
      throw error
    } finally {
      if (jobAborter === aborter) jobAborter = undefined
      explicitCancellation = false
    }
  }

  return {
    getState: () => state,
    subscribe(listener) {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    async hydrate(input) {
      setState({ loadState: 'hydrating', saveState: 'idle', error: undefined })
      try {
        const document = await api.createEditorDocument(input)
        retainedSaveOptions = undefined
        setState({
          loadState: 'ready',
          saveState: 'idle',
          document,
          draft: document.document,
          error: undefined
        })
        return document
      } catch (error) {
        setState({
          loadState: 'error',
          error: { kind: 'hydrate', message: readableError(error, 'Editor could not be loaded'), retryable: true }
        })
        throw error
      }
    },
    async reload() {
      const current = requireDocument()
      setState({ loadState: 'hydrating', error: undefined })
      try {
        const document = await api.getEditorDocument(current.document.id)
        retainedSaveOptions = undefined
        setState({ loadState: 'ready', saveState: 'idle', document, draft: document.document, error: undefined })
        return document
      } catch (error) {
        setState({
          loadState: 'error',
          error: { kind: 'hydrate', message: readableError(error, 'Editor could not be reloaded'), retryable: true }
        })
        throw error
      }
    },
    updateDraft(update) {
      const current = requireDocument()
      setState({ draft: update(current.draft), saveState: 'idle', error: undefined })
    },
    save,
    retrySave() {
      return save(retainedSaveOptions || {})
    },
    async selectRevision(revisionID) {
      const current = requireDocument()
      const revision = current.document.revisions.find((candidate) => candidate.id === revisionID)
      if (!revision) throw new Error('Image editor revision was not found')
      const draft = {
        ...current.draft,
        selected_revision_id: revisionID,
        canvas: {
          ...current.draft.canvas,
          width: revision.asset.width,
          height: revision.asset.height
        }
      }
      setState({ draft, saveState: 'idle', error: undefined })
      return save({
        currentAssetID: revision.asset.id,
        operation: 'revision_select',
        parameters: { revision_id: revisionID }
      })
    },
    async commitDerivedAsset(file, parentAssetID, operation, parameters) {
      const current = requireDocument()
      setState({ saveState: 'saving', error: undefined })
      try {
        const asset = await api.uploadEditorDerivedAsset(current.document.id, file, parentAssetID)
        setState({
          draft: {
            ...requireDocument().draft,
            canvas: {
              ...requireDocument().draft.canvas,
              width: asset.width,
              height: asset.height
            }
          }
        })
        const document = await save({ currentAssetID: asset.id, operation, parameters })
        return { asset, document }
      } catch (error) {
        if (state.saveState === 'saving') {
          setState({
            saveState: 'error',
            error: { kind: 'save', message: readableError(error, 'Derived image upload failed'), retryable: true }
          })
        }
        throw error
      }
    },
    runJob(input, completion) {
      return monitorJob((signal, onUpdate) => jobs.run(input, signal, onUpdate), completion)
    },
    resumeJob(jobID, completion) {
      return monitorJob((signal, onUpdate) => jobs.resume(jobID, signal, onUpdate), completion)
    },
    async cancelJob() {
      const job = state.job
      if (!jobAborter) return job
      explicitCancellation = true
      try {
        if (job?.id) {
          const canceled = await jobs.cancel(job.id)
          setState({ job: canceled, jobState: 'canceled', error: undefined })
          return canceled
        }
        setState({ jobState: 'canceled', error: undefined })
        return job
      } catch (error) {
        setState({
          error: { kind: 'cancel', message: readableError(error, 'Image edit could not be canceled'), retryable: true }
        })
        throw error
      } finally {
        jobAborter?.abort(new DOMException('Canceled', 'AbortError'))
      }
    },
    clearError() {
      setState({ error: undefined })
    },
    destroy() {
      destroyed = true
      jobAborter?.abort(new DOMException('Editor closed', 'AbortError'))
      jobAborter = undefined
      listeners.clear()
    }
  }
}

function isEditorConflict(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false
  const value = error as {
    status?: number
    code?: string
    reason?: string
    response?: { status?: number; data?: { code?: string; reason?: string } }
  }
  const status = value.status ?? value.response?.status
  const code = value.reason ?? value.response?.data?.reason ?? value.response?.data?.code ?? value.code
  return status === 409 || code === 'editor_version_conflict'
}

function readableError(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback
}

function withoutSelectedRevision(document: ImageEditorDocumentV1): ImageEditorDocumentV1 {
  if (!document.selected_revision_id) return document
  const { selected_revision_id: _, ...next } = document
  return next
}
