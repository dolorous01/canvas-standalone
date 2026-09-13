import { createContext, useContext, useSyncExternalStore, type ReactNode } from 'react'

export interface CanvasHostContext {
  apiBaseURL: string
  locale: string
  theme: 'light' | 'dark'
  routeMode: 'user' | 'admin'
  request<T>(
    method: string,
    path: string,
    body?: unknown,
    headers?: Record<string, string>,
    options?: { signal?: AbortSignal; timeoutMs?: number }
  ): Promise<T>
  stream(path: string, init?: RequestInit): Promise<ReadableStream<Uint8Array>>
  navigate(path: string): void
  notify(level: 'success' | 'warning' | 'error', message: string): void
}

export interface CanvasHandle {
  updateContext(context: CanvasHostContext): void
  unmount(): void
}

export interface CanvasHostStore {
  getSnapshot(): CanvasHostContext
  subscribe(listener: () => void): () => void
  update(context: CanvasHostContext): void
}

const HostContext = createContext<CanvasHostContext | null>(null)

export function createCanvasHostStore(initial: CanvasHostContext): CanvasHostStore {
  let current = initial
  const listeners = new Set<() => void>()
  return {
    getSnapshot: () => current,
    subscribe(listener) {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    update(context) {
      current = context
      for (const listener of listeners) listener()
    }
  }
}

export function CanvasHostProvider({ store, children }: { store: CanvasHostStore; children: ReactNode }) {
  const context = useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot)
  return <HostContext.Provider value={context}>{children}</HostContext.Provider>
}

export function useCanvasHost(): CanvasHostContext {
  const value = useContext(HostContext)
  if (!value) throw new Error('CanvasHostProvider is missing')
  return value
}
