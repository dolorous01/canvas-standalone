import { nanoid } from 'nanoid'
import { create } from 'zustand'

import i18n from '@/i18n'
import { localForageStorage } from '@/lib/localforage-storage'
import { assetIDFromStorageKey, assetStorageKey } from '@sub2api/adapters/asset-runtime'
import { uploadImage } from '@sub2api/adapters/image-storage'
import { uploadMediaFile } from '@sub2api/adapters/file-storage'
import {
  createCanvasAPI,
  type CanvasLibraryItem,
  type CanvasLibraryItemWrite
} from '@sub2api/api/canvas-api'
import { getCanvasRuntimeHost } from '@sub2api/runtime/host-runtime'
import { getImageBlob as getLegacyImageBlob } from '../upstream/services/image-storage'
import { getMediaBlob as getLegacyMediaBlob } from '../upstream/services/file-storage'

export type AssetKind = 'text' | 'image' | 'video' | 'audio'
export type TextAsset = AssetBase<'text'> & { data: { content: string } }
export type ImageAsset = AssetBase<'image'> & {
  data: { dataUrl: string; storageKey?: string; width: number; height: number; bytes: number; mimeType: string }
}
export type VideoAsset = AssetBase<'video'> & {
  data: { url: string; storageKey?: string; width: number; height: number; bytes: number; mimeType: string; durationMs?: number }
}
export type AudioAsset = AssetBase<'audio'> & {
  data: { url: string; storageKey?: string; bytes: number; mimeType: string; durationMs?: number }
}
export type Asset = TextAsset | ImageAsset | VideoAsset | AudioAsset

type AssetBase<T extends AssetKind> = {
  id: string
  kind: T
  title: string
  coverUrl: string
  tags: string[]
  source?: string
  note?: string
  createdAt: string
  updatedAt: string
  metadata?: Record<string, unknown>
}

export type NewAsset<T extends Asset = Asset> = T extends Asset
  ? Omit<T, 'id' | 'createdAt' | 'updatedAt'>
  : never
type AssetPatch<T extends Asset = Asset> = T extends Asset
  ? Partial<Omit<T, 'id' | 'createdAt'>>
  : never

type AssetStore = {
  hydrated: boolean
  assets: Asset[]
  addAsset: (asset: NewAsset) => string
  addAssetAsync: (asset: NewAsset) => Promise<string>
  updateAsset: (id: string, patch: AssetPatch) => void
  updateAssetAsync: (id: string, patch: AssetPatch) => Promise<void>
  removeAsset: (id: string) => void
  removeAssetAsync: (id: string) => Promise<void>
  replaceAssets: (assets: Asset[]) => void
  cleanupImages: (extra?: unknown) => void
}

type LibrarySync = { serverID: string; version: number }

const LEGACY_ASSET_STORE_KEY = 'infinite-canvas:asset_store'
const LIBRARY_DATA_KEY = '__sub2api_library_data'
const LIBRARY_COVER_KEY = '__sub2api_cover_url'
const syncByClientID = new Map<string, LibrarySync>()
const pendingCreates = new Map<string, Promise<LibrarySync>>()
const mutationQueues = new Map<string, Promise<void>>()
const coverObjectURLs = new Set<string>()
let initialization: Promise<void> | undefined
let initializationVersion = 0

function api() {
  return createCanvasAPI(getCanvasRuntimeHost())
}

function nowISO(): string {
  return new Date().toISOString()
}

function newAssetValue(asset: NewAsset): Asset {
  const now = nowISO()
  return { ...asset, id: nanoid(), createdAt: now, updatedAt: now } as Asset
}

function insertOptimisticAsset(asset: NewAsset): Asset {
  const value = newAssetValue(asset)
  useAssetStore.setState((state) => ({ assets: [value, ...state.assets] }))
  return value
}

async function createServerItem(asset: Asset): Promise<LibrarySync> {
  const request = await libraryWrite(asset, undefined, true)
  let item = await api().createLibraryItem(request)
  if (!libraryItemMatchesWrite(item, request)) {
    item = await api().updateLibraryItem(item.id, await libraryWrite(asset, item.version, false))
  }
  const sync = { serverID: item.id, version: item.version }
  syncByClientID.set(asset.id, sync)
  useAssetStore.setState((state) => ({
    assets: state.assets.map((value) => value.id === asset.id
      ? { ...value, createdAt: item.created_at, updatedAt: item.updated_at }
      : value)
  }))
  return sync
}

