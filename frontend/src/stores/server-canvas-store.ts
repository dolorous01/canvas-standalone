import localforage from 'localforage'
import type { CanvasAPI, CanvasDocument, CanvasProject } from '@sub2api/api/canvas-api'

export type CanvasSaveState = 'idle' | 'saving' | 'saved' | 'conflict' | 'error'

export interface ServerCanvasProject extends CanvasProject {
  saveState: CanvasSaveState
  localDraft?: CanvasDocument
}

export interface ServerCanvasState {
  projectsByID: Record<string, ServerCanvasProject>
  order: string[]
  hydrated: boolean
  activeProjectID?: string
}

export interface ServerCanvasStore {
  getState(): ServerCanvasState
  subscribe(listener: () => void): () => void
  hydrate(): Promise<void>
  createProject(name?: string): Promise<ServerCanvasProject>
  openProject(id: string): Promise<ServerCanvasProject>
  updateDocument(id: string, document: CanvasDocument): void
  mutateDocument(id: string, update: (document: CanvasDocument) => CanvasDocument): CanvasDocument | undefined
  renameProject(id: string, name: string): Promise<void>
  deleteProject(id: string): Promise<void>
  reloadServer(id: string): Promise<void>
  saveAsNew(id: string, name?: string): Promise<ServerCanvasProject>
  destroy(): void
}

const emptyDocument = (): CanvasDocument => ({
  schema_version: 1,
  nodes: [],
  edges: [],
  viewport: { x: 0, y: 0, k: 1 }
})

