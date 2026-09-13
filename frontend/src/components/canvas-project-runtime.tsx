import { useParams } from 'react-router-dom'

import UpstreamCanvasProjectPage from '@/pages/canvas/project'
import { useCanvasStore } from '@sub2api/adapters/use-canvas-store'

export default function CanvasProjectRuntime() {
  const { id = '' } = useParams<{ id: string }>()
  const recoveryRevision = useCanvasStore((state) =>
    state.projects.find((project) => project.id === id)?.recoveryRevision || 0
  )
  return <UpstreamCanvasProjectPage key={`${id}:${recoveryRevision}`} />
}
