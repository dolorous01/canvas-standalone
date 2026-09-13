import {
  Alert,
  Button,
  Input,
  Progress,
  Segmented,
  Slider,
  Spin,
  Tooltip
} from 'antd'
import {
  ArrowLeft,
  Check,
  Crop,
  Expand,
  Image as ImageIcon,
  Paintbrush,
  RefreshCw,
  RotateCcw,
  Square,
  WandSparkles,
  ZoomIn,
  ZoomOut
} from 'lucide-react'
import { nanoid } from 'nanoid'
import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  useSyncExternalStore
} from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate, useParams } from 'react-router-dom'

import { buildCanvasImageParameters, withCanvasSystemPrompt } from '@sub2api/adapters/image-api'
import {
  assetStorageKey,
  cacheCanvasAssetBlob,
  resolveCanvasAssetURL,
  uploadCanvasAsset
} from '@sub2api/adapters/asset-runtime'
import { useCanvasStore } from '@sub2api/adapters/use-canvas-store'
import { createCanvasAPI, type CanvasAsset, type ImageEditorRevision } from '@sub2api/api/canvas-api'
import { useCanvasHost } from '@sub2api/host-context'
import {
  getCanvasModelCapability,
  getSelectedCanvasAPIKeyID,
  modelOptionName,
  resolveModelForCapability,
  useConfigStore
} from '@sub2api/adapters/use-config-store'
import { CanvasNodeCropDialog, type CanvasImageCropRect } from '@/components/canvas/canvas-node-crop-dialog'
import {
  CanvasNodeMaskEditDialog,
  type CanvasImageMaskEditPayload
} from '@/components/canvas/canvas-node-mask-edit-dialog'
import { cropDataUrl } from '@/lib/canvas/canvas-image-data'
import {
  applyEditorResultToProject,
  persistedImageAssetID
} from './apply-editor-result'
import {
  createImageEditorController,
  type ImageEditorJobCompletion
} from './image-editor-controller'
import {
  createOutpaintFiles,
  imageFileFromURL
} from './image-editor-media'

type MaskOperation = Extract<ImageEditorJobCompletion['operation'], 'mask_edit' | 'background_replace'>
type RetryAction =
  | { kind: 'mask'; payload: CanvasImageMaskEditPayload; operation: MaskOperation }
  | { kind: 'outpaint'; prompt: string; ratio: number }

const outpaintRatios = [
  { label: '1:1', value: 1 },
  { label: '4:3', value: 4 / 3 },
  { label: '16:9', value: 16 / 9 },
  { label: '3:4', value: 3 / 4 },
  { label: '9:16', value: 9 / 16 }
]

