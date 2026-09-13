import {
  useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore,
  type PointerEvent as ReactPointerEvent, type ReactElement
} from 'react'
import {
  Box, Braces, ChevronDown, CircleHelp, Copy, Download, FileImage, Focus,
  FolderKanban, Group, ImagePlus, Link2, LoaderCircle, PanelRightClose,
  Plus, Redo2, Save, Search, SlidersHorizontal, Trash2, Type,
  Undo2, Upload, WandSparkles, X, ZoomIn, ZoomOut
} from 'lucide-react'
import { nanoid } from 'nanoid'
import { InfiniteCanvas } from '@/components/canvas/infinite-canvas'
import { useThemeStore } from '@/stores/use-theme-store'
import type { CanvasDocument, CanvasEdgeDocument, CanvasJob, CanvasModelParameters, CanvasNodeDocument } from '@sub2api/api/canvas-api'
import { createCanvasAPI } from '@sub2api/api/canvas-api'
import { APIKeyModelSelector } from '@sub2api/components/api-key-model-selector'
import { SourceNoticeDialog } from '@sub2api/components/source-notice-dialog'
import { useCanvasHost } from '@sub2api/host-context'
import { useCanvasI18n } from '@sub2api/i18n'
import { createCanvasJobController } from '@sub2api/jobs/canvas-job-controller'
import { useCanvasSessionStore } from '@sub2api/stores/canvas-session-store'
import { createServerCanvasStore } from '@sub2api/stores/server-canvas-store'

type Viewport = { x: number; y: number; k: number }
type DragState = {
  ids: string[]
  startX: number
  startY: number
  scale: number
  original: Map<string, { x: number; y: number }>
  before: CanvasDocument
}

