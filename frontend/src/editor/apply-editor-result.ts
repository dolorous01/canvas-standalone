import type { CanvasAsset } from '@sub2api/api/canvas-api'
import { assetIDFromStorageKey, assetStorageKey } from '@sub2api/adapters/asset-runtime'
import type { CanvasProject } from '@sub2api/adapters/use-canvas-store'
import { CanvasNodeType, type CanvasNodeData, type CanvasNodeImage } from '@/types/canvas'

export function persistedImageAssetID(node?: CanvasNodeData): string | undefined {
  if (!node || node.type !== CanvasNodeType.Image) return undefined
  const primary = node.metadata?.images?.find((image) => image.id === node.metadata?.primaryImageId)
  return assetIDFromStorageKey(primary?.storageKey || node.metadata?.storageKey)
}

export function applyEditorResultToProject(
  project: CanvasProject,
  nodeID: string,
  asset: CanvasAsset,
  imageURL: string
): CanvasProject | undefined {
  let changed = false
  const nodes = project.nodes.map((node) => {
    if (node.id !== nodeID || node.type !== CanvasNodeType.Image) return node
    changed = true
    const storageKey = assetStorageKey(asset.id)
    const imagePatch = {
      content: imageURL,
      storageKey,
      naturalWidth: asset.width,
      naturalHeight: asset.height,
      bytes: asset.byte_size,
      mimeType: asset.mime_type,
      status: 'success' as const,
      errorDetails: undefined
    }
    const images = updatePrimaryImage(node.metadata?.images, node.metadata?.primaryImageId, imagePatch)
    return {
      ...node,
      metadata: {
        ...node.metadata,
        ...imagePatch,
        ...(images ? { images } : {})
      }
    }
  })
  return changed ? { ...project, nodes } : undefined
}

function updatePrimaryImage(
  images: CanvasNodeImage[] | undefined,
  primaryImageID: string | undefined,
  patch: Omit<CanvasNodeImage, 'id'>
): CanvasNodeImage[] | undefined {
  if (!images?.length) return images
  const targetID = primaryImageID || images[0].id
  return images.map((image) => image.id === targetID ? { ...image, ...patch } : image)
}