export default function FocusedImageEditor() {
  const { projectId = '', nodeId = '' } = useParams<{ projectId: string; nodeId: string }>()
  const navigate = useNavigate()
  const host = useCanvasHost()
  const { t } = useTranslation()
  const hydrated = useCanvasStore((value) => value.hydrated)
  const project = useCanvasStore((value) => value.projects.find((candidate) => candidate.id === projectId))
  const node = project?.nodes.find((candidate) => candidate.id === nodeId)
  const config = useConfigStore((value) => value.config)
  const sourceAssetID = persistedImageAssetID(node)
  const dimensions = nodeImageDimensions(node)
  const api = useMemo(() => createCanvasAPI(host), [host])
  const controller = useMemo(() => createImageEditorController(api), [api])
  const state = useSyncExternalStore(controller.subscribe, controller.getState, controller.getState)
  const [cropOpen, setCropOpen] = useState(false)
  const [maskOperation, setMaskOperation] = useState<MaskOperation>()
  const [outpaintRatio, setOutpaintRatio] = useState(16 / 9)
  const [outpaintPrompt, setOutpaintPrompt] = useState('')
  const [actionPending, setActionPending] = useState(false)
  const [uiError, setUIError] = useState('')
  const [retryAction, setRetryAction] = useState<RetryAction>()
  const currentAsset = state.document?.current_asset
  const currentURL = useAssetURL(currentAsset)
  const isBusy = actionPending || state.saveState === 'saving' || state.jobState === 'running'

  useEffect(() => () => controller.destroy(), [controller])

  useEffect(() => {
    if (!hydrated || !project || !node || !sourceAssetID) return
    void controller.hydrate({
      project_id: project.id,
      node_id: node.id,
      base_asset_id: sourceAssetID,
      document: {
        schema_version: 1,
        viewport: { zoom: 1, x: 0, y: 0 },
        canvas: {
          width: dimensions.width,
          height: dimensions.height,
          background: 'transparent'
        }
      }
    }).catch(() => undefined)
  }, [controller, dimensions.height, dimensions.width, hydrated, node?.id, project?.id, sourceAssetID])

  const backToCanvas = useCallback(() => {
    navigate(`/canvas/${encodeURIComponent(projectId)}`)
  }, [navigate, projectId])

  const runMaskedJob = useCallback(async (
    sourceAsset: CanvasAsset,
    mask: File,
    prompt: string,
    completion: ImageEditorJobCompletion
  ) => {
    const apiKeyID = getSelectedCanvasAPIKeyID()
    const modelValue = resolveModelForCapability(config, node?.metadata?.model, 'image') || config.imageModel || config.model
    const selectedModel = modelOptionName(modelValue)
    const capability = getCanvasModelCapability(selectedModel)
    if (!apiKeyID || !selectedModel) throw new Error(t('canvas.focusedEditor.modelRequired'))
    if (!capability?.edit || capability.mask === false) throw new Error(t('canvas.focusedEditor.maskUnavailable'))
    const maskAsset = await uploadCanvasAsset(mask, mask.name, projectId, {
      width: sourceAsset.width,
      height: sourceAsset.height
    })
    await controller.runJob({
      project_id: projectId,
      client_node_id: nodeId,
      operation: 'edit',
      api_key_id: apiKeyID,
      selected_model: selectedModel,
      prompt: withCanvasSystemPrompt(config, prompt),
      input_asset_ids: [sourceAsset.id],
      mask_asset_id: maskAsset.id,
      parameters: { ...buildCanvasImageParameters(config, selectedModel), n: 1 },
      generationNonce: `editor-${nanoid()}`
    }, completion)
  }, [config, controller, node?.metadata?.model, nodeId, projectId, t])

  const performMaskEdit = useCallback(async (payload: CanvasImageMaskEditPayload, operation: MaskOperation) => {
    if (!currentAsset) return
    setActionPending(true)
    setUIError('')
    try {
      const mask = await imageFileFromURL(payload.maskDataUrl, 'editor-mask.png')
      await runMaskedJob(currentAsset, mask, payload.prompt, {
        operation,
        parameters: { prompt: payload.prompt }
      })
    } catch (error) {
      if (controller.getState().jobState !== 'canceled') setUIError(readableError(error))
    } finally {
      setActionPending(false)
    }
  }, [controller, currentAsset, runMaskedJob])

  const performOutpaint = useCallback(async (prompt: string, ratio: number) => {
    const document = controller.getState().document
    if (!document || !currentAsset || !currentURL) return
    setActionPending(true)
    setUIError('')
    try {
      const files = await createOutpaintFiles(currentURL, ratio)
      const extended = await api.uploadEditorDerivedAsset(document.id, files.source, currentAsset.id)
      cacheCanvasAssetBlob(assetStorageKey(extended.id), files.source)
      await runMaskedJob(extended, files.mask, prompt, {
        operation: 'outpaint',
        parameters: { prompt, target_ratio: ratio },
        canvas: { width: files.width, height: files.height }
      })
    } catch (error) {
      if (controller.getState().jobState !== 'canceled') setUIError(readableError(error))
    } finally {
      setActionPending(false)
    }
  }, [api, controller, currentAsset, currentURL, runMaskedJob])

  const handleMaskConfirm = (payload: CanvasImageMaskEditPayload) => {
    const operation = maskOperation || 'mask_edit'
    setMaskOperation(undefined)
    setRetryAction({ kind: 'mask', payload, operation })
    void performMaskEdit(payload, operation)
  }

  const handleCrop = async (crop: CanvasImageCropRect) => {
    if (!currentAsset || !currentURL) return
    setCropOpen(false)
    setActionPending(true)
    setUIError('')
    try {
      const croppedURL = await cropDataUrl(currentURL, crop)
      const file = await imageFileFromURL(croppedURL, 'editor-crop.png')
      const result = await controller.commitDerivedAsset(file, currentAsset.id, 'crop', { crop })
      cacheCanvasAssetBlob(assetStorageKey(result.asset.id), file)
    } catch (error) {
      setUIError(readableError(error))
    } finally {
      setActionPending(false)
    }
  }

  const startOutpaint = () => {
    const prompt = outpaintPrompt.trim()
    if (!prompt) {
      setUIError(t('canvas.focusedEditor.promptRequired'))
      return
    }
    setRetryAction({ kind: 'outpaint', prompt, ratio: outpaintRatio })
    void performOutpaint(prompt, outpaintRatio)
  }

  const retry = () => {
    if (!retryAction) return
    if (retryAction.kind === 'mask') void performMaskEdit(retryAction.payload, retryAction.operation)
    else void performOutpaint(retryAction.prompt, retryAction.ratio)
  }

  const applyToCanvas = () => {
    if (!project || !currentAsset || !currentURL || state.saveState === 'conflict') return
    const updated = applyEditorResultToProject(project, nodeId, currentAsset, currentURL)
    if (!updated) {
      setUIError(t('canvas.focusedEditor.nodeMissing'))
      return
    }
    useCanvasStore.getState().updateProject(project.id, { nodes: updated.nodes })
    host.notify('success', t('canvas.focusedEditor.applied'))
    backToCanvas()
  }

  const selectBaseAsset = async () => {
    const document = controller.getState().document
    if (!document) return
    controller.updateDraft((value) => {
      const { selected_revision_id: _, ...draft } = value
      return {
        ...draft,
        canvas: {
          ...draft.canvas,
          width: document.base_asset.width,
          height: document.base_asset.height
        }
      }
    })
    try {
      await controller.save({
        currentAssetID: document.base_asset.id,
        operation: 'revision_select',
        parameters: { revision_id: 'base' }
      })
    } catch {
      // Conflict and retry controls are rendered from controller state.
    }
  }

  const selectRevision = async (revision: ImageEditorRevision) => {
    try {
      await controller.selectRevision(revision.id)
    } catch (error) {
      setUIError(readableError(error))
    }
  }

  const setZoom = (zoom: number, persist = false) => {
    if (!state.draft) return
    controller.updateDraft((value) => ({
      ...value,
      viewport: { ...value.viewport, zoom }
    }))
    if (persist) void controller.save().catch(() => undefined)
  }

  const setBackground = (background: 'transparent' | 'white' | 'black') => {
    controller.updateDraft((value) => ({
      ...value,
      canvas: { ...value.canvas, background }
    }))
    void controller.save().catch(() => undefined)
  }

  if (!hydrated || (sourceAssetID && state.loadState !== 'error' && state.loadState !== 'ready')) {
    return <EditorLoading label={t('canvas.focusedEditor.loading')} />
  }

  if (!project || !node || !sourceAssetID) {
    return (
      <EditorUnavailable
        title={t('canvas.focusedEditor.unavailable')}
        description={t('canvas.focusedEditor.persistedRequired')}
        backLabel={t('canvas.focusedEditor.back')}
        onBack={backToCanvas}
      />
    )
  }

  if (state.loadState === 'error' || !state.document || !state.draft) {
    return (
      <EditorUnavailable
        title={t('canvas.focusedEditor.loadFailed')}
        description={state.error?.message || t('canvas.focusedEditor.unavailable')}
        backLabel={t('canvas.focusedEditor.back')}
        onBack={backToCanvas}
      />
    )
  }

  const background = state.draft.canvas.background
  const zoom = state.draft.viewport.zoom
  const error = uiError || state.error?.message
  const progress = state.job
    ? Math.round((state.job.completed_count / Math.max(1, state.job.requested_count)) * 100)
    : 0

  return (
    <div className="flex h-full min-h-[480px] min-w-0 flex-col overflow-hidden bg-[#eef1f2] text-[#192126] dark:bg-[#171a1c] dark:text-[#edf1f2]" data-focused-image-editor>
      <header className="grid min-h-14 shrink-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3 border-b border-black/10 bg-white px-3 dark:border-white/10 dark:bg-[#222629] sm:px-4">
        <Tooltip title={t('canvas.focusedEditor.back')}>
          <Button type="text" icon={<ArrowLeft className="size-4" />} aria-label={t('canvas.focusedEditor.back')} onClick={backToCanvas} />
        </Tooltip>
        <div className="min-w-0">
          <div className="truncate text-sm font-semibold">{t('canvas.focusedEditor.title')}</div>
          <div className="truncate text-xs opacity-55">{project.title} / {node.title}</div>
        </div>
        <div className="flex min-w-0 items-center justify-end gap-2">
          <SaveStateLabel state={state.saveState} />
          <Button
            type="primary"
            icon={<Check className="size-4" />}
            disabled={!currentURL || isBusy || state.saveState === 'conflict'}
            onClick={applyToCanvas}
          >
            <span className="hidden sm:inline">{t('canvas.focusedEditor.apply')}</span>
          </Button>
        </div>
      </header>

      <main className="grid min-h-0 flex-1 grid-cols-[88px_minmax(0,1fr)_300px] overflow-hidden max-lg:grid-cols-[76px_minmax(0,1fr)_268px] max-md:grid-cols-1 max-md:grid-rows-[minmax(280px,1fr)_auto_auto] max-md:overflow-y-auto">
        <RevisionRail
          baseAsset={state.document.base_asset}
          revisions={state.document.revisions}
          currentAssetID={state.document.current_asset.id}
          selectedRevisionID={state.draft.selected_revision_id}
          disabled={isBusy}
          onSelectBase={() => void selectBaseAsset()}
          onSelectRevision={(revision) => void selectRevision(revision)}
        />

        <section className="relative order-2 flex min-h-0 min-w-0 items-center justify-center overflow-auto border-x border-black/10 p-5 dark:border-white/10 max-md:order-1 max-md:border-x-0 max-md:border-b max-md:p-3">
          <div
            className="relative flex h-full min-h-[240px] w-full items-center justify-center overflow-hidden"
            style={stageBackground(background)}
          >
            {currentURL ? (
              <img
                src={currentURL}
                alt={node.title || t('canvas.focusedEditor.image')}
                className="block max-h-full max-w-full object-contain transition-transform motion-reduce:transition-none"
                style={{ transform: `scale(${zoom})` }}
                draggable={false}
              />
            ) : (
              <Spin />
            )}
            {actionPending && state.jobState !== 'running' ? (
              <div className="absolute inset-0 flex items-center justify-center bg-white/70 dark:bg-black/60">
                <Spin />
              </div>
            ) : null}
          </div>

          <div className="absolute bottom-3 left-1/2 flex h-10 -translate-x-1/2 items-center gap-1 border border-black/10 bg-white px-1 shadow-sm dark:border-white/10 dark:bg-[#252a2d]">
            <Tooltip title={t('canvas.focusedEditor.zoomOut')}>
              <Button type="text" size="small" icon={<ZoomOut className="size-4" />} aria-label={t('canvas.focusedEditor.zoomOut')} disabled={zoom <= 0.25 || isBusy} onClick={() => setZoom(Math.max(0.25, zoom - 0.25), true)} />
            </Tooltip>
            <span className="w-12 text-center text-xs tabular-nums">{Math.round(zoom * 100)}%</span>
            <Tooltip title={t('canvas.focusedEditor.zoomIn')}>
              <Button type="text" size="small" icon={<ZoomIn className="size-4" />} aria-label={t('canvas.focusedEditor.zoomIn')} disabled={zoom >= 3 || isBusy} onClick={() => setZoom(Math.min(3, zoom + 0.25), true)} />
            </Tooltip>
            <Tooltip title={t('canvas.focusedEditor.resetZoom')}>
              <Button type="text" size="small" icon={<RotateCcw className="size-4" />} aria-label={t('canvas.focusedEditor.resetZoom')} disabled={isBusy} onClick={() => setZoom(1, true)} />
            </Tooltip>
          </div>
        </section>

        <aside className="order-3 min-h-0 overflow-y-auto bg-white dark:bg-[#222629] max-md:overflow-visible">
          <EditorSection title={t('canvas.focusedEditor.transform')}>
            <div className="grid grid-cols-2 gap-2">
              <Button icon={<Crop className="size-4" />} disabled={!currentURL || isBusy} onClick={() => setCropOpen(true)}>
                {t('canvas.focusedEditor.crop')}
              </Button>
              <Button icon={<Paintbrush className="size-4" />} disabled={!currentURL || isBusy} onClick={() => setMaskOperation('mask_edit')}>
                {t('canvas.focusedEditor.maskEdit')}
              </Button>
              <Button className="col-span-2" icon={<WandSparkles className="size-4" />} disabled={!currentURL || isBusy} onClick={() => setMaskOperation('background_replace')}>
                {t('canvas.focusedEditor.backgroundReplace')}
              </Button>
            </div>
          </EditorSection>

          <EditorSection title={t('canvas.focusedEditor.outpaint')}>
            <Segmented
              block
              size="small"
              value={outpaintRatio}
              options={outpaintRatios}
              disabled={isBusy}
              onChange={(value) => setOutpaintRatio(Number(value))}
            />
            <Input.TextArea
              rows={4}
              value={outpaintPrompt}
              disabled={isBusy}
              placeholder={t('canvas.focusedEditor.outpaintPrompt')}
              onChange={(event) => {
                setOutpaintPrompt(event.target.value)
                setUIError('')
              }}
            />
            <Button block type="primary" icon={<Expand className="size-4" />} disabled={!currentURL || isBusy} onClick={startOutpaint}>
              {t('canvas.focusedEditor.expand')}
            </Button>
          </EditorSection>

          <EditorSection title={t('canvas.focusedEditor.view')}>
            <Segmented
              block
              size="small"
              value={background}
              disabled={isBusy}
              options={[
                { label: t('canvas.focusedEditor.transparent'), value: 'transparent' },
                { label: t('canvas.focusedEditor.white'), value: 'white' },
                { label: t('canvas.focusedEditor.black'), value: 'black' }
              ]}
              onChange={(value) => setBackground(value as 'transparent' | 'white' | 'black')}
            />
            <Slider
              min={0.25}
              max={3}
              step={0.05}
              value={zoom}
              disabled={isBusy}
              onChange={(value) => setZoom(value)}
              onChangeComplete={(value) => setZoom(value, true)}
            />
          </EditorSection>

          {state.jobState !== 'idle' ? (
            <EditorSection title={t('canvas.focusedEditor.job')}>
              <div className="flex items-center justify-between gap-3 text-xs">
                <span>{t(`canvas.focusedEditor.job_${state.jobState}`)}</span>
                {state.job?.phase ? <span className="truncate opacity-55">{state.job.phase}</span> : null}
              </div>
              <Progress percent={state.jobState === 'completed' ? 100 : progress} size="small" status={state.jobState === 'failed' ? 'exception' : state.jobState === 'completed' ? 'success' : 'active'} showInfo={false} />
              {state.jobState === 'running' ? (
                <Button block danger icon={<Square className="size-3.5 fill-current" />} onClick={() => void controller.cancelJob().catch(() => undefined)}>
                  {t('canvas.focusedEditor.cancelJob')}
                </Button>
              ) : null}
            </EditorSection>
          ) : null}

          {error ? (
            <div className="border-t border-black/10 p-4 dark:border-white/10">
              <Alert
                type={state.saveState === 'conflict' ? 'warning' : 'error'}
                showIcon
                message={state.saveState === 'conflict' ? t('canvas.focusedEditor.conflict') : error}
                description={state.saveState === 'conflict' ? error : undefined}
                action={(
                  <div className="flex flex-col gap-1">
                    {state.saveState === 'conflict' ? (
                      <>
                        <Button size="small" onClick={() => void controller.retrySave().catch(() => undefined)}>{t('canvas.focusedEditor.retrySave')}</Button>
                        <Button size="small" type="text" onClick={() => void controller.reload().catch(() => undefined)}>{t('canvas.focusedEditor.reload')}</Button>
                      </>
                    ) : retryAction ? (
                      <Button size="small" icon={<RefreshCw className="size-3.5" />} disabled={isBusy} onClick={retry}>{t('canvas.focusedEditor.retry')}</Button>
                    ) : null}
                  </div>
                )}
              />
            </div>
          ) : null}
        </aside>
      </main>

      {currentURL ? (
        <CanvasNodeCropDialog
          dataUrl={currentURL}
          open={cropOpen}
          onClose={() => setCropOpen(false)}
          onConfirm={(crop) => void handleCrop(crop)}
        />
      ) : null}
      {currentURL ? (
        <CanvasNodeMaskEditDialog
          dataUrl={currentURL}
          open={Boolean(maskOperation)}
          onClose={() => setMaskOperation(undefined)}
          onConfirm={handleMaskConfirm}
        />
      ) : null}
    </div>
  )
}

