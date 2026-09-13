import { afterEach, describe, expect, it, vi } from 'vitest'
import { imageFileFromURL, outpaintGeometry } from './image-editor-media'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('outpaintGeometry', () => {
  it('centers a portrait source inside a wider target', () => {
    expect(outpaintGeometry(800, 1200, 16 / 9)).toEqual({
      width: 2134,
      height: 1200,
      sourceX: 667,
      sourceY: 0,
      sourceWidth: 800,
      sourceHeight: 1200
    })
  })

  it('centers a landscape source inside a taller target', () => {
    expect(outpaintGeometry(1200, 800, 3 / 4)).toEqual({
      width: 1200,
      height: 1600,
      sourceX: 0,
      sourceY: 400,
      sourceWidth: 1200,
      sourceHeight: 800
    })
  })

  it('rejects output beyond the server canvas limit', () => {
    expect(() => outpaintGeometry(16_000, 16_000, 16 / 9)).toThrow(/supported size/)
  })
})

describe('imageFileFromURL', () => {
  it('decodes base64 data URLs without using fetch', async () => {
    const fetch = vi.fn()
    vi.stubGlobal('fetch', fetch)

    const file = await imageFileFromURL('DATA:image/png;BASE64,AAECAw==', 'crop.png')

    expect(fetch).not.toHaveBeenCalled()
    expect(file.name).toBe('crop.png')
    expect(file.type).toBe('image/png')
    expect(file.size).toBe(4)
  })

  it('decodes percent-encoded data URLs', async () => {
    const file = await imageFileFromURL('data:image/svg+xml,%3Csvg%2F%3E', 'mask.svg')

    expect(file.name).toBe('mask.svg')
    expect(file.type).toBe('image/svg+xml')
    expect(file.size).toBe(6)
  })

  it('continues to fetch regular image URLs', async () => {
    const blob = new Blob([new Uint8Array([4, 5, 6])], { type: 'image/webp' })
    const fetch = vi.fn().mockResolvedValue({ ok: true, blob: vi.fn().mockResolvedValue(blob) })
    vi.stubGlobal('fetch', fetch)

    const file = await imageFileFromURL('/images/source.webp', 'source.webp')

    expect(fetch).toHaveBeenCalledWith('/images/source.webp')
    expect(file.type).toBe('image/webp')
    expect(file.size).toBe(3)
  })

  it('rejects malformed data URLs without falling back to fetch', async () => {
    const fetch = vi.fn()
    vi.stubGlobal('fetch', fetch)

    await expect(imageFileFromURL('data:image/png;base64', 'crop.png')).rejects.toThrow('Image could not be read')
    expect(fetch).not.toHaveBeenCalled()
  })

  it('rejects invalid base64 payloads', async () => {
    await expect(imageFileFromURL('data:image/png;base64,not-valid-%%%', 'crop.png')).rejects.toThrow('Image could not be read')
  })
})