function persistNewAsset(asset: Asset): Promise<LibrarySync> {
  const existing = pendingCreates.get(asset.id)
  if (existing) return existing
  const pending = createServerItem(asset).finally(() => {
    if (pendingCreates.get(asset.id) === pending) pendingCreates.delete(asset.id)
  })
  pendingCreates.set(asset.id, pending)
  return pending
}

function removeOptimisticAsset(id: string): void {
  useAssetStore.setState((state) => ({ assets: state.assets.filter((asset) => asset.id !== id) }))
}

function notifyPersistenceFailure(error: unknown): void {
  getCanvasRuntimeHost().notify(
    'error',
    readableError(error, i18n.t('canvas.sidePanel.addFailed', { defaultValue: 'Asset could not be saved' }))
  )
}

function enqueueMutation(id: string, operation: () => Promise<void>): Promise<void> {
  const previous = mutationQueues.get(id) || Promise.resolve()
  const next = previous.catch(() => undefined).then(operation)
  mutationQueues.set(id, next)
  void next.finally(() => {
    if (mutationQueues.get(id) === next) mutationQueues.delete(id)
  }).catch(() => undefined)
  return next
}

function updateAssetAndPersist(id: string, patch: AssetPatch): Promise<void> {
  const previous = useAssetStore.getState().assets.find((asset) => asset.id === id)
  if (!previous) return Promise.resolve()
  const updatedAt = nowISO()
  useAssetStore.setState((state) => ({
    assets: state.assets.map((asset) => asset.id === id ? { ...asset, ...patch, updatedAt } as Asset : asset)
  }))
  return enqueueMutation(id, async () => {
    try {
      const pending = pendingCreates.get(id)
      if (pending) await pending
      const sync = syncByClientID.get(id)
      const current = useAssetStore.getState().assets.find((asset) => asset.id === id)
      if (!sync || !current) return
      const item = await api().updateLibraryItem(sync.serverID, await libraryWrite(current, sync.version, false))
      syncByClientID.set(id, { serverID: item.id, version: item.version })
      useAssetStore.setState((state) => ({
        assets: state.assets.map((asset) => asset.id === id ? { ...asset, updatedAt: item.updated_at } : asset)
      }))
    } catch (error) {
      const latest = useAssetStore.getState().assets.find((asset) => asset.id === id)
      if (latest?.updatedAt === updatedAt) {
        useAssetStore.setState((state) => ({
          assets: state.assets.map((asset) => asset.id === id ? previous : asset)
        }))
      }
      throw error
    }
  })
}

function removeAssetAndPersist(id: string): Promise<void> {
  const assets = useAssetStore.getState().assets
  const previousIndex = assets.findIndex((asset) => asset.id === id)
  if (previousIndex < 0) return Promise.resolve()
  const previous = assets[previousIndex]
  removeOptimisticAsset(id)
  return enqueueMutation(id, async () => {
    try {
      const pending = pendingCreates.get(id)
      if (pending) {
        try {
          await pending
        } catch {
          return
        }
      }
      const sync = syncByClientID.get(id)
      if (!sync) return
      await api().deleteLibraryItem(sync.serverID)
      syncByClientID.delete(id)
    } catch (error) {
      useAssetStore.setState((state) => state.assets.some((asset) => asset.id === id)
        ? state
        : { assets: [...state.assets.slice(0, previousIndex), previous, ...state.assets.slice(previousIndex)] })
      throw error
    }
  })
}

