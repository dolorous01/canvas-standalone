import type {
  CanvasJob,
  CanvasMediaTask,
  CanvasProject as ServerCanvasProject
} from '@sub2api/api/canvas-api'
import type { CanvasConnection, CanvasNodeData, CanvasNodeImage } from '@/types/canvas'

type RecoveryKind = 'image' | 'video' | 'audio'
type RecoverySnapshot = CanvasJob | CanvasMediaTask

export type CanvasRecoveryProject = {
  nodes: CanvasNodeData[]
  connections: CanvasConnection[]
}

export type CanvasRecoveryBinding = {
  taskID: string
  kind: RecoveryKind
  sourceNodeID: string
  targetNodeID: string
  imageID?: string
  snapshot: RecoverySnapshot
}

export type CanvasRecoveryRecord = Omit<CanvasRecoveryBinding, 'snapshot'>

const terminalStatuses = new Set(['completed', 'partial', 'failed', 'canceled', 'indeterminate', 'expired'])

export function buildCanvasRecoveryBindings(
  project: CanvasRecoveryProject,
  jobs: CanvasJob[] = [],
  mediaTasks: CanvasMediaTask[] = [],
  claimedBindings: CanvasRecoveryRecord[] = []
): CanvasRecoveryBinding[] {
  const claimedImages = new Set(claimedBindings.flatMap((binding) =>
    binding.imageID ? [`${binding.targetNodeID}\u0000${binding.imageID}`] : []
  ))
  const snapshots = [
    ...jobs.map((snapshot) => ({ kind: 'image' as const, snapshot })),
    ...mediaTasks.map((snapshot) => ({ kind: snapshot.kind, snapshot }))
  ].sort((left, right) => snapshotCreatedAt(left.snapshot) - snapshotCreatedAt(right.snapshot))

  const bindings: CanvasRecoveryBinding[] = []
  for (const { kind, snapshot } of snapshots) {
    const sourceNodeID = snapshot.client_node_id || ''
    if (!sourceNodeID) continue
    const target = findRecoveryTarget(project, sourceNodeID, kind, claimedImages)
    if (!target) continue
    if (target.imageID) claimedImages.add(`${target.nodeID}\u0000${target.imageID}`)
    bindings.push({
      taskID: snapshot.id,
      kind,
      sourceNodeID,
      targetNodeID: target.nodeID,
      imageID: target.imageID,
      snapshot
    })
  }
  return bindings
}

export function isTerminalRecoverySnapshot(snapshot: RecoverySnapshot): boolean {
  return terminalStatuses.has(snapshot.status)
}

export function applyCanvasRecoverySnapshot<T extends CanvasRecoveryProject>(
  project: T,
  binding: CanvasRecoveryBinding,
  snapshot: RecoverySnapshot
): T {
  const successful = snapshot.status === 'completed' || snapshot.status === 'partial'
  const result = snapshot.results.find((item) => item.asset_id)
  const error = snapshot.error?.message || `${binding.kind} generation ${snapshot.status}`
  const nodes = project.nodes.map((node) => {
    if (node.id !== binding.targetNodeID) return node
    if (binding.kind === 'image') {
      return reconcileImageNode(node, binding.imageID, successful ? result : undefined, error)
    }
    if (successful && result?.asset_id) {
      return {
        ...node,
        metadata: {
          ...node.metadata,
          content: '',
          storageKey: `asset:${result.asset_id}`,
          mimeType: result.mime_type || (binding.kind === 'video' ? 'video/mp4' : 'audio/mpeg'),
          status: 'success' as const,
          errorDetails: undefined
        }
      }
    }
    return {
      ...node,
      metadata: { ...node.metadata, status: 'error' as const, errorDetails: error }
    }
  })
  return { ...project, nodes }
}

