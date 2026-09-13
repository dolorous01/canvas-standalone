import localforage from 'localforage'
import { nanoid } from 'nanoid'
import { create } from 'zustand'

import i18n from '@/i18n'
import type { CanvasBackgroundMode } from '@/lib/canvas-theme'
import type {
  CanvasAssistantSession,
  CanvasConnection,
  CanvasNodeData,
  ViewportTransform
} from '@/types/canvas'
import { resolveStorageKeyAlias } from '@sub2api/adapters/asset-runtime'
import {
  createCanvasAPI,
  type CanvasDocument,
  type CanvasJob,
  type CanvasMediaTask,
  type CanvasProject as ServerProject
} from '@sub2api/api/canvas-api'
import {
  applyCanvasRecoverySnapshot,
  buildCanvasRecoveryBindings,
  isTerminalRecoverySnapshot,
  serverProjectRecoveries,
  settleCanvasRecoverySources,
  type CanvasRecoveryBinding,
  type CanvasRecoveryRecord
} from '@sub2api/jobs/canvas-task-recovery'
import { getCanvasRuntimeHost } from '@sub2api/runtime/host-runtime'

export type CanvasProject = {
  id: string
  title: string
  createdAt: string
  updatedAt: string
  nodes: CanvasNodeData[]
  connections: CanvasConnection[]
  chatSessions: CanvasAssistantSession[]
  activeChatId: string | null
  backgroundMode: CanvasBackgroundMode
  showImageInfo: boolean
  viewport: ViewportTransform
  recoveryRevision: number
  recoveries: CanvasRecoveryRecord[]
}

type CanvasProjectPatch = Partial<Pick<CanvasProject,
  'nodes' | 'connections' | 'chatSessions' | 'activeChatId' | 'backgroundMode' | 'showImageInfo' | 'viewport'>>

export type CanvasProjectSaveState = 'dirty' | 'saving' | 'saved' | 'error' | 'conflict'

export type CanvasProjectSaveStatus = {
  state: CanvasProjectSaveState
  error?: string
}

type CanvasStore = {
  hydrated: boolean
  projects: CanvasProject[]
  saveStatusByProject: Record<string, CanvasProjectSaveStatus>
  createProject: (title?: string) => string
  importProject: (project: Partial<CanvasProject>) => string
  openProject: (id: string) => CanvasProject | null
  renameProject: (id: string, title: string) => void
  deleteProjects: (ids: string[]) => void
  replaceProjects: (projects: CanvasProject[]) => void
  updateProject: (id: string, patch: CanvasProjectPatch) => void
  flushProject: (id: string) => Promise<void>
  retryProject: (id: string) => Promise<void>
  reloadServerProject: (id: string) => Promise<void>
  saveProjectAsNew: (id: string, title?: string) => string | undefined
}

type ProjectSync = {
  version: number
  sequence: number
  creating?: Promise<void>
  timer?: ReturnType<typeof setTimeout>
  saving: boolean
  currentSave?: Promise<void>
  retryAfterSave: boolean
  blocked: boolean
}

type DraftRecord = {
  baseVersion: number
  project: CanvasProject
  savedAt: number
}

const initialViewport: ViewportTransform = { x: 0, y: 0, k: 1 }
const drafts = localforage.createInstance({ name: 'sub2api-canvas-drafts-v2' })
const syncByProject = new Map<string, ProjectSync>()
const recoveriesByProject = new Map<string, CanvasRecoveryBinding[]>()
const recoveryControllers = new Map<string, AbortController>()
const recoveryCleanupTimers = new Map<string, ReturnType<typeof setTimeout>>()
let hydration: Promise<void> | undefined
let runtimeEpoch = 0
let activeProjectID: string | undefined

function setProjectSaveStatus(id: string, state: CanvasProjectSaveState, error?: string): void {
  useCanvasStore.setState((current) => ({
    saveStatusByProject: {
      ...current.saveStatusByProject,
      [id]: { state, ...(error ? { error } : {}) }
    }
  }))
}

function api() {
  return createCanvasAPI(getCanvasRuntimeHost())
}

function emptyProject(id: string, title: string): CanvasProject {
  const now = new Date().toISOString()
  return {
    id,
    title,
    createdAt: now,
    updatedAt: now,
    nodes: [],
    connections: [],
    chatSessions: [],
    activeChatId: null,
    backgroundMode: 'lines',
    showImageInfo: false,
    viewport: initialViewport,
    recoveryRevision: 0,
    recoveries: []
  }
}

