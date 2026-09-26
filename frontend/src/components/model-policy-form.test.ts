import { describe, expect, it } from 'vitest'
import { buildPolicyModels, defaultImageCapability } from './model-policy-form'

describe('model policy form', () => {
  const capability = { ...defaultImageCapability, custom_extension: { keep: true }, defaults: { size: '1024x1024', quality: 'high' } }
  const models = [{ model: 'gpt-image-2', enabled: true, position: 0, capability }]

  it('adds a named model without losing existing or advanced capability settings', () => {
    const next = buildPolicyModels(models, null, ' gpt-image-2.5-flare ', true, JSON.stringify(capability))
    expect(next).toHaveLength(2)
    expect(next[0]).toEqual(models[0])
    expect(next[1]).toEqual({ model: 'gpt-image-2.5-flare', enabled: true, position: 1, capability })
    expect(models).toHaveLength(1)
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
