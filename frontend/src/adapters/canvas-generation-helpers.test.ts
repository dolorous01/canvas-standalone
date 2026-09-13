import { describe, expect, it, vi } from 'vitest'

vi.mock('../upstream/lib/canvas/canvas-generation-helpers', () => ({
  buildGenerationConfig: vi.fn()
}))

vi.mock('@/lib/canvas/canvas-node-factory', () => ({
  imageMetadata: vi.fn()
}))

vi.mock('@/services/image-storage', () => ({
  resolveImageUrl: vi.fn((storageKey: string) => Promise.resolve(`blob:${storageKey}`)),
  uploadImage: vi.fn()
}))

vi.mock('@/services/file-storage', () => ({
  resolveMediaUrl: vi.fn((storageKey: string) => Promise.resolve(`blob:${storageKey}`))
}))

import { hydrateCanvasImages, resolveMetadataReferences } from './canvas-generation-helpers'
import { CanvasNodeType, type CanvasNodeData, type CanvasNodeMetadata } from '@/types/canvas'

describe('server canvas media hydration', () => {
  it('restores an image node and its image list when transient content was stripped', async () => {
    const node = {
      id: 'image-1',
      type: CanvasNodeType.Image,
      title: 'Image',
      position: { x: 0, y: 0 },
      width: 320,
      height: 320,
      metadata: {
        content: '',
        storageKey: 'asset:primary',
        images: [{ id: 'variant-1', status: 'success', content: '', storageKey: 'asset:variant' }]
      }
    } as CanvasNodeData

    const [hydrated] = await hydrateCanvasImages([node])

    expect(hydrated.metadata?.content).toBe('blob:asset:primary')
    expect(hydrated.metadata?.images?.[0]?.content).toBe('blob:asset:variant')
  })

  it('resolves durable edit references and preserves their storage keys', async () => {
    const references = await resolveMetadataReferences({
      generationType: 'edit',
      references: ['asset:reference']
    } as CanvasNodeMetadata)

    expect(references).toEqual([expect.objectContaining({
      dataUrl: 'blob:asset:reference',
      storageKey: 'asset:reference'
    })])
  })
})
