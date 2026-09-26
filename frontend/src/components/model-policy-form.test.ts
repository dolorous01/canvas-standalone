import { describe, expect, it } from 'vitest'
import { buildPolicyModels, defaultImageCapability, parseImageCapability, setCapabilityOptions, setReferenceImages } from './model-policy-form'

describe('model policy form', () => {
  const capability = { ...defaultImageCapability, custom_extension: { keep: true }, qualities: ['high'], defaults: { size: '1024x1024', quality: 'high' } }
  const models = [{ model: 'gpt-image-2', enabled: true, position: 0, capability }]

  it('adds a named model without losing existing or advanced capability settings', () => {
    const next = buildPolicyModels(models, null, ' gpt-image-2.5-flare ', true, JSON.stringify(capability))
    expect(next).toHaveLength(2)
    expect(next[0]).toEqual(models[0])
    expect(next[1]).toEqual({ model: 'gpt-image-2.5-flare', enabled: true, position: 1, capability })
    expect(models).toHaveLength(1)
  })

  it('keeps options and defaults consistent while preserving unrelated settings', () => {
    const changed = setCapabilityOptions(capability, 'sizes', [' 1536x1024 ', '', '1536x1024'])
    expect(changed.sizes).toEqual(['1536x1024'])
    expect(changed.defaults).toEqual({ size: '1536x1024', quality: 'high' })
    expect(changed).toHaveProperty('custom_extension', { keep: true })
    const cleared = setCapabilityOptions(changed, 'qualities', [])
    expect(cleared.defaults?.quality).toBeUndefined()
    expect(parseImageCapability(JSON.stringify(cleared))).toEqual(JSON.parse(JSON.stringify(cleared)))
    expect(capability.defaults.size).toBe('1024x1024')
  })

  it('updates related editing flags only when reference image settings change', () => {
    const on = setReferenceImages(capability, true, 3)
    expect(on).toMatchObject({ edit: true, multi_image: true, max_input_images: 3 })
    expect(setReferenceImages(on, true, 1)).toMatchObject({ edit: true, multi_image: false, max_input_images: 1 })
    expect(setReferenceImages({ ...on, mask: true }, false, 3)).toMatchObject({ edit: false, multi_image: false, mask: false, max_input_images: 0 })
    const legacy = { ...capability, edit: false, max_input_images: 3 }
    expect(parseImageCapability(JSON.stringify(legacy))).toEqual(legacy)
  })

  it('rejects invalid defaults and incompatible transparent JPEG before saving', () => {
    for (const defaults of [[], 'png', null, { size: '2048x2048' }, { quality: 0 }]) {
      expect(() => parseImageCapability(JSON.stringify({ ...capability, defaults }))).toThrow('invalidDefaults')
    }
    expect(() => parseImageCapability(JSON.stringify({ ...capability, sizes: [12] }))).toThrow('invalidOptions')
    expect(() => parseImageCapability(JSON.stringify({ ...capability, sizes: [], defaults: {} }))).toThrow('requiredSizes')
    expect(() => parseImageCapability(JSON.stringify({ ...capability, backgrounds: ['transparent'], output_formats: ['jpeg'],
      defaults: { background: 'transparent', output_format: 'jpeg' } }))).toThrow('transparentJPEG')
    const omittedFormat = { ...capability, output_formats: undefined, defaults: { size: '1024x1024' } }
    expect(parseImageCapability(JSON.stringify(omittedFormat)).output_formats).toBeUndefined()
  })

  it('rejects duplicates but permits editing the same model', () => {
    expect(() => buildPolicyModels(models, null, 'GPT-IMAGE-2', true, JSON.stringify(capability))).toThrow('duplicateName')
    expect(buildPolicyModels(models, 0, 'gpt-image-2', false, JSON.stringify(capability))[0].enabled).toBe(false)
  })

  it('rejects malformed JSON and unsupported capabilities before saving', () => {
    expect(() => buildPolicyModels(models, null, 'new', true, '{')).toThrow('invalidJSON')
    for (const invalid of [null, [], { ...capability, media_kind: 'video' }, { ...capability, max_outputs: 0 },
      { ...capability, max_input_images: 33 }, { ...capability, generation: false, edit: false }]) {
      expect(() => buildPolicyModels(models, null, 'new', true, JSON.stringify(invalid))).toThrow('invalidCapability')
    }
  })
})