function projectDocument(project: CanvasProject): CanvasDocument {
  return sanitizeProjectValue({
    schema_version: 2,
    nodes: project.nodes,
    connections: project.connections,
    chat_sessions: project.chatSessions,
    active_chat_id: project.activeChatId,
    background_mode: project.backgroundMode,
    show_image_info: project.showImageInfo,
    viewport: project.viewport,
    recoveries: project.recoveries
  }) as CanvasDocument
}

function serverProject(value: ServerProject): CanvasProject {
  const document = value.document as CanvasDocument & Record<string, unknown>
  if (document.schema_version === 1) return migrateLegacyProject(value)
  return {
    id: value.id,
    title: value.name,
    createdAt: value.created_at,
    updatedAt: value.updated_at,
    nodes: arrayValue<CanvasNodeData>(document.nodes),
    connections: arrayValue<CanvasConnection>(document.connections),
    chatSessions: arrayValue<CanvasAssistantSession>(document.chat_sessions),
    activeChatId: typeof document.active_chat_id === 'string' ? document.active_chat_id : null,
    backgroundMode: canvasBackgroundMode(document.background_mode),
    showImageInfo: document.show_image_info === true,
    viewport: viewportValue(document.viewport),
    recoveryRevision: 0,
    recoveries: canvasRecoveryRecords(document.recoveries)
  }
}

function migrateLegacyProject(value: ServerProject): CanvasProject {
  const document = value.document as CanvasDocument & Record<string, unknown>
  const nodes = arrayValue<Record<string, unknown>>(document.nodes).map((source) => {
    const position = objectValue(source.position)
    const size = objectValue(source.size)
    const metadata = { ...objectValue(source.metadata) }
    if (typeof source.prompt === 'string') metadata.prompt = source.prompt
    if (typeof source.text === 'string') metadata.content = source.text
    if (typeof source.asset_id === 'string' && source.asset_id) {
      metadata.storageKey = `asset:${source.asset_id}`
      metadata.content = ''
      metadata.status = 'success'
    }
    const type = typeof source.type === 'string' ? source.type : 'text'
    const defaults = legacyNodeSize(type)
    return {
      id: String(source.id || nanoid()),
      type,
      title: typeof source.title === 'string' ? source.title : legacyNodeTitle(type),
      position: { x: numberValue(position.x, 0), y: numberValue(position.y, 0) },
      width: numberValue(source.width ?? size.width, defaults.width),
      height: numberValue(source.height ?? size.height, defaults.height),
      metadata
    } as CanvasNodeData
  })
  const connections = arrayValue<Record<string, unknown>>(document.edges).map((edge) => ({
    id: String(edge.id || nanoid()),
    fromNodeId: String(edge.source || edge.fromNodeId || ''),
    toNodeId: String(edge.target || edge.toNodeId || '')
  })).filter((edge) => edge.fromNodeId && edge.toNodeId)
  return {
    id: value.id,
    title: value.name,
    createdAt: value.created_at,
    updatedAt: value.updated_at,
    nodes,
    connections,
    chatSessions: [],
    activeChatId: null,
    backgroundMode: 'lines',
    showImageInfo: false,
    viewport: viewportValue(document.viewport),
    recoveryRevision: 0,
    recoveries: canvasRecoveryRecords(document.recoveries)
  }
}

function scheduleSave(id: string, immediate = false): void {
  const sync = syncByProject.get(id)
  const project = useCanvasStore.getState().projects.find((item) => item.id === id)
  if (!sync || !project) return
  if (sync.timer) clearTimeout(sync.timer)
  void drafts.setItem(id, {
    baseVersion: sync.version,
    project: sanitizeProjectValue(project) as CanvasProject,
    savedAt: Date.now()
  } satisfies DraftRecord)
  if (sync.blocked) return
  setProjectSaveStatus(id, 'dirty')
  sync.timer = setTimeout(() => {
    sync.timer = undefined
    void persistProject(id)
  }, immediate ? 0 : 400)
}