export const useAssetStore = create<AssetStore>()((set, get) => ({
  hydrated: false,
  assets: [],
  addAsset: (asset) => {
    const value = insertOptimisticAsset(asset)
    void persistNewAsset(value).catch((error) => {
      removeOptimisticAsset(value.id)
      notifyPersistenceFailure(error)
    })
    return value.id
  },
  addAssetAsync: async (asset) => {
    const value = insertOptimisticAsset(asset)
    try {
      await persistNewAsset(value)
      return value.id
    } catch (error) {
      removeOptimisticAsset(value.id)
      throw error
    }
  },
  updateAsset: (id, patch) => {
    void updateAssetAndPersist(id, patch).catch(notifyPersistenceFailure)
  },
  updateAssetAsync: updateAssetAndPersist,
  removeAsset: (id) => {
    void removeAssetAndPersist(id).catch(notifyPersistenceFailure)
  },
  removeAssetAsync: removeAssetAndPersist,
  replaceAssets: (assets) => {
    const previous = get().assets
    const nextIDs = new Set(assets.map((asset) => asset.id))
    for (const asset of previous) {
      if (!nextIDs.has(asset.id)) get().removeAsset(asset.id)
    }
    set({ assets })
    for (const asset of assets) {
      if (syncByClientID.has(asset.id)) get().updateAsset(asset.id, asset)
      else void persistNewAsset(asset).catch(notifyPersistenceFailure)
    }
  },
  cleanupImages: () => undefined
}))

export function initializeCanvasAssetStore(): Promise<void> {
  if (initialization) return initialization
  const version = ++initializationVersion
  initialization = (async () => {
    let legacyAssets: Asset[] = []
    try {
      legacyAssets = await readLegacyAssets()
      const items = await api().listLibraryItems()
      const itemByClientID = new Map(items.map((item) => [item.client_id, item]))
      const migrationFailures: Asset[] = []
      for (const legacy of legacyAssets) {
        if (itemByClientID.has(legacy.id)) continue
        try {
          const migrated = await migrateLegacyAsset(legacy)
          const created = await api().createLibraryItem(await libraryWrite(migrated, undefined, true))
          itemByClientID.set(created.client_id, created)
        } catch {
          migrationFailures.push(legacy)
        }
      }
      if (version !== initializationVersion) return
      syncByClientID.clear()
      const serverAssets = await Promise.all(Array.from(itemByClientID.values()).map(async (item) => {
        syncByClientID.set(item.client_id, { serverID: item.id, version: item.version })
        return assetFromLibraryItem(item)
      }))
      useAssetStore.setState({ assets: [...serverAssets, ...migrationFailures], hydrated: true })
      if (migrationFailures.length === 0) {
        await localForageStorage.removeItem(LEGACY_ASSET_STORE_KEY)
      } else {
        getCanvasRuntimeHost().notify(
          'warning',
          i18n.t('assets.migrationPartial', {
            count: migrationFailures.length,
            defaultValue: `${migrationFailures.length} local assets could not be migrated`
          })
        )
      }
    } catch (error) {
      if (version !== initializationVersion) return
      useAssetStore.setState({ assets: legacyAssets, hydrated: true })
      getCanvasRuntimeHost().notify('error', readableError(error, 'Asset library could not be loaded'))
    }
  })()
  return initialization
}

export function resetCanvasAssetStore(): void {
  initializationVersion += 1
  initialization = undefined
  syncByClientID.clear()
  pendingCreates.clear()
  mutationQueues.clear()
  for (const url of coverObjectURLs) URL.revokeObjectURL(url)
  coverObjectURLs.clear()
  useAssetStore.setState({ assets: [], hydrated: false })
}

async function libraryWrite(asset: Asset, version?: number, includeClientID = false): Promise<CanvasLibraryItemWrite> {
  const metadata = { ...(asset.metadata || {}) } as Record<string, unknown>
  metadata[LIBRARY_COVER_KEY] = durableCoverURL(asset.coverUrl)
  let assetID: string | undefined
  if (asset.kind === 'text') {
    metadata[LIBRARY_DATA_KEY] = {}
  } else {
    assetID = assetIDFromStorageKey(asset.data.storageKey)
    if (!assetID) throw new Error('Asset binary has not been uploaded')
    metadata[LIBRARY_DATA_KEY] = asset.kind === 'image'
      ? { width: asset.data.width, height: asset.data.height, bytes: asset.data.bytes, mimeType: asset.data.mimeType }
      : asset.kind === 'video'
        ? { width: asset.data.width, height: asset.data.height, bytes: asset.data.bytes, mimeType: asset.data.mimeType, durationMs: asset.data.durationMs }
        : { bytes: asset.data.bytes, mimeType: asset.data.mimeType, durationMs: asset.data.durationMs }
  }
  return {
    ...(includeClientID ? { client_id: asset.id } : {}),
    ...(version ? { version } : {}),
    kind: asset.kind,
    ...(assetID ? { asset_id: assetID } : {}),
    title: asset.title,
    ...(asset.kind === 'text' ? { content: asset.data.content } : {}),
    tags: asset.tags || [],
    source: asset.source || '',
    note: asset.note || '',
    metadata
  }
}

