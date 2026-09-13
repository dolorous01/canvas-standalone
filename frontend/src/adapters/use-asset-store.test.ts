import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  api: {
    listLibraryItems: vi.fn(),
    createLibraryItem: vi.fn(),
    updateLibraryItem: vi.fn(),
    deleteLibraryItem: vi.fn(),
    getAssetBlob: vi.fn()
  },
  storage: {
    getItem: vi.fn(),
    setItem: vi.fn(),
    removeItem: vi.fn()
  },
  notify: vi.fn(),
  uploadImage: vi.fn(),
  uploadMediaFile: vi.fn(),
  getLegacyImageBlob: vi.fn(),
  getLegacyMediaBlob: vi.fn()
}))

vi.mock('nanoid', () => ({ nanoid: () => 'local-generated' }))
vi.mock('@/lib/localforage-storage', () => ({ localForageStorage: mocks.storage }))
vi.mock('@sub2api/api/canvas-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@sub2api/api/canvas-api')>()
  return { ...actual, createCanvasAPI: () => mocks.api }
})
vi.mock('@sub2api/runtime/host-runtime', () => ({
  getCanvasRuntimeHost: () => ({ notify: mocks.notify })
}))
vi.mock('@sub2api/adapters/asset-runtime', () => ({
  assetIDFromStorageKey: (value?: string) => value?.startsWith('asset:') ? value.slice(6) : undefined,
  assetStorageKey: (value: string) => `asset:${value}`
}))
vi.mock('@sub2api/adapters/image-storage', () => ({ uploadImage: mocks.uploadImage }))
vi.mock('@sub2api/adapters/file-storage', () => ({ uploadMediaFile: mocks.uploadMediaFile }))
vi.mock('../upstream/services/image-storage', () => ({ getImageBlob: mocks.getLegacyImageBlob }))
vi.mock('../upstream/services/file-storage', () => ({ getMediaBlob: mocks.getLegacyMediaBlob }))

import { initializeCanvasAssetStore, resetCanvasAssetStore, useAssetStore } from './use-asset-store'

function libraryTextItem(overrides: Record<string, unknown> = {}) {
  return {
    id: 'library-1',
    client_id: 'local-1',
    kind: 'text' as const,
    title: 'Prompt',
    content: 'Hello',
    tags: [],
    metadata: {},
    version: 1,
    created_at: '2026-08-28T00:00:00.000Z',
    updated_at: '2026-08-28T00:00:00.000Z',
    ...overrides
  }
}

describe('server-backed asset store', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.storage.getItem.mockResolvedValue(null)
    mocks.storage.removeItem.mockResolvedValue(undefined)
    mocks.api.listLibraryItems.mockResolvedValue([])
    mocks.api.deleteLibraryItem.mockResolvedValue(undefined)
    mocks.api.updateLibraryItem.mockImplementation(async (_id, input) => libraryTextItem({
      title: input.title,
      content: input.content,
      tags: input.tags,
      source: input.source,
      note: input.note,
      metadata: input.metadata,
      version: (input.version || 1) + 1,
      updated_at: '2026-08-28T00:01:00.000Z'
    }))
    resetCanvasAssetStore()
  })

  it('waits for backend persistence before addAssetAsync resolves', async () => {
    mocks.api.createLibraryItem.mockImplementation(async (input) => libraryTextItem({
      client_id: input.client_id,
      title: input.title,
      content: input.content,
      tags: input.tags,
      source: input.source,
      note: input.note,
      metadata: input.metadata
    }))
    await initializeCanvasAssetStore()

    const id = await useAssetStore.getState().addAssetAsync({
      kind: 'text', title: 'Prompt', coverUrl: '', tags: ['reusable'], data: { content: 'Hello' }
    })

    expect(id).toBe('local-generated')
    expect(mocks.api.createLibraryItem).toHaveBeenCalledWith(expect.objectContaining({
      client_id: 'local-generated', kind: 'text', content: 'Hello', tags: ['reusable']
    }))
    expect(useAssetStore.getState().assets).toHaveLength(1)
  })

  it('migrates legacy IndexedDB metadata with its original client id exactly once', async () => {
    mocks.storage.getItem.mockResolvedValue(JSON.stringify({
      state: {
        assets: [{
          id: 'legacy-text', kind: 'text', title: 'Legacy prompt', coverUrl: '', tags: [],
          createdAt: '2026-08-20T00:00:00.000Z', updatedAt: '2026-08-20T00:00:00.000Z',
          data: { content: 'Keep this' }
        }]
      }
    }))
    mocks.api.createLibraryItem.mockImplementation(async (input) => libraryTextItem({
      id: 'library-legacy', client_id: input.client_id, title: input.title, content: input.content,
      tags: input.tags, source: input.source, note: input.note, metadata: input.metadata
    }))

    await initializeCanvasAssetStore()

    expect(mocks.api.createLibraryItem).toHaveBeenCalledWith(expect.objectContaining({
      client_id: 'legacy-text', content: 'Keep this'
    }))
    expect(mocks.storage.removeItem).toHaveBeenCalledWith('infinite-canvas:asset_store')
    expect(useAssetStore.getState().assets[0]).toMatchObject({ id: 'legacy-text', title: 'Legacy prompt' })
  })

  it('reconciles a prior idempotent create before reporting success', async () => {
    mocks.api.createLibraryItem.mockResolvedValue(libraryTextItem({
      client_id: 'local-generated', title: 'Older prompt', content: 'Old content', version: 4
    }))
    mocks.api.updateLibraryItem.mockImplementation(async (_id, input) => libraryTextItem({
      client_id: 'local-generated', title: input.title, content: input.content, tags: input.tags,
      source: input.source, note: input.note, metadata: input.metadata, version: 5
    }))
    await initializeCanvasAssetStore()

    await useAssetStore.getState().addAssetAsync({
      kind: 'text', title: 'Current prompt', coverUrl: '', tags: [], data: { content: 'Current content' }
    })

    expect(mocks.api.updateLibraryItem).toHaveBeenCalledWith('library-1', expect.objectContaining({
      version: 4, title: 'Current prompt', content: 'Current content'
    }))
  })

  it('waits for update and delete persistence and rolls back a failed delete', async () => {
    mocks.api.listLibraryItems.mockResolvedValue([libraryTextItem()])
    await initializeCanvasAssetStore()

    await useAssetStore.getState().updateAssetAsync('local-1', { title: 'Updated prompt' })
    expect(mocks.api.updateLibraryItem).toHaveBeenCalledWith('library-1', expect.objectContaining({
      version: 1, title: 'Updated prompt'
    }))
    expect(useAssetStore.getState().assets[0].title).toBe('Updated prompt')

    mocks.api.deleteLibraryItem.mockRejectedValueOnce(new Error('delete failed'))
    await expect(useAssetStore.getState().removeAssetAsync('local-1')).rejects.toThrow('delete failed')
    expect(useAssetStore.getState().assets[0]).toMatchObject({ id: 'local-1', title: 'Updated prompt' })
  })
})