export function settleCanvasRecoverySources<T extends CanvasRecoveryProject>(
  project: T,
  sourceNodeIDs: Iterable<string>,
  activeBindings: CanvasRecoveryBinding[]
): T {
  const sources = new Set(sourceNodeIDs)
  const activeSources = new Set(activeBindings.map((binding) => binding.sourceNodeID))
  const nodes = project.nodes.map((node) => {
    if (!sources.has(node.id) || activeSources.has(node.id) || node.metadata?.status !== 'loading') return node
    const targets = project.connections
      .filter((connection) => connection.fromNodeId === node.id)
      .map((connection) => project.nodes.find((candidate) => candidate.id === connection.toNodeId))
      .filter((candidate): candidate is CanvasNodeData => Boolean(candidate))
    const succeeded = targets.some((target) => target.metadata?.status === 'success' || target.metadata?.images?.some((image) => image.status === 'success'))
    const failure = targets.find((target) => target.metadata?.errorDetails)?.metadata?.errorDetails
    return {
      ...node,
      metadata: {
        ...node.metadata,
        status: succeeded ? 'success' as const : 'error' as const,
        errorDetails: succeeded ? undefined : failure || 'Generation could not be recovered'
      }
    }
  })
  return { ...project, nodes }
}

export function serverProjectRecoveries(project: ServerCanvasProject): {
  jobs: CanvasJob[]
  mediaTasks: CanvasMediaTask[]
} {
  return {
    jobs: project.open_jobs || [],
    mediaTasks: project.media_tasks || []
  }
}

function findRecoveryTarget(
  project: CanvasRecoveryProject,
  sourceNodeID: string,
  kind: RecoveryKind,
  claimedImages: Set<string>
): { nodeID: string; imageID?: string } | undefined {
  const direct = project.nodes.find((node) => node.id === sourceNodeID)
  const downstreamIDs = project.connections
    .filter((connection) => connection.fromNodeId === sourceNodeID)
    .map((connection) => connection.toNodeId)
  const candidates = [direct, ...downstreamIDs.map((id) => project.nodes.find((node) => node.id === id))]
    .filter((node): node is CanvasNodeData => Boolean(node))
    .filter((node) => node.type === kind && node.metadata?.status === 'loading')

  for (const node of candidates) {
    if (kind !== 'image' || !node.metadata?.images?.length) return { nodeID: node.id }
    const image = node.metadata.images.find((item) =>
      item.status === 'loading' && !claimedImages.has(`${node.id}\u0000${item.id}`)
    )
    if (image) return { nodeID: node.id, imageID: image.id }
  }
  return undefined
}

function reconcileImageNode(
  node: CanvasNodeData,
  imageID: string | undefined,
  result: CanvasJob['results'][number] | CanvasMediaTask['results'][number] | undefined,
  error: string
): CanvasNodeData {
  if (!imageID) {
    if (result?.asset_id) {
      return {
        ...node,
        metadata: {
          ...node.metadata,
          content: '',
          storageKey: `asset:${result.asset_id}`,
          mimeType: result.mime_type || 'image/png',
          status: 'success',
          errorDetails: undefined
        }
      }
    }
    return { ...node, metadata: { ...node.metadata, status: 'error', errorDetails: error } }
  }

  const images = (node.metadata?.images || []).map((image): CanvasNodeImage => {
    if (image.id !== imageID) return image
    if (!result?.asset_id) return { ...image, status: 'error', errorDetails: error }
    return {
      ...image,
      status: 'success',
      errorDetails: undefined,
      content: '',
      storageKey: `asset:${result.asset_id}`,
      mimeType: result.mime_type || 'image/png'
    }
  })
  const recovered = images.find((image) => image.id === imageID && image.status === 'success')
  const hasLoading = images.some((image) => image.status === 'loading')
  const hasSuccess = images.some((image) => image.status === 'success')
  return {
    ...node,
    metadata: {
      ...node.metadata,
      images,
      status: hasLoading ? 'loading' : hasSuccess ? 'success' : 'error',
      errorDetails: hasLoading || hasSuccess ? undefined : error,
      ...(recovered && !node.metadata?.storageKey
        ? {
            content: '',
            storageKey: recovered.storageKey,
            mimeType: recovered.mimeType,
            primaryImageId: recovered.id
          }
        : {})
    }
  }
}

function snapshotCreatedAt(snapshot: RecoverySnapshot): number {
  return typeof snapshot.created_at === 'number' ? snapshot.created_at : 0
}