async function assetFromLibraryItem(item: CanvasLibraryItem): Promise<Asset> {
  const sourceMetadata = item.metadata && typeof item.metadata === 'object' ? { ...item.metadata } : {}
  const data = objectValue(sourceMetadata[LIBRARY_DATA_KEY])
  const storedCover = stringValue(sourceMetadata[LIBRARY_COVER_KEY])
  delete sourceMetadata[LIBRARY_DATA_KEY]
  delete sourceMetadata[LIBRARY_COVER_KEY]
  const base = {
    id: item.client_id,
    title: item.title,
    coverUrl: storedCover,
    tags: Array.isArray(item.tags) ? item.tags : [],
    source: item.source,
    note: item.note,
    createdAt: item.created_at,
    updatedAt: item.updated_at,
    metadata: sourceMetadata
  }
  if (item.kind === 'text') {
    return { ...base, kind: 'text', data: { content: item.content || '' } }
  }
  const storageKey = item.asset_id ? assetStorageKey(item.asset_id) : undefined
  if (item.kind === 'image') {
    let coverUrl = storedCover
    if (item.asset_id) {
      try {
        const thumbnail = await api().getAssetBlob(item.asset_id, undefined, true)
        coverUrl = URL.createObjectURL(thumbnail)
        coverObjectURLs.add(coverUrl)
      } catch {
        // The original remains available lazily through storageKey.
      }
    }
    return {
      ...base,
      kind: 'image',
      coverUrl,
      data: {
        dataUrl: '', storageKey,
        width: numberValue(data.width), height: numberValue(data.height),
        bytes: numberValue(data.bytes), mimeType: stringValue(data.mimeType) || 'image/png'
      }
    }
  }
  if (item.kind === 'video') {
    return {
      ...base,
      kind: 'video',
      data: {
        url: '', storageKey,
        width: numberValue(data.width), height: numberValue(data.height),
        bytes: numberValue(data.bytes), mimeType: stringValue(data.mimeType) || 'video/mp4',
        durationMs: optionalNumberValue(data.durationMs)
      }
    }
  }
  return {
    ...base,
    kind: 'audio',
    data: {
      url: '', storageKey,
      bytes: numberValue(data.bytes), mimeType: stringValue(data.mimeType) || 'audio/mpeg',
      durationMs: optionalNumberValue(data.durationMs)
    }
  }
}

async function readLegacyAssets(): Promise<Asset[]> {
  const raw = await localForageStorage.getItem(LEGACY_ASSET_STORE_KEY)
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw) as { state?: { assets?: unknown[] } }
    return Array.isArray(parsed.state?.assets)
      ? parsed.state.assets.filter(isLegacyAsset).map((asset) => normalizeLegacyAsset(asset as Asset))
      : []
  } catch {
    return []
  }
}

async function migrateLegacyAsset(source: Asset): Promise<Asset> {
  let asset = normalizeLegacyAsset(source)
  if (asset.kind === 'image' && asset.data.mimeType.startsWith('audio/')) {
    asset = {
      ...asset,
      kind: 'audio',
      coverUrl: '',
      data: {
        url: asset.data.dataUrl,
        storageKey: asset.data.storageKey,
        bytes: asset.data.bytes,
        mimeType: asset.data.mimeType
      }
    }
  }
  if (asset.kind === 'text' || assetIDFromStorageKey(asset.data.storageKey)) return asset
  if (asset.kind === 'image') {
    const blob = await legacyAssetBlob(asset)
    const uploaded = await uploadImage(blob)
    return { ...asset, coverUrl: uploaded.url, data: { ...asset.data, dataUrl: uploaded.url, storageKey: uploaded.storageKey, width: uploaded.width, height: uploaded.height, bytes: uploaded.bytes, mimeType: uploaded.mimeType } }
  }
  const blob = await legacyAssetBlob(asset)
  const uploaded = await uploadMediaFile(blob, asset.kind)
  if (asset.kind === 'video') {
    return { ...asset, data: { ...asset.data, url: uploaded.url, storageKey: uploaded.storageKey, width: uploaded.width || asset.data.width, height: uploaded.height || asset.data.height, bytes: uploaded.bytes, mimeType: uploaded.mimeType, durationMs: uploaded.durationMs } }
  }
  return { ...asset, data: { ...asset.data, url: uploaded.url, storageKey: uploaded.storageKey, bytes: uploaded.bytes, mimeType: uploaded.mimeType, durationMs: uploaded.durationMs } }
}

