import { describe, expect, it, vi } from 'vitest'
import { CanvasNodeType, type CanvasNodeData } from '@/types/canvas'

const resolveImageUrl = vi.hoisted(() => vi.fn(async (key: string) => `blob:${key}`))

vi.mock('@/services/image-storage', () => ({
  resolveImageUrl,
  uploadImage: vi.fn()
}))
vi.mock('@/services/file-storage', () => ({ resolveMediaUrl: vi.fn() }))

import { hydrateCanvasImages } from '@/lib/canvas/canvas-generation-helpers'

describe('hydrateCanvasImages', () => {
  it('restores a persisted image when sanitized content is empty', async () => {
    const node: CanvasNodeData = {
      id: 'node-1',
      type: CanvasNodeType.Image,
      title: 'Persisted image',
      position: { x: 0, y: 0 },
      width: 320,
      height: 240,
      metadata: {
        content: '',
        storageKey: 'asset:asset-1',
        images: [{
          id: 'image-1',
          status: 'success',
          content: '',
          storageKey: 'asset:asset-1',
          naturalWidth: 640,
          naturalHeight: 480,
          bytes: 100,
          mimeType: 'image/png'
        }]
      }
    }

    const [hydrated] = await hydrateCanvasImages([node])

    expect(hydrated.metadata?.content).toBe('blob:asset:asset-1')
    expect(hydrated.metadata?.images?.[0].content).toBe('blob:asset:asset-1')
    expect(resolveImageUrl).toHaveBeenCalledTimes(2)
  })
})
