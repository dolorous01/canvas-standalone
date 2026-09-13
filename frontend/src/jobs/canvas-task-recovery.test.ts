import { describe, expect, it } from 'vitest'

import type { CanvasJob, CanvasMediaTask } from '@sub2api/api/canvas-api'
import type { CanvasNodeData } from '@/types/canvas'
import {
  applyCanvasRecoverySnapshot,
  buildCanvasRecoveryBindings,
  settleCanvasRecoverySources
} from './canvas-task-recovery'

function node(id: string, type: CanvasNodeData['type'], images?: string[]): CanvasNodeData {
  return {
    id,
    type,
    title: id,
    position: { x: 0, y: 0 },
    width: 320,
    height: 240,
    metadata: {
      status: 'loading',
      images: images?.map((imageID) => ({
        id: imageID,
        status: 'loading',
        content: '',
        storageKey: '',
        naturalWidth: 0,
        naturalHeight: 0,
        bytes: 0,
        mimeType: ''
      }))
    }
  }
}

function imageJob(id: string, status: CanvasJob['status'], assetID?: string, createdAt = 1): CanvasJob {
  return {
    id,
    status,
    operation: 'generation',
    client_node_id: 'source',
    attempt_plan: ['gpt-image-1'],
    requested_count: 1,
    completed_count: assetID ? 1 : 0,
    results: assetID ? [{ index: 0, status: 'completed', asset_id: assetID, mime_type: 'image/png' }] : [],
    created_at: createdAt
  }
}

describe('canvas task recovery', () => {
  it('attaches a completed video task to the loading downstream node', () => {
    const project = {
      nodes: [node('source', 'config'), node('video-target', 'video')],
      connections: [{ id: 'edge', fromNodeId: 'source', toNodeId: 'video-target' }]
    }
    const task: CanvasMediaTask = {
      id: 'video-task',
      kind: 'video',
      status: 'completed',
      project_id: 'project',
      client_node_id: 'source',
      selected_model: 'grok-imagine-video',
      results: [{ index: 0, asset_id: 'video-asset', mime_type: 'video/mp4' }]
    }

    const [binding] = buildCanvasRecoveryBindings(project, [], [task])
    const recovered = applyCanvasRecoverySnapshot(project, binding, task)
    const settled = settleCanvasRecoverySources(recovered, ['source'], [])

    expect(binding.targetNodeID).toBe('video-target')
    expect(settled.nodes[1].metadata).toMatchObject({
      status: 'success',
      storageKey: 'asset:video-asset',
      mimeType: 'video/mp4'
    })
    expect(settled.nodes[0].metadata?.status).toBe('success')
  })

  it('binds concurrent image jobs to stable image slots and keeps the source active', () => {
    const project = {
      nodes: [node('source', 'config'), node('image-target', 'image', ['slot-1', 'slot-2'])],
      connections: [{ id: 'edge', fromNodeId: 'source', toNodeId: 'image-target' }]
    }
    const completed = imageJob('job-1', 'completed', 'image-asset', 1)
    const running = imageJob('job-2', 'running', undefined, 2)
    const bindings = buildCanvasRecoveryBindings(project, [running, completed], [])

    expect(bindings.map((binding) => binding.imageID)).toEqual(['slot-1', 'slot-2'])
    const recovered = applyCanvasRecoverySnapshot(project, bindings[0], completed)
    const settled = settleCanvasRecoverySources(recovered, ['source'], [bindings[1]])

    expect(settled.nodes[1].metadata?.images?.[0]).toMatchObject({
      status: 'success',
      storageKey: 'asset:image-asset'
    })
    expect(settled.nodes[1].metadata?.images?.[1].status).toBe('loading')
    expect(settled.nodes[0].metadata?.status).toBe('loading')
  })
})
