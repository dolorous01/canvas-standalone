import { nanoid } from 'nanoid'
import type { CanvasAPI, CanvasJob, CanvasJobCreate } from '@sub2api/api/canvas-api'

export interface CanvasGeneration extends CanvasJobCreate {
  generationNonce?: string
}

export interface CanvasJobController {
  run(input: CanvasGeneration, signal?: AbortSignal, onUpdate?: (job: CanvasJob) => void): Promise<CanvasJob>
  resume(jobID: string, signal?: AbortSignal, onUpdate?: (job: CanvasJob) => void): Promise<CanvasJob>
  cancel(jobID: string): Promise<CanvasJob>
}

export function createCanvasJobController(api: CanvasAPI): CanvasJobController {
  const terminal = (job: CanvasJob) =>
    ['completed', 'partial', 'failed', 'canceled', 'indeterminate', 'expired'].includes(job.status)

  const monitor = async (job: CanvasJob, signal?: AbortSignal, onUpdate?: (job: CanvasJob) => void): Promise<CanvasJob> => {
    onUpdate?.(job)
    if (terminal(job)) return job
    try {
      for await (const snapshot of api.streamJob(job.id, signal)) {
        job = snapshot
        onUpdate?.(job)
        if (terminal(job)) return job
      }
    } catch (error) {
      if (signal?.aborted) throw error
    }
    let delay = 1000
    while (!terminal(job)) {
      await wait(delay, signal)
      job = await api.getJob(job.id)
      onUpdate?.(job)
      delay = Math.min(5000, delay * 2)
    }
    return job
  }

  return {
    async run(input, signal, onUpdate) {
      const idempotencyKey = `${input.project_id}:${input.client_node_id}:${input.generationNonce || nanoid()}`
      const { generationNonce: _, ...request } = input
      let job = await api.createJob(request, idempotencyKey)
      job = await monitor(job, signal, onUpdate)
      return job
    },
    resume: (jobID, signal, onUpdate) => api.getJob(jobID).then((job) => monitor(job, signal, onUpdate)),
    cancel: (jobID) => api.cancelJob(jobID)
  }
}

function wait(milliseconds: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const cleanup = () => signal?.removeEventListener('abort', abort)
    const timer = setTimeout(() => {
      cleanup()
      resolve()
    }, milliseconds)
    const abort = () => {
      clearTimeout(timer)
      cleanup()
      reject(signal?.reason || new DOMException('Aborted', 'AbortError'))
    }
    if (signal?.aborted) abort()
    else signal?.addEventListener('abort', abort, { once: true })
  })
}
