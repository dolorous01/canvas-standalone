import type { CanvasConfig } from '@sub2api/api/canvas-api'
import { useCanvasI18n } from '@sub2api/i18n'

interface Props {
  config?: CanvasConfig
  operation: 'generation' | 'edit'
  apiKeyID?: number
  model?: string
  onAPIKeyChange(id?: number): void
  onModelChange(model?: string): void
  onCreateKey(): void
}

export function APIKeyModelSelector({ config, operation, apiKeyID, model, onAPIKeyChange, onModelChange, onCreateKey }: Props) {
  const t = useCanvasI18n()
  const models = (config?.models || []).filter((item) => item.capability?.[operation])
  if (config && config.api_keys.length === 0) {
    return <button className="canvas-command primary" onClick={onCreateKey}>{t('createKey')}</button>
  }
  return (
    <div className="canvas-selectors" data-canvas-no-zoom>
      <label>
        <span>{t('apiKey')}</span>
        <select
          aria-label={t('apiKey')}
          value={apiKeyID || ''}
          onChange={(event) => onAPIKeyChange(event.target.value ? Number(event.target.value) : undefined)}
        >
          <option value="">-</option>
          {(config?.api_keys || []).map((key) => <option key={key.id} value={key.id}>{key.name} · {key.group_name}</option>)}
        </select>
      </label>
      <label>
        <span>{t('model')}</span>
        <select aria-label={t('model')} value={model || ''} onChange={(event) => onModelChange(event.target.value || undefined)} disabled={!apiKeyID}>
          <option value="">-</option>
          {models.map((item) => <option key={item.model} value={item.model}>{item.model}</option>)}
        </select>
      </label>
    </div>
  )
}
