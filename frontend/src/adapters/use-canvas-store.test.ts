import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  api: {
    listProjects: vi.fn(),
    createProject: vi.fn(),
    getProject: vi.fn(),
    getMediaTask: vi.fn(),
    updateProject: vi.fn()
  },
  drafts: {
    getItem: vi.fn(),
    setItem: vi.fn(),
    removeItem: vi.fn()
  },
  notify: vi.fn()
}))

vi.mock('localforage', () => ({
  default: { createInstance: () => mocks.drafts }
}))
vi.mock('nanoid', () => ({ nanoid: () => 'generated-id' }))
vi.mock('@sub2api/adapters/asset-runtime', () => ({ resolveStorageKeyAlias: (value: string) => value }))
vi.mock('@sub2api/api/canvas-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@sub2api/api/canvas-api')>()
  return { ...actual, createCanvasAPI: () => mocks.api }
})
vi.mock('@sub2api/runtime/host-runtime', () => ({
  getCanvasRuntimeHost: () => ({ notify: mocks.notify })
}))

import {
  initializeCanvasProjectStore,
  isCanvasRecoveryActive,
  mostRecentCanvasProject,
  registerCanvasTaskRecovery,
  resetCanvasProjectStore,
  scheduleCanvasTaskRecoveryCleanup,
  useCanvasStore,
  type CanvasProject
} from './use-canvas-store'

function serverProject(status: 'running' | 'completed') {
  return {
    id: 'project-1',
    name: 'Recovery project',
    version: 1,
    created_at: '2026-08-24T00:00:00Z',
    updated_at: '2026-08-24T00:00:01Z',
    document: {
      schema_version: 2 as const,
      nodes: [
        {
          id: 'source', type: 'config', title: 'Source', position: { x: 0, y: 0 }, width: 320, height: 240,
          metadata: { status: 'loading' }
        },
        {
          id: 'target', type: 'video', title: 'Video', position: { x: 400, y: 0 }, width: 320, height: 240,
          metadata: { status: 'loading' }
        }
      ],
      connections: [{ id: 'edge', fromNodeId: 'source', toNodeId: 'target' }]
    },
    media_tasks: [{
      id: 'task-1',
      kind: 'video' as const,
      status,
      project_id: 'project-1',
      client_node_id: 'source',
      selected_model: 'grok-imagine-video',
      results: status === 'completed'
        ? [{ index: 0, asset_id: 'video-asset', mime_type: 'video/mp4' }]
        : [],
      created_at: 1
    }]
  }
}

function localProject(id: string, updatedAt: string): CanvasProject {
  return {
    id,
    title: id,
    createdAt: '2026-08-20T00:00:00Z',
    updatedAt,
    nodes: [],
    connections: [],
    chatSessions: [],
    activeChatId: null,
    backgroundMode: 'lines',
    showImageInfo: false,
    viewport: { x: 0, y: 0, k: 1 },
    recoveryRevision: 0,
    recoveries: []
  }
}

function plainServerProject(version = 1) {
  return {
    id: 'project-1',
    name: 'Canvas',
    version,
    created_at: '2026-08-24T00:00:00Z',
    updated_at: `2026-08-24T00:00:0${version}Z`,
    document: {
      schema_version: 2 as const,
      nodes: [],
      connections: [],
      chat_sessions: [],
      active_chat_id: null,
      background_mode: 'lines',
      show_image_info: false,
      viewport: { x: 0, y: 0, k: 1 }
    }
  }
}

describe('mostRecentCanvasProject', () => {
  it('uses the current update timestamp instead of array order', () => {
    const older = localProject('older', '2026-08-25T10:00:00Z')
    const newer = localProject('newer', '2026-08-26T10:00:00Z')

    expect(mostRecentCanvasProject([older, newer])?.id).toBe('newer')
  })
})

