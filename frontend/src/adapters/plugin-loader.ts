import type { InstalledPlugin } from '@/stores/canvas/use-plugin-store'
import type { CanvasPlugin } from '@/types/canvas-plugin'

const unavailableMessage = 'Browser plugins are disabled in Sub2API Studio'

export function activatePlugin(_plugin: CanvasPlugin): void {
  throw new Error(unavailableMessage)
}

export function deactivatePlugin(_pluginID: string): void {}

export async function installPluginFromUrl(
  _url: string,
  _options?: { official?: boolean; bustCache?: boolean }
): Promise<CanvasPlugin> {
  throw new Error(unavailableMessage)
}

export async function updatePlugin(_record: InstalledPlugin): Promise<CanvasPlugin> {
  throw new Error(unavailableMessage)
}

export async function setPluginEnabled(_record: InstalledPlugin, _enabled: boolean): Promise<void> {
  throw new Error(unavailableMessage)
}

export function uninstallPlugin(_id: string): void {
  throw new Error(unavailableMessage)
}

export async function ensurePluginsLoaded(): Promise<void> {}
