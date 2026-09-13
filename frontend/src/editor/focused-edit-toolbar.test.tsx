import { describe, expect, it, vi } from 'vitest'
import { CanvasNodeType, type CanvasNodeData } from '@/types/canvas'
import {
  buildImageToolbarTools,
  defaultImageQuickToolIds,
  shouldShowImageToolLabel,
  type ImageToolHandlers
} from '@/components/canvas/canvas-image-toolbar-tools'

const handlers = new Proxy({}, {
  get: () => vi.fn()
}) as ImageToolHandlers

function imageNode(storageKey: string): CanvasNodeData {
  return {
    id: 'node-1',
    type: CanvasNodeType.Image,
    title: 'Image',
    position: { x: 0, y: 0 },
    width: 320,
    height: 240,
    metadata: { content: 'blob:preview', storageKey }
  }
}

describe('focused image toolbar command', () => {
  it('is available for a persisted asset and hidden for a local-only image', () => {
    expect(buildImageToolbarTools(imageNode('asset:asset-1'), handlers).map((tool) => tool.id)).toContain('focusedEdit')
    expect(buildImageToolbarTools(imageNode('image:local-1'), handlers).map((tool) => tool.id)).not.toContain('focusedEdit')
  })

  it('is a default action with a visible label even in compact mode', () => {
    expect(defaultImageQuickToolIds).toContain('focusedEdit')
    expect(shouldShowImageToolLabel('focusedEdit', false)).toBe(true)
    expect(shouldShowImageToolLabel('crop', false)).toBe(false)
    expect(shouldShowImageToolLabel('crop', true)).toBe(true)
  })
})
