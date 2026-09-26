import { useEffect, useState } from 'react'
import { Alert, Button, Spin } from 'antd'
import { useTranslation } from 'react-i18next'
import type { CanvasAPI, CanvasCredentialCandidate, CanvasDiscoveredModel, CanvasModelDiscovery } from '@sub2api/api/canvas-api'

export function ModelDiscoveryPicker({ api, onSelect, onReset }: {
  api: CanvasAPI
  onSelect: (model: CanvasDiscoveredModel) => void
  onReset: () => void
}) {
  const { t } = useTranslation()
  const [keys, setKeys] = useState<CanvasCredentialCandidate[]>([])
  const [loadingKeys, setLoadingKeys] = useState(true)
  const [keyID, setKeyID] = useState('')
  const [reload, setReload] = useState(0)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [result, setResult] = useState<CanvasModelDiscovery>()
  const [selected, setSelected] = useState('')

  useEffect(() => {
    let active = true
    void api.listCredentialCandidates().then((items) => {
      if (!active) return
      const available = items.filter((key) => key.bound && key.status === 'active'
        && !(key.quota > 0 && key.quota_used >= key.quota)
        && (!key.expires_at || Date.parse(key.expires_at) > Date.now()))
      setKeys(available)
      if (available.length === 1) setKeyID(String(available[0].id))
    }).catch(() => { if (active) setError(t('modelPolicy.keysFailed')) })
      .finally(() => { if (active) setLoadingKeys(false) })
    return () => { active = false }
  }, [api, t])

  useEffect(() => {
    if (!keyID) return
    let active = true
    setLoading(true)
    setError('')
    setResult(undefined)
    void api.discoverModels(Number(keyID)).then((data) => {
      if (active && data.api_key_id === Number(keyID)) setResult(data)
    }).catch(() => { if (active) setError(t('modelPolicy.discoveryFailed')) })
      .finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [api, keyID, reload, t])

  const reset = () => { setSelected(''); setResult(undefined); onReset() }

  return <div className="flex flex-col gap-3">
    <label className="flex flex-col gap-1.5">{t('modelPolicy.discoveryKey')}
      <select aria-label={t('modelPolicy.discoveryKey')} className="w-full min-w-0 rounded border border-stone-300 bg-background p-2"
        value={keyID} disabled={loadingKeys} onChange={(event) => { reset(); setKeyID(event.target.value) }}>
        <option value="">{t('modelPolicy.chooseKey')}</option>
        {keys.map((key) => <option key={key.id} value={key.id}>{key.name} · #{key.id}</option>)}
      </select>
    </label>
    {loadingKeys && <Spin />}
    {!loadingKeys && !keys.length && !error && <Alert type="warning" title={t('modelPolicy.noDiscoveryKeys')} />}
    <Button disabled={!keyID || loading} onClick={() => { reset(); setReload((value) => value + 1) }}>{t('modelPolicy.probeAgain')}</Button>
    {loading && <Spin description={t('modelPolicy.discovering')} />}
    {error && <Alert type="error" title={error} />}
    {result && <>
      <p className="text-sm text-stone-500">{t('modelPolicy.discoveryCount', { total: result.total, count: result.models.length })}</p>
      {!result.models.length ? <Alert type="warning" title={t('modelPolicy.noImageModels')} /> :
        <label className="flex flex-col gap-1.5">{t('modelPolicy.discoveredModel')}
          <select aria-label={t('modelPolicy.discoveredModel')} className="w-full min-w-0 rounded border border-stone-300 bg-background p-2"
            value={selected} onChange={(event) => {
              const model = result.models.find((item) => item.model === event.target.value)
              setSelected(event.target.value)
              if (model && !model.configured) onSelect(model); else onReset()
            }}>
            <option value="">{t('modelPolicy.chooseModel')}</option>
            {result.models.map((model) => <option key={model.model} value={model.model} disabled={model.configured}>
              {model.model}{model.configured ? ' · ' + t('modelPolicy.alreadyConfigured') : ''}
            </option>)}
          </select>
        </label>}
      <Alert type="info" title={t('modelPolicy.discoveryNotice')} />
    </>}
  </div>
}