describe('canvas project recovery store', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.drafts.getItem.mockResolvedValue(null)
    mocks.drafts.setItem.mockResolvedValue(undefined)
    mocks.drafts.removeItem.mockResolvedValue(undefined)
    resetCanvasProjectStore()
  })

  afterEach(() => {
    resetCanvasProjectStore()
    vi.useRealTimers()
  })

  it('reconciles a completed task before exposing the hydrated project', async () => {
    mocks.api.listProjects.mockResolvedValue([serverProject('completed')])

    await initializeCanvasProjectStore()
    const project = useCanvasStore.getState().openProject('project-1')

    expect(project?.nodes[1].metadata).toMatchObject({
      status: 'success',
      storageKey: 'asset:video-asset'
    })
    expect(project?.nodes[0].metadata?.status).toBe('success')
    expect(isCanvasRecoveryActive('target')).toBe(false)
  })

  it('keeps an active task recoverable and applies its terminal snapshot', async () => {
    vi.useFakeTimers()
    mocks.api.listProjects.mockResolvedValue([serverProject('running')])
    mocks.api.getMediaTask.mockResolvedValue({
      ...serverProject('completed').media_tasks[0],
      status: 'completed'
    })
    mocks.api.updateProject.mockImplementation(async (_id, _version, _name, document) => ({
      ...serverProject('completed'),
      document,
      version: 2
    }))

    await initializeCanvasProjectStore()
    useCanvasStore.getState().openProject('project-1')
    expect(isCanvasRecoveryActive('source')).toBe(true)
    expect(isCanvasRecoveryActive('target')).toBe(true)

    await vi.advanceTimersByTimeAsync(1500)
    await vi.advanceTimersByTimeAsync(0)

    const project = useCanvasStore.getState().projects[0]
    expect(project.nodes[1].metadata).toMatchObject({
      status: 'success',
      storageKey: 'asset:video-asset'
    })
    expect(project.recoveryRevision).toBe(1)
    expect(isCanvasRecoveryActive('target')).toBe(false)
  })

  it('resumes a persisted task ID even when it is absent from the project listing snapshot', async () => {
    vi.useFakeTimers()
    const value = serverProject('running')
    mocks.api.listProjects.mockResolvedValue([{
      ...value,
      document: {
        ...value.document,
        recoveries: [{
          taskID: 'task-1',
          kind: 'video',
          sourceNodeID: 'source',
          targetNodeID: 'target'
        }]
      },
      media_tasks: []
    }])
    mocks.api.getMediaTask.mockResolvedValue(serverProject('completed').media_tasks[0])
    mocks.api.updateProject.mockImplementation(async (_id, _version, _name, document) => ({
      ...serverProject('completed'),
      document,
      version: 2
    }))

    await initializeCanvasProjectStore()
    useCanvasStore.getState().openProject('project-1')
    expect(isCanvasRecoveryActive('target')).toBe(true)

    await vi.advanceTimersByTimeAsync(1500)
    await vi.advanceTimersByTimeAsync(0)

    expect(mocks.api.getMediaTask).toHaveBeenCalledWith('task-1', expect.any(AbortSignal))
    expect(useCanvasStore.getState().projects[0].nodes[1].metadata?.storageKey).toBe('asset:video-asset')
    expect(useCanvasStore.getState().projects[0].recoveries).toEqual([])
  })

  it('persists a new task-to-node binding until delayed cleanup', async () => {
    vi.useFakeTimers()
    const value = serverProject('running')
    mocks.api.listProjects.mockResolvedValue([{ ...value, media_tasks: [] }])
    mocks.api.updateProject.mockImplementation(async (_id, _version, _name, document) => ({
      ...value,
      document,
      version: 2
    }))
    await initializeCanvasProjectStore()
    useCanvasStore.getState().openProject('project-1')

    registerCanvasTaskRecovery({
      id: 'new-task',
      kind: 'video',
      status: 'queued',
      project_id: 'project-1',
      client_node_id: 'source',
      selected_model: 'grok-imagine-video',
      results: []
    }, 'video')

    expect(useCanvasStore.getState().projects[0].recoveries).toEqual([{
      taskID: 'new-task',
      kind: 'video',
      sourceNodeID: 'source',
      targetNodeID: 'target'
    }])
    scheduleCanvasTaskRecoveryCleanup('new-task', 100)
    await vi.advanceTimersByTimeAsync(100)
    await vi.advanceTimersByTimeAsync(0)

    expect(useCanvasStore.getState().projects[0].recoveries).toEqual([])
  })
})