function RevisionRail({
  baseAsset,
  revisions,
  currentAssetID,
  selectedRevisionID,
  disabled,
  onSelectBase,
  onSelectRevision
}: {
  baseAsset: CanvasAsset
  revisions: ImageEditorRevision[]
  currentAssetID: string
  selectedRevisionID?: string
  disabled: boolean
  onSelectBase(): void
  onSelectRevision(revision: ImageEditorRevision): void
}) {
  const { t } = useTranslation()
  const selectedBase = !selectedRevisionID && baseAsset.id === currentAssetID
  return (
    <aside className="order-1 min-h-0 overflow-y-auto bg-[#f7f8f8] p-2 dark:bg-[#1d2022] max-md:order-2 max-md:flex max-md:overflow-x-auto max-md:overflow-y-hidden max-md:border-b max-md:border-black/10 max-md:dark:border-white/10">
      <div className="mb-2 px-1 text-[11px] font-semibold uppercase text-black/45 dark:text-white/45 max-md:mb-0 max-md:mr-2 max-md:self-center">
        {t('canvas.focusedEditor.revisions')}
      </div>
      <RevisionButton
        asset={baseAsset}
        label={t('canvas.focusedEditor.source')}
        accessibleLabel={t('canvas.focusedEditor.source')}
        selected={selectedBase}
        disabled={disabled}
        onClick={onSelectBase}
      />
      {[...revisions].reverse().map((revision, index) => (
        <RevisionButton
          key={revision.id}
          asset={revision.asset}
          label={`${revisions.length - index}`}
          accessibleLabel={t('canvas.focusedEditor.revision', { number: revisions.length - index })}
          selected={selectedRevisionID ? selectedRevisionID === revision.id : revision.asset.id === currentAssetID}
          disabled={disabled}
          onClick={() => onSelectRevision(revision)}
        />
      ))}
    </aside>
  )
}

