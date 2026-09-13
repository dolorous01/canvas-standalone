import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { CanvasAPI, CanvasDocument, CanvasProject } from '@sub2api/api/canvas-api'

const draftState = vi.hoisted(() => new Map<string, unknown>())

vi.mock('localforage', () => ({
  default: {
    createInstance: () => ({
      getItem: (key: string) => Promise.resolve(draftState.get(key) ?? null),
      setItem: (key: string, value: unknown) => {
        draftState.set(key, value)
        return Promise.resolve(value)
      },
      removeItem: (key: string) => {
        draftState.delete(key)
        return Promise.resolve()
      }
    })
  }
}))

import { createServerCanvasStore } from './server-canvas-store'

const initialDocument: CanvasDocument = {
  schema_version: 1,
  nodes: [],
  edges: [],
  viewport: { x: 0, y: 0, k: 1 }
}

function project(): CanvasProject {
  return {
    id: 'project-1',
    name: 'Canvas',
    document: initialDocument,
    version: 3,
    created_at: '2026-08-20T00:00:00Z',
    updated_at: '2026-08-20T00:00:00Z'
  }
}

describe('createServerCanvasStore', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    draftState.clear()
  })

  it('preserves the local draft when optimistic save returns a conflict', async () => {
    const updateProject = vi.fn().mockRejectedValue({ status: 409, code: 'project_version_conflict' })
    const api = {
      listProjects: vi.fn().mockResolvedValue([project()]),
      updateProject
    } as unknown as CanvasAPI
    const store = createServerCanvasStore(api)
    await store.hydrate()
    const draft: CanvasDocument = {
      ...initialDocument,
      nodes: [{ id: 'node-1', type: 'text', position: { x: 10, y: 20 }, text: 'local edit' }]
    }

    store.updateDocument('project-1', draft)
    await vi.advanceTimersByTimeAsync(401)

    expect(updateProject).toHaveBeenCalledWith('project-1', 3, 'Canvas', draft)
    expect(store.getState().projectsByID['project-1']).toMatchObject({
      saveState: 'conflict',
      localDraft: draft
    })
    expect(draftState.has('project-1')).toBe(true)
    store.destroy()
    vi.useRealTimers()
  })
  it('duplicates the latest local draft and activates the copy', async () => {
    const draft: CanvasDocument = {
      ...initialDocument,
      nodes: [{ id: 'node-2', type: 'text', position: { x: 30, y: 40 }, text: 'copy me' }]
    }
    const created: CanvasProject = {
      ...project(),
      id: 'project-2',
      name: 'Canvas copy',
      document: draft,
      version: 1
    }
    const createProject = vi.fn().mockResolvedValue(created)
    const api = {
      listProjects: vi.fn().mockResolvedValue([project()]),
      createProject
    } as unknown as CanvasAPI
    const store = createServerCanvasStore(api)
    await store.hydrate()
    store.updateDocument('project-1', draft)

    const copy = await store.saveAsNew('project-1', 'Canvas copy')

    expect(createProject).toHaveBeenCalledWith('Canvas copy', draft)
    expect(copy).toMatchObject({ id: 'project-2', name: 'Canvas copy', saveState: 'saved' })
    expect(store.getState().activeProjectID).toBe('project-2')
    store.destroy()
    vi.useRealTimers()
  })
  it('does not resurrect a project when an in-flight save settles after deletion', async () => {
    let resolveUpdate!: (value: CanvasProject) => void
    const updateProject = vi.fn().mockImplementation(() => new Promise<CanvasProject>((resolve) => {
      resolveUpdate = resolve
    }))
    const deleteProject = vi.fn().mockResolvedValue(undefined)
    const api = {
      listProjects: vi.fn().mockResolvedValue([project()]),
      updateProject,
      deleteProject
    } as unknown as CanvasAPI
    const store = createServerCanvasStore(api)
    await store.hydrate()
    const draft: CanvasDocument = {
      ...initialDocument,
      nodes: [{ id: 'node-3', type: 'text', position: { x: 50, y: 60 }, text: 'delete me' }]
    }

    store.updateDocument('project-1', draft)
    await vi.advanceTimersByTimeAsync(401)
    await store.deleteProject('project-1')
    resolveUpdate({ ...project(), document: draft, version: 4 })
    await vi.advanceTimersByTimeAsync(0)

    expect(deleteProject).toHaveBeenCalledWith('project-1')
    expect(store.getState().projectsByID['project-1']).toBeUndefined()
    expect(store.getState().order).toEqual([])
    expect(draftState.has('project-1')).toBe(false)
    store.destroy()
    vi.useRealTimers()
  })
})