async function persistProject(id: string): Promise<void> {
  const sync = syncByProject.get(id)
  if (!sync || sync.blocked) return
  if (sync.saving) {
    sync.retryAfterSave = true
    await sync.currentSave
    return
  }
  sync.saving = true
  setProjectSaveStatus(id, 'saving')
  const operation = (async () => {
    try {
      await sync.creating
      const project = useCanvasStore.getState().projects.find((item) => item.id === id)
      if (!project || !syncByProject.has(id)) return
      const sequence = sync.sequence
      const updated = await api().updateProject(id, sync.version, project.title, projectDocument(project))
      if (!syncByProject.has(id)) return
      sync.version = updated.version
      useCanvasStore.setState((state) => ({
        projects: state.projects.map((item) => item.id === id ? {
          ...item,
          title: updated.name,
          createdAt: updated.created_at,
          updatedAt: updated.updated_at
        } : item)
      }))
      if (sync.sequence === sequence) {
        await drafts.removeItem(id)
        setProjectSaveStatus(id, 'saved')
      } else {
        sync.retryAfterSave = true
        setProjectSaveStatus(id, 'dirty')
      }
    } catch (error) {
      const message = readableError(error, 'Project save failed')
      if (canvasErrorStatus(error) === 409 || canvasErrorCode(error) === 'project_version_conflict') {
        sync.blocked = true
        setProjectSaveStatus(id, 'conflict', message)
        getCanvasRuntimeHost().notify('warning', i18n.t('canvas.project.conflict', { defaultValue: 'This project changed elsewhere. Your local draft was kept.' }))
      } else {
        setProjectSaveStatus(id, 'error', message)
        getCanvasRuntimeHost().notify('error', message)
      }
    } finally {
      sync.saving = false
      sync.currentSave = undefined
      if (sync.retryAfterSave && !sync.blocked) {
        sync.retryAfterSave = false
        await persistProject(id)
      }
    }
  })()
  sync.currentSave = operation
  await operation
}

function beginProjectCreation(id: string, sync: ProjectSync): Promise<void> {
  const epoch = runtimeEpoch
  const creating = (async () => {
    const project = useCanvasStore.getState().projects.find((item) => item.id === id)
    if (!project) throw new Error('Project not found')
    try {
      const created = await api().createProject(project.title, projectDocument(project), id)
      sync.version = created.version
      sync.blocked = false
      if (epoch !== runtimeEpoch || !syncByProject.has(id)) return
      const current = useCanvasStore.getState().projects.find((item) => item.id === id)
      useCanvasStore.setState((state) => ({
        projects: state.projects.map((item) => item.id === id ? {
          ...(current || serverProject(created)),
          createdAt: created.created_at,
          updatedAt: created.updated_at
        } : item)
      }))
      if (sync.sequence === 0) {
        await drafts.removeItem(id)
        setProjectSaveStatus(id, 'saved')
      } else {
        scheduleSave(id, true)
      }
    } catch (error) {
      if (epoch !== runtimeEpoch || !syncByProject.has(id)) return
      sync.blocked = true
      const message = readableError(error, 'Project creation failed')
      setProjectSaveStatus(id, 'error', message)
      getCanvasRuntimeHost().notify('error', message)
      throw error
    }
  })()
  sync.creating = creating
  void creating.catch(() => undefined)
  return creating
}

async function retryProjectCreation(id: string, sync: ProjectSync): Promise<void> {
  sync.blocked = false
  setProjectSaveStatus(id, 'saving')
  try {
    const existing = await api().getProject(id)
    if (!syncByProject.has(id)) return
    sync.version = existing.version
    sync.creating = Promise.resolve()
    useCanvasStore.setState((state) => ({
      projects: state.projects.map((project) => project.id === id ? {
        ...project,
        createdAt: existing.created_at,
        updatedAt: existing.updated_at
      } : project)
    }))
    setProjectSaveStatus(id, 'dirty')
    await persistProject(id)
  } catch (error) {
    if (canvasErrorStatus(error) === 404) {
      await beginProjectCreation(id, sync)
      return
    }
    sync.blocked = true
    const message = readableError(error, 'Project creation retry failed')
    setProjectSaveStatus(id, 'error', message)
    getCanvasRuntimeHost().notify('error', message)
  }
}

