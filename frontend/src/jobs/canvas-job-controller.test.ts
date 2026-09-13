import { describe, expect, it, vi } from 'vitest'
import type { CanvasAPI, CanvasJob, CanvasJobCreate } from '@sub2api/api/canvas-api'
import { createCanvasJobController } from './canvas-job-controller'

function job(status: CanvasJob['status']): CanvasJob {
  return {
    id: 'job-1',
    status,
    operation: 'generation',
    attempt_plan: ['gpt-image-1'],
    requested_count: 1,
    completed_count: status === 'completed' ? 1 : 0,
    results: []
  }
}

const input: CanvasJobCreate & { generationNonce: string } = {
  project_id: 'project-1',
  client_node_id: 'node-1',
  operation: 'generation',
  api_key_id: 42,
  selected_model: 'gpt-image-1',
  prompt: 'A quiet operations dashboard',
  input_asset_ids: [],
  parameters: { n: 1 },
  generationNonce: 'nonce-1'
}

describe('createCanvasJobController', () => {
  it('uses a stable idempotency key and stops on a streamed terminal state', async () => {
    const createJob = vi.fn().mockResolvedValue(job('queued'))
    const streamJob = vi.fn(async function* () {
      yield job('running')
      yield job('completed')
    })
    const api = { createJob, streamJob } as unknown as CanvasAPI

    const result = await createCanvasJobController(api).run(input)

    expect(result.status).toBe('completed')
    expect(createJob).toHaveBeenCalledWith(
      expect.not.objectContaining({ generationNonce: expect.anything() }),
      'project-1:node-1:nonce-1'
    )
    expect(streamJob).toHaveBeenCalledWith('job-1', undefined)
  })

  it('falls back to polling when the event stream disconnects', async () => {
    vi.useFakeTimers()
    try {
      const getJob = vi.fn()
        .mockResolvedValueOnce(job('queued'))
        .mockResolvedValueOnce(job('completed'))
      // This stream fails before its first event to exercise polling fallback.
      // eslint-disable-next-line require-yield
      const streamJob = vi.fn(async function* () {
        throw new Error('connection lost')
      })
      const api = { getJob, streamJob } as unknown as CanvasAPI

      const resultPromise = createCanvasJobController(api).resume('job-1')
      await vi.runAllTimersAsync()

      await expect(resultPromise).resolves.toMatchObject({ status: 'completed' })
      expect(getJob).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it('stops observing on abort without canceling the server job', async () => {
    const aborter = new AbortController()
    const cancelJob = vi.fn()
    const streamJob = vi.fn(async function* (_id: string, signal?: AbortSignal) {
      if (signal?.aborted) throw signal.reason
      yield job('running')
    })
    const api = {
      createJob: vi.fn().mockResolvedValue(job('queued')),
      streamJob,
      cancelJob
    } as unknown as CanvasAPI

    const result = createCanvasJobController(api).run(input, aborter.signal, () => aborter.abort())

    await expect(result).rejects.toBeTruthy()
    expect(cancelJob).not.toHaveBeenCalled()
  })

  it('only cancels through the explicit cancel command', async () => {
    const cancelJob = vi.fn().mockResolvedValue(job('canceled'))
    const controller = createCanvasJobController({ cancelJob } as unknown as CanvasAPI)

    await expect(controller.cancel('job-1')).resolves.toMatchObject({ status: 'canceled' })
    expect(cancelJob).toHaveBeenCalledWith('job-1')
  })
})
