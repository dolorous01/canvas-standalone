import type { CanvasCapability, CanvasModel } from '@sub2api/api/canvas-api'

export const defaultImageCapability: CanvasCapability = {
  media_kind: 'image', provider: 'openai', dimension_mode: 'size',
  generation: true, edit: false, multi_image: false, mask: false,
  max_input_images: 0, max_outputs: 1, sizes: ['1024x1024'],
  defaults: { size: '1024x1024' }
}

export function parseImageCapability(raw: string): CanvasCapability {
  let value: CanvasCapability
  try { value = JSON.parse(raw) } catch { throw new Error('invalidJSON') }
  if (!value || typeof value !== 'object' || Array.isArray(value) || value.media_kind !== 'image'
    || typeof value.generation !== 'boolean' || typeof value.edit !== 'boolean'
    || (!value.generation && !value.edit)
    || !Number.isInteger(value.max_input_images) || value.max_input_images < 0 || value.max_input_images > 32
    || !Number.isInteger(value.max_outputs) || value.max_outputs < 1 || value.max_outputs > 10
    || new TextEncoder().encode(raw).length > 65536) {
    throw new Error('invalidCapability')
  }
  return value
}

export function buildPolicyModels(models: CanvasModel[], index: number | null, name: string, enabled: boolean, raw: string): CanvasModel[] {
  const model = name.trim()
  if (!model || model.length > 128 || /\s/.test(model)) throw new Error('invalidName')
  if (models.some((item, position) => position !== index && item.model.toLowerCase() === model.toLowerCase())) {
    throw new Error('duplicateName')
  }
  if (index === null && models.length >= 100) throw new Error('modelLimit')
  const item: CanvasModel = { model, enabled, position: index ?? models.length, capability: parseImageCapability(raw) }
  const next = index === null ? [...models, item] : models.map((current, position) => position === index ? item : current)
  return next.map((current, position) => ({ ...current, position }))
}