export function createServerCanvasStore(api: CanvasAPI): ServerCanvasStore {
  let state: ServerCanvasState = { projectsByID: {}, order: [], hydrated: false }
  const listeners = new Set<() => void>()
  const timers = new Map<string, ReturnType<typeof setTimeout>>()
  const persisting = new Set<string>()
  const retired = new Set<string>()
  const drafts = localforage.createInstance({ name: 'sub2api-canvas-drafts' })
  const emit = () => listeners.forEach((listener) => listener())
  const setState = (next: ServerCanvasState) => {
    state = next
    emit()
  }
  const put = (project: CanvasProject, saveState: CanvasSaveState = 'idle', localDraft?: CanvasDocument) => {
    setState({
      ...state,
      projectsByID: {
        ...state.projectsByID,
        [project.id]: { ...project, saveState, localDraft }
      },
      order: state.order.includes(project.id) ? state.order : [project.id, ...state.order]
    })
  }

  const persist = async (id: string) => {
    if (persisting.has(id) || retired.has(id)) return
    const project = state.projectsByID[id]
    if (!project?.localDraft) return
    const draft = project.localDraft
    const name = project.name
    const version = project.version
    persisting.add(id)
    put(project, 'saving', draft)
    try {
      const updated = await api.updateProject(project.id, version, name, draft)
      if (retired.has(id)) return
      const latest = state.projectsByID[id]
      if (latest?.localDraft && (latest.localDraft !== draft || latest.name !== name)) {
        const next = {
          ...updated,
          name: latest.name,
          document: latest.localDraft
        }
        put(next, 'saving', latest.localDraft)
        await drafts.setItem(id, {
          baseVersion: updated.version,
          document: latest.localDraft,
          savedAt: Date.now()
        })
        timers.set(id, setTimeout(() => void persist(id), 0))
      } else {
        await drafts.removeItem(project.id)
        put(updated, 'saved')
      }
    } catch (error) {
      if (retired.has(id)) return
      const latest = state.projectsByID[id]
      const conflict = canvasErrorCode(error) === 'project_version_conflict' || canvasErrorStatus(error) === 409
      if (latest) put(latest, conflict ? 'conflict' : 'error', latest.localDraft)
    } finally {
      persisting.delete(id)
    }
  }

  return {
    getState: () => state,
    subscribe(listener) {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    async hydrate() {
      const projects = await api.listProjects()
      const projectsByID: Record<string, ServerCanvasProject> = {}
      for (const project of projects) {
        const draft = await drafts.getItem<{ baseVersion: number; document: CanvasDocument }>(project.id)
        projectsByID[project.id] = {
          ...project,
          saveState: draft ? 'conflict' : 'idle',
          localDraft: draft?.document
        }
      }
      setState({ projectsByID, order: projects.map((project) => project.id), hydrated: true })
    },
    async createProject(name = 'Untitled canvas') {
      const project = await api.createProject(name, emptyDocument())
      put(project, 'saved')
      state = { ...state, activeProjectID: project.id }
      emit()
      return state.projectsByID[project.id]
    },
    async openProject(id) {
      const project = await api.getProject(id)
      const existing = state.projectsByID[id]
      if (existing?.localDraft) {
        put(project, 'conflict', existing.localDraft)
      } else {
        put(project, 'idle')
      }
      state = { ...state, activeProjectID: id }
      emit()
      return state.projectsByID[id]
    },
    updateDocument(id, document) {
      const project = state.projectsByID[id]
      if (!project) return
      put({ ...project, document }, 'saving', document)
      void drafts.setItem(id, { baseVersion: project.version, document, savedAt: Date.now() })
      const previous = timers.get(id)
      if (previous) clearTimeout(previous)
      timers.set(id, setTimeout(() => void persist(id), 400))
    },
    mutateDocument(id, update) {
      const project = state.projectsByID[id]
      if (!project) return undefined
      const current = project.localDraft || project.document
      const next = update(current)
      this.updateDocument(id, next)
      return next
    },
    async renameProject(id, name) {
      const project = state.projectsByID[id]
      if (!project) return
      const nextName = name.trim() || project.name
      if (nextName === project.name) return
      const document = project.localDraft || project.document
      put({ ...project, name: nextName, document }, 'saving', document)
      await drafts.setItem(id, { baseVersion: project.version, document, savedAt: Date.now() })
      const previous = timers.get(id)
      if (previous) clearTimeout(previous)
      await persist(id)
    },
    async deleteProject(id) {
      retired.add(id)
      const pendingTimer = timers.get(id)
      if (pendingTimer) clearTimeout(pendingTimer)
      timers.delete(id)
      try {
        await api.deleteProject(id)
      } catch (error) {
        retired.delete(id)
        if (state.projectsByID[id]?.localDraft) {
          timers.set(id, setTimeout(() => void persist(id), 0))
        }
        throw error
      }
      const projectsByID = { ...state.projectsByID }
      delete projectsByID[id]
      setState({
        ...state,
        projectsByID,
        order: state.order.filter((projectID) => projectID !== id),
        activeProjectID: state.activeProjectID === id ? undefined : state.activeProjectID
      })
      await drafts.removeItem(id)
    },
    async reloadServer(id) {
      const project = await api.getProject(id)
      await drafts.removeItem(id)
      put(project, 'idle')
    },
    async saveAsNew(id, name) {
      const project = state.projectsByID[id]
      if (!project) throw new Error('Project not found')
      const created = await api.createProject(name || `${project.name} copy`, project.localDraft || project.document)
      put(created, 'saved')
      state = { ...state, activeProjectID: created.id }
      emit()
      return state.projectsByID[created.id]
    },
    destroy() {
      for (const timer of timers.values()) clearTimeout(timer)
      timers.clear()
      persisting.clear()
      retired.clear()
      listeners.clear()
    }
  }
}

function canvasErrorStatus(error: unknown): number | undefined {
  if (!error || typeof error !== 'object') return undefined
  const value = error as { status?: number; response?: { status?: number } }
  return value.status ?? value.response?.status
}

function canvasErrorCode(error: unknown): string | undefined {
  if (!error || typeof error !== 'object') return undefined
  const value = error as { code?: string; reason?: string; response?: { data?: { reason?: string } } }
  return value.code ?? value.reason ?? value.response?.data?.reason
}
