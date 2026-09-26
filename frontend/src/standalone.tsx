import { createStandaloneHost, canvasBaseForPath, resolveStandaloneRole } from '@sub2api/runtime/standalone-host'
import { mountStandaloneCanvas } from './entry'

const root = document.getElementById('root')
if (!root) throw new Error('Canvas root element is missing')

document.documentElement.dataset.canvasStandalone = 'true'
root.textContent = 'Loading…'
void resolveStandaloneRole(createStandaloneHost()).then((host) => {
  root.replaceChildren()
  mountStandaloneCanvas(root, host, canvasBaseForPath(window.location.pathname))
}).catch(() => {
  root.textContent = navigator.language.startsWith('zh')
    ? '创作台加载失败，请刷新页面重试。' : 'Studio could not load. Refresh the page to retry.'
})