function RevisionButton({
  asset,
  label,
  accessibleLabel,
  selected,
  disabled,
  onClick
}: {
  asset: CanvasAsset
  label: string
  accessibleLabel: string
  selected: boolean
  disabled: boolean
  onClick(): void
}) {
  const url = useAssetURL(asset)
  return (
    <button
      type="button"
      className={`mb-2 block w-full min-w-0 border bg-white p-1 text-left transition dark:bg-[#292e31] max-md:mb-0 max-md:mr-2 max-md:w-16 max-md:shrink-0 ${selected ? 'border-[#14805e] shadow-[0_0_0_1px_#14805e]' : 'border-black/10 hover:border-black/30 dark:border-white/10 dark:hover:border-white/30'}`}
      disabled={disabled}
      aria-label={accessibleLabel}
      aria-pressed={selected}
      onClick={onClick}
    >
      <span className="flex aspect-square w-full items-center justify-center overflow-hidden bg-[#e7eaeb] dark:bg-[#16191b]">
        {url ? <img src={url} alt="" className="h-full w-full object-cover" /> : <ImageIcon className="size-4 opacity-35" />}
      </span>
      <span className="mt-1 block truncate px-0.5 text-center text-[10px] font-medium">{label}</span>
    </button>
  )
}

function EditorSection({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="space-y-3 border-b border-black/10 p-4 dark:border-white/10">
      <h2 className="text-xs font-semibold uppercase text-black/50 dark:text-white/50">{title}</h2>
      {children}
    </section>
  )
}

