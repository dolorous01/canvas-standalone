import {
  assetStorageKey,
  getCanvasAssetBlob,
  registerStorageKeyAlias,
  releaseCanvasAssetURLs,
  resolveCanvasAssetURL,
  uploadCanvasAsset
} from '@sub2api/adapters/asset-runtime'
import { getActiveCanvasProjectID } from '@sub2api/adapters/use-canvas-store'

export type UploadedFile = {
  url: string
  storageKey: string
  bytes: number
  mimeType: string
  width?: number
  height?: number
  durationMs?: number
}

export async function uploadMediaFile(input: string | Blob, prefix = 'file'): Promise<UploadedFile> {
  const blob = typeof input === 'string' ? await fetchMediaBlob(input) : input
  const previewURL = URL.createObjectURL(blob)
  try {
    const metadata: { width?: number; height?: number; durationMs?: number } = blob.type.startsWith('video/')
      ? await readVideoMeta(previewURL)
      : blob.type.startsWith('audio/') ? await readAudioMeta(previewURL) : {}
    const asset = await uploadCanvasAsset(
      blob,
      `${prefix}.${mediaExtension(blob.type)}`,
      getActiveCanvasProjectID(),
      metadata
    )
    const storageKey = assetStorageKey(asset.id)
    return {
      url: await resolveCanvasAssetURL(storageKey, asset.url),
      storageKey,
      bytes: asset.byte_size || blob.size,
      mimeType: asset.mime_type || blob.type || 'application/octet-stream',
      width: asset.width || metadata.width,
      height: asset.height || metadata.height,
      durationMs: asset.duration_ms || metadata.durationMs
    }
  } finally {
    URL.revokeObjectURL(previewURL)
  }
}

export function resolveMediaUrl(storageKey?: string, fallback = ''): Promise<string> {
  return resolveCanvasAssetURL(storageKey, fallback)
}

export function getMediaBlob(storageKey: string): Promise<Blob | null> {
  return getCanvasAssetBlob(storageKey)
}

export async function setMediaBlob(storageKey: string, blob: Blob): Promise<string> {
  const uploaded = await uploadMediaFile(blob, blob.type.startsWith('audio/') ? 'audio' : 'video')
  registerStorageKeyAlias(storageKey, uploaded.storageKey)
  return uploaded.url
}

export async function deleteStoredMedia(keys: Iterable<string>): Promise<void> {
  releaseCanvasAssetURLs(keys)
}

export async function cleanupUnusedMedia(_usedData: unknown): Promise<void> {
  // Server assets are retained until an explicit server lifecycle policy removes them.
}

export function collectMediaStorageKeys(value: unknown, keys = new Set<string>()): Set<string> {
  if (!value || typeof value !== 'object') return keys
  if ('storageKey' in value && typeof value.storageKey === 'string' && value.storageKey.includes(':')) keys.add(value.storageKey)
  Object.values(value).forEach((item) => {
    if (Array.isArray(item)) item.forEach((child) => collectMediaStorageKeys(child, keys))
    else collectMediaStorageKeys(item, keys)
  })
  return keys
}

async function fetchMediaBlob(url: string): Promise<Blob> {
  const response = await fetch(url)
  if (!response.ok) throw new Error(`Media request failed (${response.status})`)
  return response.blob()
}

function readVideoMeta(url: string): Promise<{ width: number; height: number; durationMs?: number }> {
  return new Promise((resolve, reject) => {
    const video = document.createElement('video')
    video.preload = 'metadata'
    video.onloadedmetadata = () => resolve({
      width: video.videoWidth,
      height: video.videoHeight,
      durationMs: Number.isFinite(video.duration) ? Math.round(video.duration * 1000) : undefined
    })
    video.onerror = () => reject(new Error('Video metadata could not be read'))
    video.src = url
  })
}

function readAudioMeta(url: string): Promise<{ durationMs?: number }> {
  return new Promise((resolve, reject) => {
    const audio = document.createElement('audio')
    audio.preload = 'metadata'
    audio.onloadedmetadata = () => resolve({ durationMs: Number.isFinite(audio.duration) ? Math.round(audio.duration * 1000) : undefined })
    audio.onerror = () => reject(new Error('Audio metadata could not be read'))
    audio.src = url
  })
}

function mediaExtension(mimeType: string): string {
  const subtype = mimeType.split('/')[1]?.split(';')[0] || 'bin'
  if (subtype === 'mpeg') return 'mp3'
  if (subtype === 'mp4' && mimeType.startsWith('audio/')) return 'm4a'
  return subtype.replace(/[^a-z0-9]/gi, '') || 'bin'
}