function createOptimisticProject(source: Partial<CanvasProject>, title?: string): string {
  const id = nanoid()
  const base = emptyProject(id, title || source.title || i18n.t('canvas.project.untitled'))
  const project: CanvasProject = {
    ...base,
    ...source,
    id,
    title: title || source.title || base.title,
    createdAt: source.createdAt || base.createdAt,
    updatedAt: base.updatedAt,
    nodes: source.nodes || [],
    connections: source.connections || [],
    chatSessions: source.chatSessions || [],
    activeChatId: source.activeChatId || null,
    backgroundMode: source.backgroundMode || 'lines',
    showImageInfo: source.showImageInfo || false,
    viewport: source.viewport || initialViewport,
    recoveryRevision: 0,
    recoveries: source.recoveries || []
  }
  const sync: ProjectSync = { version: 0, sequence: 0, saving: false, retryAfterSave: false, blocked: false }
  syncByProject.set(id, sync)
  useCanvasStore.setState((state) => ({ projects: [project, ...state.projects] }))
  setProjectSaveStatus(id, 'saving')
  beginProjectCreation(id, sync)
  return id
}

export const useCanvasStore = create<CanvasStore>()((set, get) => ({
  hydrated: false,
  projects: [],
  saveStatusByProject: {},
  createProject: (title) => createOptimisticProject({}, title),
  importProject: (project) => createOptimisticProject(project),
  openProject: (id) => {
    activeProjectID = id
    return get().projects.find((item) => item.id === id) || null
  },
  renameProject: (id, title) => {
    const normalized = title.trim()
    if (!normalized) return
    set((state) => ({
      projects: state.projects.map((project) => project.id === id ? {
        ...project,
        title: normalized,
        updatedAt: new Date().toISOString()
      } : project)
    }))
    const sync = syncByProject.get(id)
    if (sync) sync.sequence += 1
    scheduleSave(id)
  },
  deleteProjects: (ids) => {
    const deleting = new Set(ids)
    const removed = get().projects.filter((project) => deleting.has(project.id))
    const removedSync = new Map<string, ProjectSync>()
    for (const id of ids) {
      const sync = syncByProject.get(id)
      if (sync) removedSync.set(id, sync)
      if (sync?.timer) clearTimeout(sync.timer)
      syncByProject.delete(id)
      clearProjectRecoveries(id)
    }
    set((state) => ({
      projects: state.projects.filter((project) => !deleting.has(project.id)),
      saveStatusByProject: Object.fromEntries(
        Object.entries(state.saveStatusByProject).filter(([id]) => !deleting.has(id))
      )
    }))
    void Promise.all(removed.map(async (project) => {
      const sync = removedSync.get(project.id)
      try {
        await sync?.creating
        if ((sync?.version || 0) > 0) await api().deleteProject(project.id)
        await drafts.removeItem(project.id)
      } catch (error) {
        set((state) => state.projects.some((item) => item.id === project.id)
          ? state
          : { projects: [project, ...state.projects] })
        syncByProject.set(project.id, {
          version: sync?.version || 1,
          sequence: sync?.sequence || 0,
          saving: false,
          retryAfterSave: false,
          blocked: true
        })
        const message = readableError(error, 'Project deletion failed')
        setProjectSaveStatus(project.id, 'error', message)
        getCanvasRuntimeHost().notify('error', message)
      }
    }))
  },
  replaceProjects: (projects) => set({ projects }),
  updateProject: (id, patch) => {
    set((state) => ({
      projects: state.projects.map((project) => project.id === id ? {
        ...project,
        ...patch,
        updatedAt: new Date().toISOString()
      } : project)
    }))
    const sync = syncByProject.get(id)
    if (sync) sync.sequence += 1
    scheduleSave(id)
  },
  flushProject: async (id) => {
    const sync = syncByProject.get(id)
    if (!sync || sync.blocked) return
    if (sync.timer) {
      clearTimeout(sync.timer)
      sync.timer = undefined
    }
    if (sync.saving) {
      await sync.currentSave
      return
    }
    const status = useCanvasStore.getState().saveStatusByProject[id]?.state
    if (status !== 'saved') await persistProject(id)
  },
  retryProject: async (id) => {
    const sync = syncByProject.get(id)
    if (!sync) return
    const status = useCanvasStore.getState().saveStatusByProject[id]?.state
    if (status === 'conflict') return
    if (sync.version < 1) {
      await retryProjectCreation(id, sync)
      return
    }
    sync.blocked = false
    if (sync.timer) {
      clearTimeout(sync.timer)
      sync.timer = undefined
    }
    await persistProject(id)
  },
  reloadServerProject: async (id) => {
    const sync = syncByProject.get(id)
    if (!sync || sync.saving) return
    if (sync.timer) {
      clearTimeout(sync.timer)
      sync.timer = undefined
    }
    setProjectSaveStatus(id, 'saving')
    try {
      const value = await api().getProject(id)
      if (!syncByProject.has(id)) return
      clearProjectRecoveries(id)
      sync.version = value.version
      sync.sequence = 0
      sync.blocked = false
      sync.retryAfterSave = false
      sync.creating = Promise.resolve()
      await drafts.removeItem(id)
      set((state) => ({
        projects: state.projects.map((project) => project.id === id ? serverProject(value) : project)
      }))
      setProjectSaveStatus(id, 'saved')
    } catch (error) {
      const message = readableError(error, 'Server project could not be loaded')
      setProjectSaveStatus(id, 'error', message)
      getCanvasRuntimeHost().notify('error', message)
    }
  },
  saveProjectAsNew: (id, title) => {
    const project = get().projects.find((item) => item.id === id)
    if (!project) return undefined
    const nextTitle = title?.trim() || `${project.title} ${i18n.t('canvas.project.copySuffix', { defaultValue: 'copy' })}`
    return createOptimisticProject(project, nextTitle)
  }
}))

