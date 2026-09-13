import { describe, expect, it, vi } from 'vitest'
import type { CanvasAsset } from '@sub2api/api/canvas-api'
import type { CanvasProject } from '@sub2api/adapters/use-canvas-store'
import { CanvasNodeType } from '@/types/canvas'
import { applyEditorResultToProject, persistedImageAssetID } from './apply-editor-result'

vi.mock('@sub2api/adapters/asset-runtime', () => ({
  assetIDFromStorageKey: (key?: string) => key?.startsWith('asset:') ? key.slice(6) : undefined,
  assetStorageKey: (id: string) => `asset:${id}`
}))

const project: CanvasProject = {
  id: 'project-1',
  title: 'Board',
  createdAt: '2026-08-26T00:00:00Z',
  updatedAt: '2026-08-26T00:00:00Z',
  nodes: [{
    id: 'node-1',
    type: CanvasNodeType.Image,
    title: 'Product',
    position: { x: 0, y: 0 },
    width: 400,
    height: 300,
    metadata: {
      storageKey: 'asset:asset-1',
      content: 'blob:old',
      primaryImageId: 'image-1',
      images: [{
        id: 'image-1', status: 'success', content: 'blob:old', storageKey: 'asset:asset-1',
        naturalWidth: 800, naturalHeight: 600, bytes: 80, mimeType: 'image/png'
      }]
    }
  }],
  connections: [],
  chatSessions: [],
  activeChatId: null,
  backgroundMode: 'lines',
  showImageInfo: false,
  viewport: { x: 0, y: 0, k: 1 },
  recoveryRevision: 0,
  recoveries: []
}

const result: CanvasAsset = {
  id: 'asset-2', source_type: 'derived', media_kind: 'image', mime_type: 'image/webp',
  width: 1024, height: 768, byte_size: 120, sha256: 'hash', url: '/asset-2'
}

describe('focused editor canvas integration', () => {
  it('resolves the persisted source asset from the primary image', () => {
    expect(persistedImageAssetID(project.nodes[0])).toBe('asset-1')
  })

  it('applies the shared result asset to the node and its primary image', () => {
    const updated = applyEditorResultToProject(project, 'node-1', result, 'blob:new')

    expect(updated?.nodes[0].metadata).toMatchObject({
      storageKey: 'asset:asset-2',
      content: 'blob:new',
      naturalWidth: 1024,
      naturalHeight: 768,
      mimeType: 'image/webp',
      images: [{ storageKey: 'asset:asset-2', content: 'blob:new' }]
    })
    expect(project.nodes[0].metadata?.storageKey).toBe('asset:asset-1')
  })
})