function SaveStateLabel({ state }: { state: 'idle' | 'saving' | 'saved' | 'conflict' | 'error' }) {
  const { t } = useTranslation()
  const tone = state === 'conflict' || state === 'error'
    ? 'text-[#b33a3a] dark:text-[#f08a8a]'
    : state === 'saved'
      ? 'text-[#14745a] dark:text-[#65c7a7]'
      : 'text-black/45 dark:text-white/45'
  return <span className={`hidden text-xs sm:inline ${tone}`}>{t(`canvas.focusedEditor.save_${state}`)}</span>
}

function EditorLoading({ label }: { label: string }) {
  return (
    <div className="flex h-full min-h-[480px] items-center justify-center bg-[#eef1f2] dark:bg-[#171a1c]">
      <Spin description={label} size="large"><div className="h-16 w-48" /></Spin>
    </div>
  )
}

function EditorUnavailable({
  title,
  description,
  backLabel,
  onBack
}: {
  title: string
  description: string
  backLabel: string
  onBack(): void
}) {
  return (
    <div className="flex h-full min-h-[480px] items-center justify-center bg-[#eef1f2] p-6 dark:bg-[#171a1c]">
      <div className="max-w-md text-center">
        <ImageIcon className="mx-auto size-8 opacity-35" />
        <h1 className="mt-4 text-lg font-semibold">{title}</h1>
        <p className="mt-2 text-sm opacity-60">{description}</p>
        <Button className="mt-5" icon={<ArrowLeft className="size-4" />} onClick={onBack}>{backLabel}</Button>
      </div>
    </div>
  )
}

