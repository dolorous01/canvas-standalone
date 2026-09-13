import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CanvasHostContext } from '@sub2api/host-context'
import type { ImageEditorDocument } from '@sub2api/api/canvas-api'
import { CanvasNodeType } from '@/types/canvas'

const assetRuntime = vi.hoisted(() => ({
  resolveCanvasAssetURL: vi.fn(async (key: string) => `blob:${key}`),
  uploadCanvasAsset: vi.fn(),
  cacheCanvasAssetBlob: vi.fn()
}))

vi.mock('@sub2api/adapters/asset-runtime', () => ({
  assetIDFromStorageKey: (key?: string) => key?.startsWith('asset:') ? key.slice(6) : undefined,
  assetStorageKey: (id: string) => `asset:${id}`,
  ...assetRuntime
}))

import upstreamI18n from '@/i18n'
import { CanvasHostProvider, createCanvasHostStore } from '@sub2api/host-context'
import { useCanvasStore } from '@sub2api/adapters/use-canvas-store'
import FocusedImageEditor from './focused-image-editor'

;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const editor: ImageEditorDocument = {
  id: 'editor-1',
  project_id: 'project-1',
  node_id: 'node-1',
  base_asset: {
    id: 'asset-1', source_type: 'upload', media_kind: 'image', mime_type: 'image/png',
    width: 800, height: 600, byte_size: 100, sha256: 'one', url: '/asset-1'
  },
  current_asset: {
    id: 'asset-2', source_type: 'derived', media_kind: 'image', mime_type: 'image/webp',
    width: 1024, height: 768, byte_size: 120, sha256: 'two', url: '/asset-2'
  },
  document: {
    schema_version: 1,
    viewport: { zoom: 1, x: 0, y: 0 },
    canvas: { width: 1024, height: 768, background: 'transparent' }
  },
  version: 2,
  asset_references: [],
  revisions: [{
    id: 'revision-1', version: 2,
    asset: {
      id: 'asset-2', source_type: 'derived', media_kind: 'image', mime_type: 'image/webp',
      width: 1024, height: 768, byte_size: 120, sha256: 'two', url: '/asset-2'
    },
    operation: 'crop', parameters: {}, created_at: '2026-08-26T00:00:00Z'
  }],
  created_at: '2026-08-26T00:00:00Z',
  updated_at: '2026-08-26T00:00:01Z'
}

describe('FocusedImageEditor', () => {
  beforeEach(async () => {
    vi.clearAllMocks()
    vi.stubGlobal('ResizeObserver', class {
      observe() {}
      unobserve() {}
      disconnect() {}
    })
    await upstreamI18n.changeLanguage('en-US')
    useCanvasStore.setState({
      hydrated: true,
      projects: [{
        id: 'project-1',
        title: 'Campaign board',
        createdAt: '2026-08-26T00:00:00Z',
        updatedAt: '2026-08-26T00:00:00Z',
        nodes: [{
          id: 'node-1', type: CanvasNodeType.Image, title: 'Product',
          position: { x: 0, y: 0 }, width: 400, height: 300,
          metadata: {
            storageKey: 'asset:asset-1', content: 'blob:source',
            naturalWidth: 800, naturalHeight: 600, bytes: 100, mimeType: 'image/png'
          }
        }],
        connections: [], chatSessions: [], activeChatId: null,
        backgroundMode: 'lines', showImageInfo: false,
        viewport: { x: 0, y: 0, k: 1 }, recoveryRevision: 0, recoveries: []
      }]
    })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    useCanvasStore.setState({ hydrated: false, projects: [] })
  })

  it('hydrates, applies the shared result, and returns to the source canvas', async () => {
    const notify = vi.fn()
    const request = vi.fn().mockResolvedValue(editor)
    const host: CanvasHostContext = {
      apiBaseURL: '/api/v1', locale: 'en-US', theme: 'light', routeMode: 'user',
      request, stream: async () => new ReadableStream<Uint8Array>(), navigate: vi.fn(), notify
    }
    const container = document.createElement('div')
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <CanvasHostProvider store={createCanvasHostStore(host)}>
          <MemoryRouter initialEntries={['/editor/project-1/node-1']}>
            <Routes>
              <Route path="/editor/:projectId/:nodeId" element={<FocusedImageEditor />} />
              <Route path="/canvas/:id" element={<div data-canvas-returned />} />
            </Routes>
          </MemoryRouter>
        </CanvasHostProvider>
      )
    })
    await act(async () => { await Promise.resolve() })

    expect(request).toHaveBeenCalledWith(
      'POST',
      '/image-canvas/editor-documents',
      expect.objectContaining({ project_id: 'project-1', node_id: 'node-1', base_asset_id: 'asset-1' }),
      undefined
    )
    expect(container.querySelector('[data-focused-image-editor]')).not.toBeNull()
    const apply = Array.from(container.querySelectorAll('button')).find((button) => button.textContent?.includes('Apply'))
    expect(apply).toBeDefined()

    await act(async () => { apply?.click() })

    expect(useCanvasStore.getState().projects[0].nodes[0].metadata).toMatchObject({
      storageKey: 'asset:asset-2',
      content: 'blob:asset:asset-2',
      naturalWidth: 1024,
      naturalHeight: 768
    })
    expect(container.querySelector('[data-canvas-returned]')).not.toBeNull()
    expect(notify).toHaveBeenCalledWith('success', 'Editor result applied to the canvas')

    await act(async () => root.unmount())
  })
})