async function legacyAssetBlob(asset: Exclude<Asset, TextAsset>): Promise<Blob> {
  const storageKey = asset.data.storageKey
  if (storageKey) {
    const blob = asset.kind === 'image' ? await getLegacyImageBlob(storageKey) : await getLegacyMediaBlob(storageKey)
    if (blob) return blob
  }
  const url = asset.kind === 'image' ? asset.data.dataUrl || asset.coverUrl : asset.data.url
  if (!url) throw new Error('Local asset binary is missing')
  const response = await fetch(url)
  if (!response.ok) throw new Error(`Local asset could not be read (${response.status})`)
  return response.blob()
}

function isLegacyAsset(value: unknown): boolean {
  if (!value || typeof value !== 'object') return false
  const item = value as { id?: unknown; kind?: unknown; title?: unknown; data?: unknown }
  return typeof item.id === 'string' && typeof item.title === 'string' &&
    (item.kind === 'text' || item.kind === 'image' || item.kind === 'video' || item.kind === 'audio') &&
    !!item.data && typeof item.data === 'object'
}

function normalizeLegacyAsset(asset: Asset): Asset {
  return {
    ...asset,
    coverUrl: typeof asset.coverUrl === 'string' ? asset.coverUrl : '',
    tags: Array.isArray(asset.tags) ? asset.tags.filter((tag): tag is string => typeof tag === 'string') : [],
    createdAt: asset.createdAt || nowISO(),
    updatedAt: asset.updatedAt || asset.createdAt || nowISO(),
    metadata: objectValue(asset.metadata)
  }
}

function durableCoverURL(value: string): string {
  return /^https?:\/\//i.test(value) ? value : ''
}

function objectValue(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function optionalNumberValue(value: unknown): number | undefined {
  const number = numberValue(value)
  return number > 0 ? number : undefined
}

function readableError(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback
}

function libraryItemMatchesWrite(item: CanvasLibraryItem, write: CanvasLibraryItemWrite): boolean {
  return item.client_id === write.client_id &&
    item.kind === write.kind &&
    (item.asset_id || '') === (write.asset_id || '') &&
    item.title === write.title.trim() &&
    (item.content || '') === (write.content || '') &&
    sameStringArray(item.tags, normalizeTagsForComparison(write.tags)) &&
    (item.source || '') === (write.source || '').trim() &&
    (item.note || '') === (write.note || '').trim() &&
    canonicalJSON(item.metadata || {}) === canonicalJSON(write.metadata || {})
}

function normalizeTagsForComparison(tags: string[]): string[] {
  const result: string[] = []
  const seen = new Set<string>()
  for (const value of tags) {
    const tag = value.trim()
    const key = tag.toLocaleLowerCase()
    if (!tag || seen.has(key)) continue
    seen.add(key)
    result.push(tag)
  }
  return result
}

function sameStringArray(left: string[], right: string[]): boolean {
  return left.length === right.length && left.every((value, index) => value === right[index])
}

function canonicalJSON(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonicalJSON).join(',')}]`
  if (value && typeof value === 'object') {
    const entries = Object.entries(value as Record<string, unknown>).sort(([left], [right]) => left.localeCompare(right))
    return `{${entries.map(([key, entry]) => `${JSON.stringify(key)}:${canonicalJSON(entry)}`).join(',')}}`
  }
  return JSON.stringify(value) ?? 'null'
}