function useAssetURL(asset?: CanvasAsset): string {
  const [url, setURL] = useState('')
  useEffect(() => {
    let active = true
    setURL('')
    if (!asset?.id) return () => { active = false }
    void resolveCanvasAssetURL(assetStorageKey(asset.id), asset.url).then((value) => {
      if (active) setURL(value)
    })
    return () => { active = false }
  }, [asset?.id, asset?.url])
  return url
}

function nodeImageDimensions(node: ReturnType<typeof useCanvasStore.getState>['projects'][number]['nodes'][number] | undefined) {
  const primary = node?.metadata?.images?.find((image) => image.id === node.metadata?.primaryImageId)
  return {
    width: Math.max(1, primary?.naturalWidth || node?.metadata?.naturalWidth || Math.round(node?.width || 1024)),
    height: Math.max(1, primary?.naturalHeight || node?.metadata?.naturalHeight || Math.round(node?.height || 1024))
  }
}

function stageBackground(background: 'transparent' | 'white' | 'black'): React.CSSProperties {
  if (background === 'white') return { backgroundColor: '#fff' }
  if (background === 'black') return { backgroundColor: '#111' }
  return {
    backgroundColor: '#dfe3e5',
    backgroundImage: 'linear-gradient(45deg, rgba(255,255,255,.55) 25%, transparent 25%), linear-gradient(-45deg, rgba(255,255,255,.55) 25%, transparent 25%), linear-gradient(45deg, transparent 75%, rgba(255,255,255,.55) 75%), linear-gradient(-45deg, transparent 75%, rgba(255,255,255,.55) 75%)',
    backgroundSize: '24px 24px',
    backgroundPosition: '0 0, 0 12px, 12px -12px, -12px 0'
  }
}

function readableError(error: unknown): string {
  return error instanceof Error && error.message ? error.message : 'Image edit failed'
}
