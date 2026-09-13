import { createCanvasAPI, type CanvasAsset } from '@sub2api/api/canvas-api'
import { getCanvasRuntimeHost } from '@sub2api/runtime/host-runtime'

const aliases = new Map<string, string>()
const objectURLs = new Map<string, string>()
const blobs = new Map<string, Blob>()

export function assetStorageKey(assetID: string): string {
  return `asset:${assetID}`
}

export function resolveStorageKeyAlias(storageKey: string): string {
  let current = storageKey
  const seen = new Set<string>()
  while (aliases.has(current) && !seen.has(current)) {
    seen.add(current)
    current = aliases.get(current)!
  }
  return current
}

export function registerStorageKeyAlias(from: string, to: string): void {
  if (from && to && from !== to) aliases.set(from, to)
}

export function assetIDFromStorageKey(storageKey?: string): string | undefined {
  const resolved = resolveStorageKeyAlias(storageKey || '')
  return resolved.startsWith('asset:') ? resolved.slice('asset:'.length) || undefined : undefined
}

export async function uploadCanvasAsset(
  blob: Blob,
  fileName: string,
  projectID?: string,
  metadata?: { width?: number; height?: number; durationMs?: number }
): Promise<CanvasAsset> {
  const file = blob instanceof File ? blob : new File([blob], fileName, { type: blob.type || 'application/octet-stream' })
  const asset = await createCanvasAPI(getCanvasRuntimeHost()).uploadAsset(file, projectID, metadata)
  blobs.set(assetStorageKey(asset.id), blob)
  return asset
}

export async function resolveCanvasAssetURL(storageKey?: string, fallback = ''): Promise<string> {
  if (!storageKey) return fallback
  const resolved = resolveStorageKeyAlias(storageKey)
  const cached = objectURLs.get(resolved)
  if (cached) return cached
  const blob = await getCanvasAssetBlob(resolved)
  if (!blob) return fallback
  const url = URL.createObjectURL(blob)
  objectURLs.set(resolved, url)
  return url
}

export async function getCanvasAssetBlob(storageKey: string): Promise<Blob | null> {
  const resolved = resolveStorageKeyAlias(storageKey)
  const cached = blobs.get(resolved)
  if (cached) return cached
  const assetID = assetIDFromStorageKey(resolved)
  if (!assetID) return null
  const blob = await createCanvasAPI(getCanvasRuntimeHost()).getAssetBlob(assetID)
  blobs.set(resolved, blob)
  return blob
}

export function cacheCanvasAssetBlob(storageKey: string, blob: Blob): void {
  const resolved = resolveStorageKeyAlias(storageKey)
  const objectURL = objectURLs.get(resolved)
  if (objectURL) URL.revokeObjectURL(objectURL)
  objectURLs.delete(resolved)
  blobs.set(resolved, blob)
}

export function releaseCanvasAssetURLs(storageKeys?: Iterable<string>): void {
  const keys = storageKeys ? Array.from(storageKeys, resolveStorageKeyAlias) : Array.from(objectURLs.keys())
  for (const key of keys) {
    const url = objectURLs.get(key)
    if (url) URL.revokeObjectURL(url)
    objectURLs.delete(key)
    blobs.delete(key)
  }
}

export function resetCanvasAssetRuntime(): void {
  releaseCanvasAssetURLs()
  aliases.clear()
}
