import i18n from '@/i18n'
import { readImageMeta } from '@/lib/image-utils'
import {
  assetStorageKey,
  getCanvasAssetBlob,
  registerStorageKeyAlias,
  releaseCanvasAssetURLs,
  resolveCanvasAssetURL,
  uploadCanvasAsset
} from '@sub2api/adapters/asset-runtime'
import { getActiveCanvasProjectID } from '@sub2api/adapters/use-canvas-store'

export type UploadedImage = {
  url: string
  storageKey: string
  width: number
  height: number
  bytes: number
  mimeType: string
}

export async function uploadImage(input: string | Blob): Promise<UploadedImage> {
  if (typeof input === 'string' && input.startsWith('asset:')) {
    const blob = await getCanvasAssetBlob(input)
    if (!blob) throw new Error(i18n.t('common.imageReadFailed'))
    const url = await resolveCanvasAssetURL(input)
    const meta = await readImageMeta(url)
    return { url, storageKey: input, width: meta.width, height: meta.height, bytes: blob.size, mimeType: blob.type || meta.mimeType }
  }
  const blob = typeof input === 'string' ? await fetchBlob(input) : input
  const previewURL = URL.createObjectURL(blob)
  try {
    const meta = await readImageMeta(previewURL)
    const asset = await uploadCanvasAsset(
      blob,
      `image.${imageExtension(blob.type || meta.mimeType)}`,
      getActiveCanvasProjectID(),
      { width: meta.width, height: meta.height }
    )
    const storageKey = assetStorageKey(asset.id)
    return {
      url: await resolveCanvasAssetURL(storageKey, asset.url),
      storageKey,
      width: asset.width || meta.width,
      height: asset.height || meta.height,
      bytes: asset.byte_size || blob.size,
      mimeType: asset.mime_type || blob.type || meta.mimeType
    }
  } finally {
    URL.revokeObjectURL(previewURL)
  }
}

export function resolveImageUrl(storageKey?: string, fallback = ''): Promise<string> {
  return resolveCanvasAssetURL(storageKey, fallback)
}

export function getImageBlob(storageKey: string): Promise<Blob | null> {
  return getCanvasAssetBlob(storageKey)
}

export async function setImageBlob(storageKey: string, blob: Blob): Promise<string> {
  const uploaded = await uploadImage(blob)
  registerStorageKeyAlias(storageKey, uploaded.storageKey)
  return uploaded.url
}

export async function imageToDataUrl(image: { url?: string; dataUrl?: string; storageKey?: string }): Promise<string> {
  if (image.dataUrl?.startsWith('data:')) return image.dataUrl
  const blob = image.storageKey
    ? await getCanvasAssetBlob(image.storageKey)
    : await fetchBlob(image.dataUrl || image.url || '')
  if (!blob) return ''
  return blobToDataURL(blob)
}

export async function deleteStoredImages(keys: Iterable<string>): Promise<void> {
  releaseCanvasAssetURLs(keys)
}

export async function cleanupUnusedImages(_usedData: unknown): Promise<void> {
  // Server assets may be shared by another project or the asset library.
}

export function collectImageStorageKeys(value: unknown, keys = new Set<string>()): Set<string> {
  if (!value || typeof value !== 'object') return keys
  if ('storageKey' in value && typeof value.storageKey === 'string' && (value.storageKey.startsWith('asset:') || value.storageKey.startsWith('image:'))) {
    keys.add(value.storageKey)
  }
  Object.values(value).forEach((item) => {
    if (Array.isArray(item)) item.forEach((child) => collectImageStorageKeys(child, keys))
    else collectImageStorageKeys(item, keys)
  })
  return keys
}

async function fetchBlob(url: string): Promise<Blob> {
  if (!url) throw new Error(i18n.t('common.imageReadFailed'))
  const response = await fetch(url)
  if (!response.ok) throw new Error(i18n.t('common.imageReadFailed'))
  return response.blob()
}

function blobToDataURL(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result || ''))
    reader.onerror = () => reject(new Error(i18n.t('common.imageReadFailed')))
    reader.readAsDataURL(blob)
  })
}

function imageExtension(mimeType: string): string {
  if (mimeType.includes('jpeg')) return 'jpg'
  if (mimeType.includes('webp')) return 'webp'
  return 'png'
}
