import { InputNumber, Select, Switch } from 'antd'
import { useTranslation } from 'react-i18next'
import type { CanvasCapability } from '@sub2api/api/canvas-api'
import { capabilityOptionFields, setCapabilityOptions, setReferenceImages, type CapabilityOptionField } from './model-policy-form'

const choices: Record<CapabilityOptionField, string[]> = {
  sizes: ['auto', '1024x1024', '1536x1024', '1024x1536', '2048x2048', '2048x1152', '1152x2048', '3840x2160', '2160x3840'],
  aspect_ratios: ['auto', '1:1', '16:9', '9:16', '4:3', '3:4', '3:2', '2:3'],
  resolutions: ['1k', '2k'],
  qualities: ['auto', 'low', 'medium', 'high'],
  output_formats: ['png', 'jpeg', 'webp'],
  backgrounds: ['auto', 'opaque', 'transparent']
}

export function ModelCapabilityFields({ capability, onChange, disabled = false }: {
  capability: CanvasCapability
  onChange: (value: CanvasCapability) => void
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const change = (values: Partial<CanvasCapability>) => onChange({ ...capability, ...values })
  const optionRow = (field: CapabilityOptionField) => {
    const values = Array.isArray(capability[field]) ? capability[field]!.filter((value): value is string => typeof value === 'string') : []
    const defaultField = capabilityOptionFields[field]
    const selectedDefault = capability.defaults?.[defaultField] ?? ''
    return <div key={field} className="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2">
      <label className="flex min-w-0 flex-col gap-1.5">
        <span>{t(`modelPolicy.options_${field}`)}</span>
        <Select aria-label={t(`modelPolicy.options_${field}`)} mode="tags" className="w-full" value={values} disabled={disabled}
          tokenSeparators={[',', '，']} placeholder={t('modelPolicy.selectOptions')}
          options={[...new Set([...choices[field], ...values])].map((value) => ({ value, label: value }))}
          onChange={(next: string[]) => onChange(setCapabilityOptions(capability, field, next))} />
      </label>
      <label className="flex min-w-0 flex-col gap-1.5">
        <span>{t(`modelPolicy.default_${field}`)}</span>
        <select aria-label={t(`modelPolicy.default_${field}`)} className="min-w-0 rounded border border-stone-300 bg-background p-2"
          disabled={disabled || !values.length} value={selectedDefault}
          onChange={(event) => change({ defaults: { ...capability.defaults, [defaultField]: event.target.value || undefined } })}>
          <option value="">{t('modelPolicy.upstreamDefault')}</option>
          {values.map((value) => <option key={value} value={value}>{value}</option>)}
          {selectedDefault && !values.includes(selectedDefault) && <option value={selectedDefault}>{selectedDefault}</option>}
        </select>
      </label>
    </div>
  }
  return <div className="flex min-w-0 flex-col gap-4">
    <p className="text-xs text-stone-500">{t('modelPolicy.simpleHelp')}</p>
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
      <label className="flex flex-col gap-1.5">{t('modelPolicy.maxOutputs')}
        <InputNumber aria-label={t('modelPolicy.maxOutputs')} min={1} max={10} precision={0} value={capability.max_outputs} disabled={disabled}
          onChange={(value) => change({ max_outputs: value ?? 1 })} />
      </label>
      <label className="flex items-center gap-3"><Switch aria-label={t('modelPolicy.generation')} checked={capability.generation} disabled={disabled}
        onChange={(generation) => change({ generation })} />{t('modelPolicy.generation')}</label>
      <label className="flex items-center gap-3"><Switch aria-label={t('modelPolicy.imageEdit')} checked={capability.edit} disabled={disabled}
        onChange={(edit) => onChange(setReferenceImages(capability, edit, capability.max_input_images))} />{t('modelPolicy.referenceImages')}</label>
      <label className="flex flex-col gap-1.5">{t('modelPolicy.maxInputs')}
        <InputNumber aria-label={t('modelPolicy.maxInputs')} min={1} max={32} precision={0} value={capability.edit ? capability.max_input_images : 0}
          disabled={disabled || !capability.edit} onChange={(value) => onChange(setReferenceImages(capability, true, value ?? 1))} />
      </label>
    </div>
    {capability.dimension_mode === 'aspect_ratio_resolution' ? <>{optionRow('aspect_ratios')}{optionRow('resolutions')}</> : optionRow('sizes')}
    {optionRow('qualities')}
    {optionRow('output_formats')}
    <details>
      <summary className="cursor-pointer text-sm">{t('modelPolicy.moreOptions')}</summary>
      <div className="mt-3 flex flex-col gap-3">
        {optionRow('backgrounds')}
        <label className="flex items-center gap-3"><Switch aria-label={t('modelPolicy.mask')} checked={capability.mask} disabled={disabled || !capability.edit}
          onChange={(mask) => change({ mask })} />{t('modelPolicy.mask')}</label>
      </div>
    </details>
  </div>
}
