export type OfficialPluginEntry = {
  id: string
  name: string
  version: string
  description?: string
  icon?: string
  url: string
}

export async function fetchOfficialPlugins(): Promise<OfficialPluginEntry[]> {
  return []
}

export function hasUpgrade(installedVersion: string, remoteVersion: string): boolean {
  return compareVersion(remoteVersion, installedVersion) > 0
}

function compareVersion(left: string, right: string): number {
  const leftParts = left.split('.').map((part) => Number.parseInt(part, 10) || 0)
  const rightParts = right.split('.').map((part) => Number.parseInt(part, 10) || 0)
  for (let index = 0; index < 3; index += 1) {
    const difference = (leftParts[index] || 0) - (rightParts[index] || 0)
    if (difference !== 0) return difference
  }
  return 0
}
