import type { CanvasHostContext } from '@sub2api/host-context'

let currentHost: CanvasHostContext | undefined
const listeners = new Set<(host: CanvasHostContext) => void>()

export function setCanvasRuntimeHost(host: CanvasHostContext): void {
  currentHost = host
  for (const listener of listeners) listener(host)
}

export function clearCanvasRuntimeHost(host: CanvasHostContext): void {
  if (currentHost === host) currentHost = undefined
}

export function getCanvasRuntimeHost(): CanvasHostContext {
  if (!currentHost) throw new Error('Canvas runtime host is not initialized')
  return currentHost
}

export function subscribeCanvasRuntimeHost(listener: (host: CanvasHostContext) => void): () => void {
  listeners.add(listener)
  if (currentHost) listener(currentHost)
  return () => listeners.delete(listener)
}