export function initializeCanvasProjectStore(): Promise<void> {
  if (hydration) return hydration
  const epoch = runtimeEpoch
  hydration = (async () => {
    try {
      const values = await api().listProjects()
      if (epoch !== runtimeEpoch) return
      const projects: CanvasProject[] = []
      const saveStatusByProject: Record<string, CanvasProjectSaveStatus> = {}
      const resumableDraftIDs: string[] = []
      for (const value of values) {
        let project = serverProject(value)
        const draft = await drafts.getItem<DraftRecord>(value.id)
        const blocked = Boolean(draft && draft.baseVersion !== value.version)
        if (draft?.project) {
          project = {
            ...draft.project,
            id: value.id,
            createdAt: value.created_at,
            recoveryRevision: 0
          }
        }
        project.recoveries = canvasRecoveryRecords(project.recoveries).filter((record) => {
          const target = project.nodes.find((node) => node.id === record.targetNodeID)
          if (!target) return false
          return !record.imageID || target.metadata?.images?.some((image) => image.id === record.imageID)
        })
        const persistedRecoveries = project.recoveries
        const snapshots = serverProjectRecoveries(value)
        const snapshotByID = new Map<string, CanvasJob | CanvasMediaTask>([
          ...snapshots.jobs.map((snapshot) => [snapshot.id, snapshot] as const),
          ...snapshots.mediaTasks.map((snapshot) => [snapshot.id, snapshot] as const)
        ])
        const persistedBindings: CanvasRecoveryBinding[] = persistedRecoveries.map((record) => ({
          ...record,
          snapshot: snapshotByID.get(record.taskID) || pendingRecoverySnapshot(record)
        }))
        const persistedIDs = new Set(persistedBindings.map((binding) => binding.taskID))
        const inferredBindings = buildCanvasRecoveryBindings(
          project,
          snapshots.jobs.filter((snapshot) => !persistedIDs.has(snapshot.id)),
          snapshots.mediaTasks.filter((snapshot) => !persistedIDs.has(snapshot.id)),
          persistedRecoveries
        )
        const bindings = [...persistedBindings, ...inferredBindings]
        for (const binding of bindings) {
          if (isTerminalRecoverySnapshot(binding.snapshot)) {
            project = applyCanvasRecoverySnapshot(project, binding, binding.snapshot)
          }
        }
        const activeBindings = bindings.filter((binding) => !isTerminalRecoverySnapshot(binding.snapshot))
        const activeRecords = activeBindings.map(recoveryRecord)
        const recoveryDocumentChanged = JSON.stringify(activeRecords) !== JSON.stringify(persistedRecoveries)
        project = { ...project, recoveries: activeRecords }
        project = settleCanvasRecoverySources(
          project,
          new Set(bindings.map((binding) => binding.sourceNodeID)),
          activeBindings
        )
        if (activeBindings.length > 0) recoveriesByProject.set(value.id, activeBindings)
        syncByProject.set(value.id, {
          version: value.version,
          sequence: draft || recoveryDocumentChanged ? 1 : 0,
          saving: false,
          retryAfterSave: false,
          blocked
        })
        saveStatusByProject[value.id] = {
          state: blocked ? 'conflict' : draft || recoveryDocumentChanged ? 'dirty' : 'saved'
        }
        projects.push(project)
        if ((draft || recoveryDocumentChanged) && !blocked) resumableDraftIDs.push(value.id)
      }
      if (epoch === runtimeEpoch) {
        useCanvasStore.setState({ projects, saveStatusByProject, hydrated: true })
        resumableDraftIDs.forEach((id) => scheduleSave(id, true))
        for (const [projectID, bindings] of recoveriesByProject) {
          bindings.forEach((binding) => startCanvasRecoveryMonitor(projectID, binding, epoch))
        }
      }
    } catch (error) {
      if (epoch === runtimeEpoch) {
        useCanvasStore.setState({ hydrated: true })
        getCanvasRuntimeHost().notify('error', readableError(error, 'Projects could not be loaded'))
      }
    }
  })()
  return hydration
}