export function CanvasApp() {
  const host = useCanvasHost()
  const hostRef = useRef(host)
  hostRef.current = host
  const t = useCanvasI18n()
  const api = useMemo(() => createCanvasAPI(host), [host.apiBaseURL, host.request, host.stream])
  const projects = useMemo(() => createServerCanvasStore(api), [api])
  const controller = useMemo(() => createCanvasJobController(api), [api])
  const state = useSyncExternalStore(projects.subscribe, projects.getState, projects.getState)
  const { apiKeyID, model, setSelection } = useCanvasSessionStore()
  const [config, setConfig] = useState<Awaited<ReturnType<typeof api.getConfig>>>()
  const [selected, setSelected] = useState<string[]>([])
  const [prompt, setPrompt] = useState('')
  const [operation, setOperation] = useState<'generation' | 'edit'>('generation')
  const [count, setCount] = useState(1)
  const [parameters, setParameters] = useState<CanvasModelParameters>({})
  const [presetID, setPresetID] = useState('')
  const [customWidth, setCustomWidth] = useState(2048)
  const [customHeight, setCustomHeight] = useState(2048)
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const [inspectorOpen, setInspectorOpen] = useState(false)
  const [sourceOpen, setSourceOpen] = useState(false)
  const [uploading, setUploading] = useState(false)
  const [projectQuery, setProjectQuery] = useState('')
  const [projectBusy, setProjectBusy] = useState('')
  const [deleteCandidate, setDeleteCandidate] = useState('')
  const [nameDraft, setNameDraft] = useState('')
  const [drag, setDrag] = useState<DragState>()
  const [historyVersion, setHistoryVersion] = useState(0)
  const history = useRef<{ past: CanvasDocument[]; future: CanvasDocument[] }>({ past: [], future: [] })
  const canvasRef = useRef<HTMLDivElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const importInputRef = useRef<HTMLInputElement>(null)
  const aborters = useRef(new Map<string, AbortController>())
  const observedJobs = useRef(new Set<string>())

  const activeID = state.activeProjectID || state.order[0]
  const active = activeID ? state.projectsByID[activeID] : undefined
  const document = active?.localDraft || active?.document
  const viewport = document?.viewport || { x: 0, y: 0, k: 1 }
  const selectedModel = config?.models.find((item) => item.model === model)
  const capability = selectedModel?.capability
  const visibleProjectIDs = state.order.filter((id) => {
    const query = projectQuery.trim().toLocaleLowerCase()
    return !query || state.projectsByID[id]?.name.toLocaleLowerCase().includes(query)
  })
  const selectedNodes = document?.nodes.filter((node) => selected.includes(node.id)) || []
  const selectedImage = selectedNodes.length === 1 && selectedNodes[0].type === 'image' ? selectedNodes[0] : undefined
  const primarySelectedNode = selectedNodes[0]
  const selectionToolbarStyle = primarySelectedNode ? {
    left: `clamp(76px, ${viewport.x + (primarySelectedNode.position.x + (primarySelectedNode.size?.width || 220) / 2) * viewport.k}px, calc(100% - 76px))`,
    top: `clamp(66px, ${viewport.y + primarySelectedNode.position.y * viewport.k - 10}px, calc(100% - 76px))`
  } : undefined

  useEffect(() => {
    useThemeStore.getState().setTheme(host.theme)
  }, [host.theme])

  useEffect(() => {
    void Promise.all([projects.hydrate(), api.getConfig().then(setConfig)]).catch((error) => {
      hostRef.current.notify('error', readableError(error))
    })
    return () => {
      projects.destroy()
      for (const aborter of aborters.current.values()) aborter.abort()
      aborters.current.clear()
    }
  }, [api, projects])

  useEffect(() => {
    if (!apiKeyID) return
    void api.getConfig(apiKeyID).then((next) => {
      setConfig(next)
      const compatible = next.models.find((item) => item.model === model && item.capability[operation])
      if (!compatible) setSelection(apiKeyID, next.models.find((item) => item.capability[operation])?.model)
    }).catch((error) => hostRef.current.notify('error', readableError(error)))
  }, [api, apiKeyID, operation, setSelection])

  useEffect(() => {
    const defaults = selectedModel?.capability.defaults || {}
    setParameters({ ...defaults })
    setPresetID('')
    setCount((current) => Math.min(Math.max(1, current), Math.max(1, selectedModel?.capability.max_outputs || 1)))
    const dimensions = parseCanvasSize(defaults.size)
    if (dimensions) {
      setCustomWidth(dimensions.width)
      setCustomHeight(dimensions.height)
    }
  }, [model, selectedModel?.model])

  useEffect(() => {
    if (!state.hydrated || state.activeProjectID || state.order.length === 0) return
    void projects.openProject(state.order[0]).catch((error) => hostRef.current.notify('error', readableError(error)))
  }, [projects, state.activeProjectID, state.hydrated, state.order])

  useEffect(() => {
    setNameDraft(active?.name || '')
    history.current = { past: [], future: [] }
    setHistoryVersion((value) => value + 1)
    setSelected([])
  }, [activeID, active?.name])

  const commit = useCallback((update: CanvasDocument | ((current: CanvasDocument) => CanvasDocument), record = true) => {
    if (!activeID) return
    projects.mutateDocument(activeID, (current) => {
      const next = typeof update === 'function' ? update(current) : update
      if (record && next !== current) {
        history.current.past.push(structuredClone(current))
        if (history.current.past.length > 80) history.current.past.shift()
        history.current.future = []
        setHistoryVersion((value) => value + 1)
      }
      return next
    })
  }, [activeID, projects])

  useEffect(() => {
    if (!drag) return
    const move = (event: PointerEvent) => {
      const dx = (event.clientX - drag.startX) / drag.scale
      const dy = (event.clientY - drag.startY) / drag.scale
      commit((current) => ({
        ...current,
        nodes: current.nodes.map((node) => {
          const original = drag.original.get(node.id)
          return original ? { ...node, position: { x: original.x + dx, y: original.y + dy } } : node
        })
      }), false)
    }
    const up = () => {
      setDrag(undefined)
      const current = activeID
        ? projects.getState().projectsByID[activeID]?.localDraft || projects.getState().projectsByID[activeID]?.document
        : undefined
      const moved = current?.nodes.some((node) => {
        const original = drag.original.get(node.id)
        return original && (node.position.x !== original.x || node.position.y !== original.y)
      })
      if (moved) {
        history.current.past.push(drag.before)
        if (history.current.past.length > 80) history.current.past.shift()
        history.current.future = []
        setHistoryVersion((value) => value + 1)
      }
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up, { once: true })
    return () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
    }
  }, [activeID, commit, drag, projects])

  const addNode = (type: CanvasNodeDocument['type'], patch: Partial<CanvasNodeDocument> = {}) => {
    if (!activeID) return
    const nodeID = patch.id || `node_${nanoid()}`
    commit((current) => {
      const index = current.nodes.length
      const node: CanvasNodeDocument = {
        id: nodeID,
        type,
        position: { x: 160 + (index % 4) * 260, y: 130 + Math.floor(index / 4) * 220 },
        size: type === 'group' ? { width: 520, height: 340 } : { width: 220, height: type === 'image' ? 260 : 150 },
        ...patch
      }
      return { ...current, nodes: [...current.nodes, node] }
    })
    setSelected([nodeID])
  }

  const updateNode = (id: string, patch: Partial<CanvasNodeDocument>, record = true) => {
    commit((current) => ({
      ...current,
      nodes: current.nodes.map((node) => node.id === id ? { ...node, ...patch } : node)
    }), record)
  }

  const removeSelected = () => {
    if (!document || !selected.length) return
    const ids = new Set(selected)
    commit((current) => ({
      ...current,
      nodes: current.nodes.filter((node) => !ids.has(node.id)),
      edges: current.edges.filter((edge) => !ids.has(edge.source) && !ids.has(edge.target))
    }))
    setSelected([])
  }

  const connectSelected = () => {
    if (!document || selected.length !== 2) return
    const edge: CanvasEdgeDocument = { id: `edge_${nanoid()}`, source: selected[0], target: selected[1] }
    if (document.edges.some((item) => item.source === edge.source && item.target === edge.target)) return
    commit((current) => ({ ...current, edges: [...current.edges, edge] }))
  }

  const undo = () => {
    if (!document || !activeID) return
    const previous = history.current.past.pop()
    if (!previous) return
    history.current.future.push(structuredClone(document))
    projects.updateDocument(activeID, previous)
    setHistoryVersion((value) => value + 1)
  }

  const redo = () => {
    if (!document || !activeID) return
    const next = history.current.future.pop()
    if (!next) return
    history.current.past.push(structuredClone(document))
    projects.updateDocument(activeID, next)
    setHistoryVersion((value) => value + 1)
  }

  const setViewport = (next: Viewport) => {
    commit((current) => ({ ...current, viewport: next }), false)
  }

  const fitView = () => {
    if (!document || !canvasRef.current || document.nodes.length === 0) {
      setViewport({ x: 0, y: 0, k: 1 })
      return
    }
    const rect = canvasRef.current.getBoundingClientRect()
    const bounds = document.nodes.reduce((result, node) => ({
      minX: Math.min(result.minX, node.position.x), minY: Math.min(result.minY, node.position.y),
      maxX: Math.max(result.maxX, node.position.x + (node.size?.width || 220)),
      maxY: Math.max(result.maxY, node.position.y + (node.size?.height || 160))
    }), { minX: Infinity, minY: Infinity, maxX: -Infinity, maxY: -Infinity })
    const k = Math.min(1.2, Math.max(.15, Math.min((rect.width - 100) / (bounds.maxX - bounds.minX), (rect.height - 100) / (bounds.maxY - bounds.minY))))
    setViewport({ x: (rect.width - (bounds.maxX + bounds.minX) * k) / 2, y: (rect.height - (bounds.maxY + bounds.minY) * k) / 2, k })
  }

  const uploadFile = async (file: File) => {
    if (!activeID) return
    setUploading(true)
    try {
      const asset = await api.uploadAsset(file, activeID)
      addNode('image', { asset_id: asset.id, text: file.name, metadata: { source: 'upload' } })
    } catch (error) {
      hostRef.current.notify('error', readableError(error))
    } finally {
      setUploading(false)
    }
  }

  const applyJobResult = useCallback((projectID: string, nodeID: string, job: CanvasJob) => {
    projects.mutateDocument(projectID, (current) => {
      const base = current.nodes.find((node) => node.id === nodeID)
      if (!base) return current
      const results = job.results.filter((item) => item.asset_id).slice().sort((left, right) => left.index - right.index)
      const first = results[0]
      const generationStatus = job.status === 'queued' || job.status === 'running'
        ? (job.phase || job.status)
        : job.status
      const metadata = {
        ...base.metadata,
        jobId: job.id,
        generationStatus,
        selectedModel: job.selected_model,
        successfulModel: job.successful_model,
        attemptPosition: job.attempt_position,
        errorCode: job.error?.code
      }
      const nodes = current.nodes.map((node) => node.id === nodeID
        ? { ...node, asset_id: first?.asset_id || node.asset_id, metadata }
        : node)
      const known = new Set(nodes.map((node) => node.id))
      for (const result of results.slice(1)) {
        const resultNodeID = `${nodeID}_result_${result.index}`
        if (known.has(resultNodeID)) {
          const index = nodes.findIndex((node) => node.id === resultNodeID)
          nodes[index] = { ...nodes[index], asset_id: result.asset_id, metadata: { ...metadata, resultIndex: result.index } }
          continue
        }
        known.add(resultNodeID)
        nodes.push({
          ...base,
          id: resultNodeID,
          asset_id: result.asset_id,
          position: {
            x: base.position.x + (base.size?.width || 220) + 36 + ((result.index - 1) % 2) * ((base.size?.width || 220) + 24),
            y: base.position.y + Math.floor((result.index - 1) / 2) * ((base.size?.height || 260) + 24)
          },
          metadata: { ...metadata, resultIndex: result.index }
        })
      }
      return { ...current, nodes }
    })
  }, [projects])

  const runGeneration = async () => {
    if (!document || !activeID || !apiKeyID || !model || !prompt.trim()) return
    const projectID = activeID
    const inputNode = operation === 'edit'
      ? document.nodes.find((node) => selected.includes(node.id) && node.type === 'image' && node.asset_id)
      : undefined
    if (operation === 'edit' && !inputNode?.asset_id) {
      hostRef.current.notify('warning', t('selectImage'))
      return
    }
    const runningID = `node_${nanoid()}`
    addNode('image', {
      id: runningID,
      prompt: prompt.trim(),
      metadata: { generationStatus: 'queued', selectedModel: model }
    })
    if (inputNode) {
      const edge = { id: `edge_${nanoid()}`, source: inputNode.id, target: runningID }
      commit((current) => ({ ...current, edges: [...current.edges, edge] }))
    }
    const aborter = new AbortController()
    const key = canvasMonitorKey(projectID, runningID)
    aborters.current.set(key, aborter)
    try {
      const job = await controller.run({
        project_id: projectID,
        client_node_id: runningID,
        operation,
        api_key_id: apiKeyID,
        selected_model: model,
        prompt: prompt.trim(),
        input_asset_ids: inputNode?.asset_id ? [inputNode.asset_id] : [],
        parameters: compactCanvasParameters(parameters, count),
        generationNonce: nanoid()
      }, aborter.signal, (snapshot) => {
        observedJobs.current.add(snapshot.id)
        applyJobResult(projectID, runningID, snapshot)
      })
      applyJobResult(projectID, runningID, job)
    } catch (error) {
      if (!aborter.signal.aborted) {
        hostRef.current.notify('error', readableError(error))
        projects.mutateDocument(projectID, (current) => ({
          ...current,
          nodes: current.nodes.map((node) => node.id === runningID ? {
            ...node,
            metadata: { ...node.metadata, generationStatus: 'failed', errorCode: readableError(error), selectedModel: model }
          } : node)
        }))
      }
    } finally {
      if (aborters.current.get(key) === aborter) aborters.current.delete(key)
    }
  }

  const cancelNodeJob = async (projectID: string, node: CanvasNodeDocument) => {
    const jobID = typeof node.metadata?.jobId === 'string' ? node.metadata.jobId : ''
    if (!jobID) return
    try {
      const job = await controller.cancel(jobID)
      applyJobResult(projectID, node.id, job)
      aborters.current.get(canvasMonitorKey(projectID, node.id))?.abort()
    } catch (error) {
      hostRef.current.notify('error', readableError(error))
    }
  }

  useEffect(() => {
    if (!activeID || !active?.open_jobs?.length) return
    for (const job of active.open_jobs) {
      const nodeID = job.client_node_id
      if (!nodeID || observedJobs.current.has(job.id)) continue
      observedJobs.current.add(job.id)
      const key = canvasMonitorKey(activeID, nodeID)
      const aborter = new AbortController()
      aborters.current.set(key, aborter)
      void controller.resume(job.id, aborter.signal, (snapshot) => applyJobResult(activeID, nodeID, snapshot))
        .catch((error) => {
          if (!aborter.signal.aborted) hostRef.current.notify('error', readableError(error))
        })
        .finally(() => {
          if (aborters.current.get(key) === aborter) aborters.current.delete(key)
        })
    }
  }, [active?.open_jobs, activeID, applyJobResult, controller])

  const exportProject = () => {
    if (!active) return
    const blob = new Blob([JSON.stringify({ name: active.name, document }, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const link = window.document.createElement('a')
    link.href = url
    link.download = `${active.name || 'canvas'}.json`
    link.click()
    URL.revokeObjectURL(url)
  }

  const importProject = async (file: File) => {
    try {
      const parsed = JSON.parse(await file.text()) as unknown
      const record = isRecord(parsed) ? parsed : undefined
      const imported = record && 'document' in record ? record.document : parsed
      if (!isCanvasDocument(imported)) throw new Error(t('invalidDocument'))
      const name = record && typeof record.name === 'string' ? record.name : t('untitled')
      const created = await api.createProject(name || t('untitled'), imported)
      await projects.openProject(created.id)
    } catch (error) {
      hostRef.current.notify('error', readableError(error))
    }
  }

  const createProject = async () => {
    setProjectBusy('new')
    try {
      await projects.createProject(t('untitled'))
      setSidebarOpen(false)
    } catch (error) {
      hostRef.current.notify('error', readableError(error))
    } finally {
      setProjectBusy('')
    }
  }

  const openProject = async (id: string) => {
    if (id === activeID) {
      setSidebarOpen(false)
      return
    }
    setProjectBusy(id)
    try {
      await projects.openProject(id)
      setSidebarOpen(false)
    } catch (error) {
      hostRef.current.notify('error', readableError(error))
    } finally {
      setProjectBusy('')
    }
  }

  const duplicateProject = async (id: string) => {
    const project = state.projectsByID[id]
    if (!project) return
    setProjectBusy(id)
    try {
      await projects.saveAsNew(id, `${project.name} ${t('copySuffix')}`)
      setSidebarOpen(false)
    } catch (error) {
      hostRef.current.notify('error', readableError(error))
    } finally {
      setProjectBusy('')
    }
  }

  const deleteProject = async (id: string) => {
    setProjectBusy(id)
    try {
      await projects.deleteProject(id)
      setDeleteCandidate('')
      setSidebarOpen(false)
    } catch (error) {
      hostRef.current.notify('error', readableError(error))
    } finally {
      setProjectBusy('')
    }
  }

  const sizes = capability?.sizes || []
  const maxOutputs = Math.max(1, capability?.max_outputs || 1)
  const sizeIsCustom = Boolean(capability?.custom_size && parameters.size && !sizes.includes(parameters.size))
  const runningCount = document?.nodes.filter((node) => isCanvasNodeRunning(node)).length || 0
  const updateParameter = <K extends keyof CanvasModelParameters,>(key: K, value: CanvasModelParameters[K]) => {
    setPresetID('')
    setParameters((current) => ({ ...current, [key]: value }))
  }
  const applyPreset = (id: string) => {
    setPresetID(id)
    const preset = capability?.presets?.find((item) => item.id === id)
    if (!preset) {
      setParameters({ ...(capability?.defaults || {}) })
      return
    }
    setParameters({ ...(capability?.defaults || {}), ...preset.parameters })
    const dimensions = parseCanvasSize(preset.parameters.size)
    if (dimensions) {
      setCustomWidth(dimensions.width)
      setCustomHeight(dimensions.height)
    }
  }
  const updateCustomSize = (width: number, height: number) => {
    const constraints = capability?.custom_size
    const step = Math.max(1, constraints?.multiple_of || 1)
    const max = Math.max(step, constraints?.max_edge || 3840)
    const normalizedWidth = Math.min(max, Math.max(step, Math.round(width / step) * step))
    const normalizedHeight = Math.min(max, Math.max(step, Math.round(height / step) * step))
    setCustomWidth(normalizedWidth)
    setCustomHeight(normalizedHeight)
    updateParameter('size', `${normalizedWidth}x${normalizedHeight}`)
  }
  const projectsLabel = sidebarOpen ? t('collapseProjects') : t('showProjects')

  return (
    <main className="sub2api-canvas-root" data-theme={host.theme}>
      {sidebarOpen && <button className="project-drawer-backdrop" aria-label={t('closeProjects')} onClick={() => setSidebarOpen(false)} />}
      <aside className={`canvas-projects ${sidebarOpen ? 'open' : 'closed'}`} data-canvas-no-zoom aria-label={t('projects')}>
        <div className="project-heading">
          <div className="project-title"><FolderKanban size={18} /><strong>{t('projects')}</strong></div>
          <div className="project-heading-actions">
            <IconButton icon={projectBusy === 'new' ? <LoaderCircle className="spin" /> : <Plus />} label={t('newProject')} onClick={() => void createProject()} disabled={Boolean(projectBusy)} />
            <IconButton icon={<X />} label={t('closeProjects')} onClick={() => setSidebarOpen(false)} />
          </div>
        </div>
        <label className="project-search">
          <Search size={16} />
          <input aria-label={t('searchProjects')} placeholder={t('searchProjects')} value={projectQuery} onChange={(event) => setProjectQuery(event.target.value)} />
          {projectQuery && <button type="button" aria-label={t('close')} title={t('close')} onClick={() => setProjectQuery('')}><X size={14} /></button>}
        </label>
        <div className="project-list">
          {visibleProjectIDs.map((id) => {
            const item = state.projectsByID[id]
            const projectDocument = item.localDraft || item.document
            const openJobs = item.open_jobs?.length || 0
            return <div key={id} className={`project-row ${id === activeID ? 'active' : ''}`}>
              <button className="project-row-main" onClick={() => void openProject(id)} disabled={Boolean(projectBusy)}>
                <span className="project-preview"><FileImage size={18} /><small>{projectDocument.nodes.length}</small></span>
                <span className="project-row-copy">
                  <strong>{item.name}</strong>
                  <small>{projectDocument.nodes.length} {t('nodes')}{openJobs > 0 ? ` · ${openJobs} ${t('runningJobs')}` : ''}</small>
                </span>
                <span className={`project-row-state ${item.saveState}`}>{saveLabel(item.saveState, t)}</span>
              </button>
              <div className="project-row-actions" aria-label={t('projectActions')}>
                <IconButton icon={<Copy />} label={t('duplicateProject')} onClick={() => void duplicateProject(id)} disabled={Boolean(projectBusy)} />
                <IconButton icon={<Trash2 />} label={t('deleteProject')} onClick={() => setDeleteCandidate(id)} disabled={Boolean(projectBusy)} />
              </div>
            </div>
          })}
          {!state.hydrated && <div className="project-loading"><LoaderCircle className="spin" size={22} /></div>}
          {state.hydrated && state.order.length === 0 && <div className="project-empty"><FileImage size={28} /><span>{t('noProject')}</span><button className="canvas-command primary" onClick={() => void createProject()}>{t('newProject')}</button></div>}
          {state.hydrated && state.order.length > 0 && visibleProjectIDs.length === 0 && <div className="project-empty compact"><Search size={24} /><span>{t('noProjectResults')}</span></div>}
        </div>
        <div className="project-footer">
          <button className="canvas-command" onClick={() => importInputRef.current?.click()}><Upload size={16} /><span>{t('import')}</span></button>
          <button className="canvas-command" onClick={exportProject} disabled={!active}><Download size={16} /><span>{t('export')}</span></button>
          <IconButton icon={<CircleHelp />} label={t('source')} onClick={() => setSourceOpen(true)} />
        </div>
      </aside>

      <section className="canvas-workspace">
        <header className="canvas-toolbar" data-canvas-no-zoom>
          <div className="toolbar-cluster canvas-filebar">
            <button className="project-switcher" title={projectsLabel} aria-label={projectsLabel} onClick={() => setSidebarOpen(!sidebarOpen)}>
              <FolderKanban size={17} /><span>{t('projects')}</span><ChevronDown size={14} />
            </button>
            {active && <input className="project-name" aria-label={t('projectName')} value={nameDraft}
              onChange={(event) => setNameDraft(event.target.value)}
              onKeyDown={(event) => { if (event.key === 'Enter') event.currentTarget.blur() }}
              onBlur={() => void projects.renameProject(active.id, nameDraft).catch((error) => hostRef.current.notify('error', readableError(error)))} />}
            <span className={`save-state ${active?.saveState || 'idle'}`}><Save size={14} />{active ? saveLabel(active.saveState, t) : ''}</span>
          </div>
          <div className="toolbar-cluster canvas-top-actions">
            <IconButton icon={inspectorOpen ? <PanelRightClose /> : <WandSparkles />} label={inspectorOpen ? t('hideInspector') : t('showInspector')} onClick={() => setInspectorOpen(!inspectorOpen)} />
            <IconButton icon={<CircleHelp />} label={t('source')} onClick={() => setSourceOpen(true)} />
          </div>
        </header>

        {active?.saveState === 'conflict' && <div className="canvas-conflict" data-canvas-no-zoom>
          <span>{t('conflictActions')}</span>
          <button className="canvas-command" onClick={() => void projects.reloadServer(active.id)}>{t('reload')}</button>
          <button className="canvas-command" onClick={() => void projects.saveAsNew(active.id, `${active.name} ${t('copySuffix')}`)}>{t('saveCopy')}</button>
        </div>}

        <div className="canvas-stage">
          {document ? <InfiniteCanvas containerRef={canvasRef} viewport={viewport} onViewportChange={setViewport} onCanvasDeselect={() => setSelected([])} onCanvasDoubleClick={(event) => {
            const rect = canvasRef.current?.getBoundingClientRect()
            if (!rect) return
            addNode('text', { position: { x: (event.clientX - rect.left - viewport.x) / viewport.k, y: (event.clientY - rect.top - viewport.y) / viewport.k }, text: '' })
          }}>
            <CanvasEdges document={document} />
            {document.nodes.slice().sort((a, b) => Number(a.type !== 'group') - Number(b.type !== 'group')).map((node) =>
              <CanvasNode key={node.id} node={node} selected={selected.includes(node.id)} api={api} onSelect={(event) => {
                event.stopPropagation()
                setSelected((current) => event.shiftKey ? (current.includes(node.id) ? current.filter((id) => id !== node.id) : [...current, node.id]) : [node.id])
              }} onDragStart={(event) => {
                event.preventDefault()
                event.stopPropagation()
                const ids = selected.includes(node.id) ? selected : [node.id]
                if (!selected.includes(node.id)) setSelected(ids)
                const original = new Map(document.nodes.filter((item) => ids.includes(item.id)).map((item) => [item.id, { ...item.position }]))
                setDrag({
                  ids,
                  startX: event.clientX,
                  startY: event.clientY,
                  scale: viewport.k,
                  original,
                  before: structuredClone(document)
                })
              }} onChange={(patch) => updateNode(node.id, patch)} onCancel={() => { if (activeID) void cancelNodeJob(activeID, node) }} />
            )}
          </InfiniteCanvas> : <div className="canvas-empty"><Box size={36} /><button className="canvas-command primary" onClick={() => void createProject()}>{t('newProject')}</button></div>}

          {selectionToolbarStyle && <div className="selection-toolbar" style={selectionToolbarStyle} data-canvas-no-zoom>
            {selected.length > 1 && <span className="selection-count">{selected.length} {t('selectedCount')}</span>}
            {selectedImage?.asset_id && <IconButton icon={<WandSparkles />} label={t('editSelected')} onClick={() => { setOperation('edit'); setInspectorOpen(true) }} />}
            {selected.length === 2 && <IconButton icon={<Link2 />} label={t('connect')} onClick={connectSelected} />}
            <IconButton icon={<Trash2 />} label={t('delete')} onClick={removeSelected} />
          </div>}

          <div className={`canvas-tool-dock ${inspectorOpen ? 'inspector-open' : ''}`} role="toolbar" aria-label={t('canvasTools')} data-canvas-no-zoom>
            <IconButton icon={<Undo2 />} label={t('undo')} onClick={undo} disabled={!history.current.past.length} />
            <IconButton icon={<Redo2 />} label={t('redo')} onClick={redo} disabled={!history.current.future.length} dataVersion={historyVersion} />
            <span className="toolbar-divider" />
            <IconButton icon={<Type />} label={t('addText')} onClick={() => addNode('text', { text: '' })} disabled={!active} />
            <IconButton icon={uploading ? <LoaderCircle className="spin" /> : <ImagePlus />} label={t('addImage')} onClick={() => fileInputRef.current?.click()} disabled={uploading || !active} />
            <IconButton icon={<Braces />} label={t('addConfig')} onClick={() => addNode('config', { text: '{}' })} disabled={!active} />
            <IconButton icon={<Group />} label={t('addGroup')} onClick={() => addNode('group', { text: t('groupDefault') })} disabled={!active} />
            <span className="toolbar-divider" />
            <IconButton icon={<ZoomOut />} label={t('zoomOut')} onClick={() => setViewport({ ...viewport, k: Math.max(.1, viewport.k / 1.2) })} disabled={!active} />
            <span className="zoom-value">{Math.round(viewport.k * 100)}%</span>
            <IconButton icon={<ZoomIn />} label={t('zoomIn')} onClick={() => setViewport({ ...viewport, k: Math.min(5, viewport.k * 1.2) })} disabled={!active} />
            <IconButton icon={<Focus />} label={t('fit')} onClick={fitView} disabled={!active} />
          </div>
        </div>

        {inspectorOpen && <aside className="generation-panel" data-canvas-no-zoom aria-label={t('imageStudio')}>
          <header className="generation-header">
            <div><WandSparkles size={17} /><strong>{t('imageStudio')}</strong><span>{operation === 'edit' ? t('imageEdit') : t('generation')}</span></div>
            <IconButton icon={<PanelRightClose />} label={t('hideInspector')} onClick={() => setInspectorOpen(false)} />
          </header>
          <div className="generation-scroll">
            <div className="generation-mode" role="group" aria-label={t('operation')}>
              <button className={operation === 'generation' ? 'active' : ''} onClick={() => setOperation('generation')}>{t('generation')}</button>
              <button className={operation === 'edit' ? 'active' : ''} onClick={() => setOperation('edit')}>{t('imageEdit')}</button>
            </div>
            <APIKeyModelSelector config={config} operation={operation} apiKeyID={apiKeyID} model={model} onAPIKeyChange={(id) => setSelection(id)} onModelChange={(value) => setSelection(apiKeyID, value)} onCreateKey={() => host.navigate('/keys')} />
            <label className="prompt-field"><span>{t('prompt')}</span><textarea aria-label={t('prompt')} placeholder={t('prompt')} value={prompt} onChange={(event) => setPrompt(event.target.value)} /></label>
            <section className="generation-settings">
              <div className="generation-settings-heading"><SlidersHorizontal size={15} /><span>{t('generationSettings')}</span></div>
              <div className="generation-parameters">
                {!!capability?.presets?.length && <label className="compact-field"><span>{t('preset')}</span><select value={presetID} onChange={(event) => applyPreset(event.target.value)}><option value="">{t('defaultPreset')}</option>{capability.presets.map((item) => <option key={item.id} value={item.id}>{item.label}{item.experimental ? ` · ${t('experimental')}` : ''}</option>)}</select></label>}
                {capability?.dimension_mode === 'size' && <>
                  <label className="compact-field"><span>{t('size')}</span><select value={sizeIsCustom ? '__custom' : (parameters.size || '')} onChange={(event) => event.target.value === '__custom' ? updateCustomSize(customWidth, customHeight) : updateParameter('size', event.target.value)}>{sizes.map((item) => <option key={item} value={item}>{item}</option>)}{capability.custom_size && <option value="__custom">{t('customSize')}</option>}</select></label>
                  {sizeIsCustom && <div className="custom-size-fields">
                    <label className="compact-field"><span>{t('width')}</span><input type="number" min={capability.custom_size?.multiple_of || 1} max={capability.custom_size?.max_edge || 3840} step={capability.custom_size?.multiple_of || 1} value={customWidth} onChange={(event) => updateCustomSize(Number(event.target.value), customHeight)} /></label>
                    <span aria-hidden="true">x</span>
                    <label className="compact-field"><span>{t('height')}</span><input type="number" min={capability.custom_size?.multiple_of || 1} max={capability.custom_size?.max_edge || 3840} step={capability.custom_size?.multiple_of || 1} value={customHeight} onChange={(event) => updateCustomSize(customWidth, Number(event.target.value))} /></label>
                  </div>}
                </>}
                {capability?.dimension_mode === 'aspect_ratio_resolution' && <>
                  {!!capability.aspect_ratios?.length && <label className="compact-field"><span>{t('aspectRatio')}</span><select value={parameters.aspect_ratio || ''} onChange={(event) => updateParameter('aspect_ratio', event.target.value)}>{capability.aspect_ratios.map((item) => <option key={item}>{item}</option>)}</select></label>}
                  {!!capability.resolutions?.length && <label className="compact-field"><span>{t('resolution')}</span><select value={parameters.resolution || ''} onChange={(event) => updateParameter('resolution', event.target.value)}>{capability.resolutions.map((item) => <option key={item}>{item}</option>)}</select></label>}
                </>}
                {!!capability?.qualities?.length && <label className="compact-field"><span>{t('quality')}</span><select value={parameters.quality || ''} onChange={(event) => updateParameter('quality', event.target.value)}>{capability.qualities.map((item) => <option key={item}>{item}</option>)}</select></label>}
                {!!capability?.output_formats?.length && <label className="compact-field"><span>{t('format')}</span><select value={parameters.output_format || ''} onChange={(event) => {
                  const outputFormat = event.target.value
                  setPresetID('')
                  setParameters((current) => ({
                    ...current,
                    output_format: outputFormat,
                    background: outputFormat === 'jpeg' && current.background === 'transparent' ? 'opaque' : current.background,
                    output_compression: outputFormat === 'jpeg' || outputFormat === 'webp' ? (current.output_compression ?? 90) : undefined
                  }))
                }}>{capability.output_formats.map((item) => <option key={item}>{item}</option>)}</select></label>}
                {!!capability?.backgrounds?.length && <label className="compact-field"><span>{t('background')}</span><select value={parameters.background || ''} onChange={(event) => updateParameter('background', event.target.value)}>{capability.backgrounds.filter((item) => parameters.output_format !== 'jpeg' || item !== 'transparent').map((item) => <option key={item}>{item}</option>)}</select></label>}
                {capability?.output_compression && (parameters.output_format === 'jpeg' || parameters.output_format === 'webp') && <label className="compact-field compression-field"><span>{t('compression')} {parameters.output_compression ?? 90}</span><input type="range" min={0} max={100} value={parameters.output_compression ?? 90} onChange={(event) => updateParameter('output_compression', Number(event.target.value))} /></label>}
                <label className="compact-field"><span>{t('outputCount')}</span><input type="number" min={1} max={maxOutputs} value={count} onChange={(event) => setCount(Math.max(1, Math.min(maxOutputs, Number(event.target.value))))} /></label>
              </div>
            </section>
          </div>
          <div className="generation-submit">
            <button className="generate-button" disabled={!active || !apiKeyID || !model || !prompt.trim()} onClick={() => void runGeneration()}><ImagePlus size={18} /><span>{operation === 'edit' ? t('edit') : t('generate')}</span>{runningCount > 0 && <small>{runningCount}</small>}</button>
          </div>
        </aside>}
      </section>

      <input ref={fileInputRef} hidden type="file" accept="image/png,image/jpeg,image/webp" onChange={(event) => { const file = event.target.files?.[0]; if (file) void uploadFile(file); event.target.value = '' }} />
      <input ref={importInputRef} hidden type="file" accept="application/json" onChange={(event) => { const file = event.target.files?.[0]; if (file) void importProject(file); event.target.value = '' }} />
      <SourceNoticeDialog open={sourceOpen} onClose={() => setSourceOpen(false)} />
      {deleteCandidate && state.projectsByID[deleteCandidate] && <div className="canvas-modal-backdrop" data-canvas-no-zoom onPointerDown={(event) => { if (event.target === event.currentTarget) setDeleteCandidate('') }}>
        <section className="canvas-modal confirm-modal" role="dialog" aria-modal="true" aria-labelledby="delete-project-title">
          <header>
            <div className="confirm-title"><Trash2 size={18} /><h2 id="delete-project-title">{t('deleteProjectTitle')}</h2></div>
            <IconButton icon={<X />} label={t('close')} onClick={() => setDeleteCandidate('')} />
          </header>
          <p>{t('deleteProjectBody')}</p>
          <strong className="confirm-project-name">{state.projectsByID[deleteCandidate].name}</strong>
          <footer>
            <button className="canvas-command" onClick={() => setDeleteCandidate('')} disabled={projectBusy === deleteCandidate}>{t('cancel')}</button>
            <button className="canvas-command danger" onClick={() => void deleteProject(deleteCandidate)} disabled={projectBusy === deleteCandidate}>
              {projectBusy === deleteCandidate ? <LoaderCircle className="spin" size={16} /> : <Trash2 size={16} />}<span>{t('deleteProject')}</span>
            </button>
          </footer>
        </section>
      </div>}
    </main>
  )
}

function IconButton({ icon, label, onClick, disabled, dataVersion: _ }: { icon: ReactElement; label: string; onClick(): void; disabled?: boolean; dataVersion?: number }) {
  return <button className="icon-button" title={label} aria-label={label} onClick={onClick} disabled={disabled}>{icon}</button>
}

function CanvasEdges({ document }: { document: CanvasDocument }) {
  const nodes = new Map(document.nodes.map((node) => [node.id, node]))
  return <svg className="canvas-edges" aria-hidden="true">
    {document.edges.map((edge) => {
      const from = nodes.get(edge.source); const to = nodes.get(edge.target)
      if (!from || !to) return null
      const x1 = from.position.x + (from.size?.width || 220); const y1 = from.position.y + (from.size?.height || 160) / 2
      const x2 = to.position.x; const y2 = to.position.y + (to.size?.height || 160) / 2
      const bend = Math.max(70, Math.abs(x2 - x1) * .45)
      return <path key={edge.id} d={`M ${x1} ${y1} C ${x1 + bend} ${y1}, ${x2 - bend} ${y2}, ${x2} ${y2}`} />
    })}
  </svg>
}

function CanvasNode({ node, selected, api, onSelect, onDragStart, onChange, onCancel }: {
  node: CanvasNodeDocument; selected: boolean; api: ReturnType<typeof createCanvasAPI>
  onSelect(event: ReactPointerEvent): void; onDragStart(event: ReactPointerEvent): void
  onChange(patch: Partial<CanvasNodeDocument>): void; onCancel(): void
}) {
  const t = useCanvasI18n()
  const status = String(node.metadata?.generationStatus || '')
  const loading = ['queued', 'running', 'preflight', 'upstream', 'falling_back', 'saving'].includes(status)
  return <article
    className={`canvas-node type-${node.type} ${selected ? 'selected' : ''}`}
    data-node-id={node.id}
    style={{ left: node.position.x, top: node.position.y, width: node.size?.width || 220, height: node.size?.height || 160 }}
    onPointerDown={onSelect}
  >
    <header onPointerDown={onDragStart}><span className="node-type">{nodeTypeLabel(node.type, t)}</span><span className={`node-status ${status}`}>{status.replace('_', ' ')}</span>{loading && <button className="node-cancel" onPointerDown={(event) => event.stopPropagation()} onClick={onCancel} aria-label={t('nodeCancel')} title={t('nodeCancel')}><X size={14} /></button>}</header>
    {node.type === 'image' && <div className="node-image">{node.asset_id ? <AssetImage assetID={node.asset_id} api={api} /> : loading ? <LoaderCircle className="spin" size={26} /> : <FileImage size={28} />}</div>}
    {node.type === 'text' && <textarea value={node.text || ''} placeholder={t('textPlaceholder')} onPointerDown={(event) => event.stopPropagation()} onChange={(event) => onChange({ text: event.target.value })} />}
    {node.type === 'config' && <textarea className="config-editor" value={node.text || '{}'} onPointerDown={(event) => event.stopPropagation()} onChange={(event) => onChange({ text: event.target.value })} />}
    {node.type === 'group' && <input value={node.text || t('groupDefault')} onPointerDown={(event) => event.stopPropagation()} onChange={(event) => onChange({ text: event.target.value })} />}
    {(node.prompt || node.asset_id) && node.type === 'image' && <div className="node-caption">{node.prompt || node.text}</div>}
  </article>
}

function nodeTypeLabel(type: CanvasNodeDocument['type'], t: ReturnType<typeof useCanvasI18n>): string {
  if (type === 'image') return t('nodeImage')
  if (type === 'text') return t('nodeText')
  if (type === 'config') return t('nodeConfig')
  return t('nodeGroup')
}

function AssetImage({ assetID, api }: { assetID: string; api: ReturnType<typeof createCanvasAPI> }) {
  const [url, setURL] = useState<string>()
  useEffect(() => {
    let current = ''
    const aborter = new AbortController()
    void fetchCanvasAsset(api, assetID, aborter.signal).then((value) => { current = value; setURL(value) }).catch(() => undefined)
    return () => { aborter.abort(); if (current) URL.revokeObjectURL(current) }
  }, [api, assetID])
  return url ? <img src={url} alt="" draggable={false} /> : <LoaderCircle className="spin" size={24} />
}

async function fetchCanvasAsset(api: ReturnType<typeof createCanvasAPI>, assetID: string, signal: AbortSignal): Promise<string> {
  return URL.createObjectURL(await api.getAssetBlob(assetID, signal))
}

function saveLabel(state: string, t: ReturnType<typeof useCanvasI18n>): string {
  if (state === 'saving') return t('saving')
  if (state === 'conflict') return t('conflict')
  if (state === 'error') return t('failed')
  return t('saved')
}

function readableError(error: unknown): string {
  if (error instanceof Error) return error.message
  return String(error || 'Unknown error')
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function isCanvasDocument(value: unknown): value is CanvasDocument {
  if (!isRecord(value)) return false
  return value.schema_version === 1 && Array.isArray(value.nodes) && Array.isArray(value.edges)
}

function compactCanvasParameters(parameters: CanvasModelParameters, count: number): CanvasModelParameters & { n: number } {
  const result: CanvasModelParameters & { n: number } = { n: count }
  for (const key of ['size', 'aspect_ratio', 'resolution', 'quality', 'output_format', 'background'] as const) {
    const value = parameters[key]
    if (typeof value === 'string' && value.trim()) result[key] = value.trim()
  }
  if (typeof parameters.output_compression === 'number') result.output_compression = parameters.output_compression
  return result
}

function parseCanvasSize(size?: string): { width: number; height: number } | undefined {
  const match = /^([1-9]\d*)x([1-9]\d*)$/i.exec(size?.trim() || '')
  if (!match) return undefined
  return { width: Number(match[1]), height: Number(match[2]) }
}

function canvasMonitorKey(projectID: string, nodeID: string): string {
  return `${projectID}:${nodeID}`
}

function isCanvasNodeRunning(node: CanvasNodeDocument): boolean {
  return ['queued', 'running', 'preflight', 'upstream', 'falling_back', 'saving'].includes(String(node.metadata?.generationStatus || ''))
}