describe('canvas project save state', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
    mocks.drafts.getItem.mockResolvedValue(null)
    mocks.drafts.setItem.mockResolvedValue(undefined)
    mocks.drafts.removeItem.mockResolvedValue(undefined)
    resetCanvasProjectStore()
  })

  afterEach(() => {
    resetCanvasProjectStore()
    vi.useRealTimers()
  })

  it('publishes dirty, saving, and saved while persisting the latest document', async () => {
    let resolveUpdate!: (value: ReturnType<typeof plainServerProject>) => void
    mocks.api.listProjects.mockResolvedValue([plainServerProject()])
    mocks.api.updateProject.mockImplementation(() => new Promise((resolve) => {
      resolveUpdate = resolve
    }))
    await initializeCanvasProjectStore()

    useCanvasStore.getState().updateProject('project-1', {
      nodes: [{ id: 'local-node', type: 'text', title: 'Local', position: { x: 0, y: 0 }, width: 320, height: 220, metadata: { content: 'draft' } }]
    })
    expect(useCanvasStore.getState().saveStatusByProject['project-1']?.state).toBe('dirty')

    await vi.advanceTimersByTimeAsync(400)
    expect(useCanvasStore.getState().saveStatusByProject['project-1']?.state).toBe('saving')

    resolveUpdate(plainServerProject(2))
    await vi.advanceTimersByTimeAsync(0)
    expect(useCanvasStore.getState().saveStatusByProject['project-1']?.state).toBe('saved')
    expect(mocks.drafts.removeItem).toHaveBeenCalledWith('project-1')
  })

  it('keeps the local draft on conflict and can reload the server copy', async () => {
    const server = plainServerProject(2)
    mocks.api.listProjects.mockResolvedValue([plainServerProject()])
    mocks.api.updateProject.mockRejectedValue({ status: 409, code: 'project_version_conflict', message: 'Conflict' })
    mocks.api.getProject.mockResolvedValue(server)
    await initializeCanvasProjectStore()

    useCanvasStore.getState().updateProject('project-1', {
      nodes: [{ id: 'local-node', type: 'text', title: 'Local', position: { x: 0, y: 0 }, width: 320, height: 220, metadata: { content: 'draft' } }]
    })
    await vi.advanceTimersByTimeAsync(400)
    expect(useCanvasStore.getState().saveStatusByProject['project-1']?.state).toBe('conflict')
    expect(mocks.drafts.setItem).toHaveBeenCalled()

    await useCanvasStore.getState().reloadServerProject('project-1')
    expect(useCanvasStore.getState().saveStatusByProject['project-1']?.state).toBe('saved')
    expect(useCanvasStore.getState().projects[0].nodes).toEqual([])
    expect(mocks.drafts.removeItem).toHaveBeenCalledWith('project-1')
  })

  it('creates a new server project from the conflicted local document', async () => {
    mocks.api.listProjects.mockResolvedValue([plainServerProject()])
    mocks.api.createProject.mockImplementation(async (name, document, id) => ({
      ...plainServerProject(),
      id,
      name,
      document
    }))
    await initializeCanvasProjectStore()
    useCanvasStore.getState().updateProject('project-1', {
      nodes: [{ id: 'copy-node', type: 'text', title: 'Copy', position: { x: 0, y: 0 }, width: 320, height: 220, metadata: { content: 'keep me' } }]
    })

    const id = useCanvasStore.getState().saveProjectAsNew('project-1', 'Canvas copy')
    await vi.advanceTimersByTimeAsync(0)

    expect(id).toBe('generated-id')
    expect(mocks.api.createProject).toHaveBeenCalledWith(
      'Canvas copy',
      expect.objectContaining({ nodes: [expect.objectContaining({ id: 'copy-node' })] }),
      'generated-id'
    )
    expect(useCanvasStore.getState().saveStatusByProject['generated-id']?.state).toBe('saved')
  })
})