export function resetCanvasProjectStore(): void {
  runtimeEpoch += 1
  activeProjectID = undefined
  hydration = undefined
  for (const sync of syncByProject.values()) if (sync.timer) clearTimeout(sync.timer)
  syncByProject.clear()
  for (const controller of recoveryControllers.values()) controller.abort()
  recoveryControllers.clear()
  for (const timer of recoveryCleanupTimers.values()) clearTimeout(timer)
  recoveryCleanupTimers.clear()
  recoveriesByProject.clear()
  useCanvasStore.setState({ hydrated: false, projects: [], saveStatusByProject: {} })
}

export function getActiveCanvasProjectID(): string | undefined {
  return activeProjectID
}

export function mostRecentCanvasProject(projects: readonly CanvasProject[]): CanvasProject | undefined {
  let latest: CanvasProject | undefined
  let latestTimestamp = Number.NEGATIVE_INFINITY
  for (const project of projects) {
    const parsed = Date.parse(project.updatedAt)
    const timestamp = Number.isFinite(parsed) ? parsed : Number.NEGATIVE_INFINITY
    if (!latest || timestamp > latestTimestamp) {
      latest = project
      latestTimestamp = timestamp
    }
  }
  return latest
}

export function isCanvasRecoveryActive(nodeID: string, imageID?: string): boolean {
  if (!activeProjectID) return false
  return (recoveriesByProject.get(activeProjectID) || []).some((binding) => {
    if (imageID) {
      return binding.targetNodeID === nodeID && (!binding.imageID || binding.imageID === imageID)
    }
    return binding.sourceNodeID === nodeID || binding.targetNodeID === nodeID
  })
}

export function registerCanvasTaskRecovery(
  snapshot: CanvasJob | CanvasMediaTask,
  kind: CanvasRecoveryRecord['kind']
): void {
  const projectID = activeProjectID
  if (!projectID || !snapshot?.id) return
  const project = useCanvasStore.getState().projects.find((item) => item.id === projectID)
  if (!project || project.recoveries.some((record) => record.taskID === snapshot.id)) return
  const bindings = buildCanvasRecoveryBindings(
    project,
    kind === 'image' ? [snapshot as CanvasJob] : [],
    kind === 'image' ? [] : [snapshot as CanvasMediaTask],
    project.recoveries
  )
  const binding = bindings[0]
  if (!binding) return
  const record = recoveryRecord(binding)
  recoveriesByProject.set(projectID, [...(recoveriesByProject.get(projectID) || []), binding])
  useCanvasStore.setState((state) => ({
    projects: state.projects.map((item) => item.id === projectID
      ? { ...item, recoveries: [...item.recoveries, record] }
      : item)
  }))
  const sync = syncByProject.get(projectID)
  if (sync) sync.sequence += 1
  scheduleSave(projectID, true)
}

