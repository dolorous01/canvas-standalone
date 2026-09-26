import type { CanvasCapability, CanvasModel } from '@sub2api/api/canvas-api'

export const defaultImageCapability: CanvasCapability = {
  media_kind: 'image', provider: 'openai', dimension_mode: 'size',
  generation: true, edit: false, multi_image: false, mask: false,
  max_input_images: 0, max_outputs: 1, sizes: ['1024x1024'],
  output_formats: ['png'], defaults: { size: '1024x1024', output_format: 'png' }
}

export const capabilityOptionFields = {
  sizes: 'size', aspect_ratios: 'aspect_ratio', resolutions: 'resolution',
  qualities: 'quality', output_formats: 'output_format', backgrounds: 'background'
} as const
export type CapabilityOptionField = keyof typeof capabilityOptionFields

export function setCapabilityOptions(capability: CanvasCapability, field: CapabilityOptionField, options: string[]): CanvasCapability {
  const values = [...new Set(options.map((value) => value.trim()).filter(Boolean))]
  const defaultField = capabilityOptionFields[field]
  const defaults = { ...capability.defaults }
  const current = defaults[defaultField]
  if (current && !values.includes(current)) defaults[defaultField] = values[0]
  return { ...capability, [field]: values, defaults }
}

export function setReferenceImages(capability: CanvasCapability, enabled: boolean, count: number): CanvasCapability {
  const max = enabled ? Math.max(1, Math.min(32, Math.floor(count || 1))) : 0
  return { ...capability, edit: enabled, max_input_images: max, multi_image: enabled && max > 1, mask: enabled && capability.mask }
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
  if (value.defaults !== undefined && (!value.defaults || typeof value.defaults !== 'object' || Array.isArray(value.defaults))) {
    throw new Error('invalidDefaults')
  }
  for (const [field, defaultField] of Object.entries(capabilityOptionFields)) {
    const options = value[field as CapabilityOptionField]
    if (options !== undefined && (!Array.isArray(options) || options.some((option) => typeof option !== 'string' || !option.trim()))) {
      throw new Error('invalidOptions')
    }
    const selected = value.defaults?.[defaultField as typeof capabilityOptionFields[CapabilityOptionField]]
    if (selected !== undefined && (typeof selected !== 'string' || !options?.some((option) => option.toLowerCase() === selected.toLowerCase()))) {
      throw new Error('invalidDefaults')
    }
  }
  if (value.dimension_mode === 'size' && !value.sizes?.length) throw new Error('requiredSizes')
  if (value.defaults?.background === 'transparent' && value.defaults?.output_format === 'jpeg') throw new Error('transparentJPEG')
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
