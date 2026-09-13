import { createStandaloneHost, canvasBaseForPath } from '@sub2api/runtime/standalone-host'
import { mountStandaloneCanvas } from './entry'

const root = document.getElementById('root')
if (!root) throw new Error('Canvas root element is missing')

document.documentElement.dataset.canvasStandalone = 'true'
mountStandaloneCanvas(root, createStandaloneHost(), canvasBaseForPath(window.location.pathname))