export function scheduleCanvasTaskRecoveryCleanup(taskID: string, delay = 5000): void {
  const projectID = useCanvasStore.getState().projects
    .find((project) => project.recoveries.some((record) => record.taskID === taskID))?.id || activeProjectID
  if (!projectID || !taskID) return
  const key = `${projectID}\u0000${taskID}`
  const previous = recoveryCleanupTimers.get(key)
  if (previous) clearTimeout(previous)
  recoveryCleanupTimers.set(key, setTimeout(() => {
    recoveryCleanupTimers.delete(key)
    const project = useCanvasStore.getState().projects.find((item) => item.id === projectID)
    if (!project?.recoveries.some((record) => record.taskID === taskID)) return
    recoveriesByProject.set(
      projectID,
      (recoveriesByProject.get(projectID) || []).filter((binding) => binding.taskID !== taskID)
    )
    useCanvasStore.setState((state) => ({
      projects: state.projects.map((item) => item.id === projectID
        ? { ...item, recoveries: item.recoveries.filter((record) => record.taskID !== taskID) }
        : item)
    }))
    const sync = syncByProject.get(projectID)
    if (sync) sync.sequence += 1
    scheduleSave(projectID, true)
  }, delay))
}

function startCanvasRecoveryMonitor(projectID: string, binding: CanvasRecoveryBinding, epoch: number): void {
  const key = `${projectID}\u0000${binding.taskID}`
  if (recoveryControllers.has(key)) return
  const controller = new AbortController()
  recoveryControllers.set(key, controller)
  void (async () => {
    let delay = 1500
    try {
      while (epoch === runtimeEpoch && !controller.signal.aborted) {
        await waitForRecovery(delay, controller.signal)
        try {
          const snapshot = binding.kind === 'image'
            ? await api().getJob(binding.taskID)
            : await api().getMediaTask(binding.taskID, controller.signal)
          binding.snapshot = snapshot
          if (isTerminalRecoverySnapshot(snapshot)) {
            finishCanvasRecovery(projectID, binding, snapshot)
            return
          }
          delay = 2500
        } catch (error) {
          if (controller.signal.aborted || epoch !== runtimeEpoch) return
          delay = Math.min(10_000, delay * 2)
        }
      }
    } finally {
      recoveryControllers.delete(key)
    }
  })()
}

function finishCanvasRecovery(
  projectID: string,
  binding: CanvasRecoveryBinding,
  snapshot: CanvasRecoveryBinding['snapshot']
): void {
  const remaining = (recoveriesByProject.get(projectID) || [])
    .filter((candidate) => candidate.taskID !== binding.taskID)
  if (remaining.length > 0) recoveriesByProject.set(projectID, remaining)
  else recoveriesByProject.delete(projectID)
  useCanvasStore.setState((state) => ({
    projects: state.projects.map((project) => {
      if (project.id !== projectID) return project
      const recovered = applyCanvasRecoverySnapshot(project, binding, snapshot)
      const settled = settleCanvasRecoverySources(recovered, [binding.sourceNodeID], remaining)
      return {
        ...settled,
        recoveries: project.recoveries.filter((record) => record.taskID !== binding.taskID),
        recoveryRevision: project.recoveryRevision + 1
      }
    })
  }))
  const sync = syncByProject.get(projectID)
  if (sync) sync.sequence += 1
  scheduleSave(projectID, true)
}

function clearProjectRecoveries(projectID: string): void {
  recoveriesByProject.delete(projectID)
  const prefix = `${projectID}\u0000`
  for (const [key, controller] of recoveryControllers) {
    if (!key.startsWith(prefix)) continue
    controller.abort()
    recoveryControllers.delete(key)
  }
  for (const [key, timer] of recoveryCleanupTimers) {
    if (!key.startsWith(prefix)) continue
    clearTimeout(timer)
    recoveryCleanupTimers.delete(key)
  }
}

function waitForRecovery(milliseconds: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(signal.reason)
      return
    }
    const onAbort = () => {
      clearTimeout(timer)
      reject(signal.reason)
    }
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', onAbort)
      resolve()
    }, milliseconds)
    signal.addEventListener('abort', onAbort, { once: true })
  })
}

function recoveryRecord(binding: CanvasRecoveryBinding): CanvasRecoveryRecord {
  return {
    taskID: binding.taskID,
    kind: binding.kind,
    sourceNodeID: binding.sourceNodeID,
    targetNodeID: binding.targetNodeID,
    ...(binding.imageID ? { imageID: binding.imageID } : {})
  }
}

function pendingRecoverySnapshot(record: CanvasRecoveryRecord): CanvasJob | CanvasMediaTask {
  if (record.kind === 'image') {
    return {
      id: record.taskID,
      status: 'queued',
      operation: 'generation',
      client_node_id: record.sourceNodeID,
      attempt_plan: [],
      requested_count: 1,
      completed_count: 0,
      results: []
    }
  }
  return {
    id: record.taskID,
    kind: record.kind,
    status: 'queued',
    project_id: '',
    client_node_id: record.sourceNodeID,
    selected_model: '',
    results: []
  }
}

function sanitizeProjectValue(value: unknown): unknown {
  if (typeof value === 'string' && /^(blob:|data:)/i.test(value)) return ''
  if (Array.isArray(value)) return value.map(sanitizeProjectValue)
  if (!value || typeof value !== 'object') return value
  const source = value as Record<string, unknown>
  const durable = typeof source.storageKey === 'string' && source.storageKey.startsWith('asset:')
  const result: Record<string, unknown> = {}
  for (const [key, child] of Object.entries(source)) {
    if (typeof child === 'string' && /^(blob:|data:)/i.test(child)) {
      result[key] = ''
      continue
    }
    if (durable && typeof child === 'string' && /^(https?:)?\/\//i.test(child) && ['content', 'dataUrl', 'url', 'coverUrl'].includes(key)) {
      result[key] = ''
      continue
    }
    if (key === 'storageKey' && typeof child === 'string') {
      result[key] = resolveStorageKeyAlias(child)
    } else if (key === 'references' && Array.isArray(child)) {
      result[key] = child.map((item) => typeof item === 'string' ? resolveStorageKeyAlias(item) : sanitizeProjectValue(item))
    } else {
      result[key] = sanitizeProjectValue(child)
    }
  }
  return result
}

function canvasRecoveryRecords(value: unknown): CanvasRecoveryRecord[] {
  if (!Array.isArray(value)) return []
  const records: CanvasRecoveryRecord[] = []
  const seen = new Set<string>()
  for (const item of value.slice(0, 100)) {
    const source = objectValue(item)
    const taskID = typeof source.taskID === 'string' ? source.taskID.trim() : ''
    const kind = source.kind
    const sourceNodeID = typeof source.sourceNodeID === 'string' ? source.sourceNodeID.trim() : ''
    const targetNodeID = typeof source.targetNodeID === 'string' ? source.targetNodeID.trim() : ''
    const imageID = typeof source.imageID === 'string' ? source.imageID.trim() : undefined
    if (!taskID || taskID.length > 128 || seen.has(taskID) ||
      (kind !== 'image' && kind !== 'video' && kind !== 'audio') ||
      !sourceNodeID || sourceNodeID.length > 128 || !targetNodeID || targetNodeID.length > 128 ||
      (imageID && imageID.length > 128)) continue
    seen.add(taskID)
    records.push({ taskID, kind, sourceNodeID, targetNodeID, ...(imageID ? { imageID } : {}) })
  }
  return records
}

function arrayValue<T>(value: unknown): T[] {
  return Array.isArray(value) ? value as T[] : []
}

function objectValue(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function numberValue(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

function viewportValue(value: unknown): ViewportTransform {
  const viewport = objectValue(value)
  return { x: numberValue(viewport.x, 0), y: numberValue(viewport.y, 0), k: numberValue(viewport.k, 1) }
}

function canvasBackgroundMode(value: unknown): CanvasBackgroundMode {
  return value === 'dots' || value === 'blank' ? value : 'lines'
}

function legacyNodeSize(type: string): { width: number; height: number } {
  if (type === 'group') return { width: 520, height: 360 }
  if (type === 'text') return { width: 320, height: 220 }
  if (type === 'config') return { width: 360, height: 300 }
  return { width: 360, height: 360 }
}

function legacyNodeTitle(type: string): string {
  return type.charAt(0).toUpperCase() + type.slice(1)
}

function canvasErrorStatus(error: unknown): number | undefined {
  const value = error as { status?: number; response?: { status?: number } } | null
  return value?.status ?? value?.response?.status
}

function canvasErrorCode(error: unknown): string | undefined {
  const value = error as { code?: string; reason?: string; response?: { data?: { reason?: string } } } | null
  return value?.code ?? value?.reason ?? value?.response?.data?.reason
}

function readableError(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback
}
