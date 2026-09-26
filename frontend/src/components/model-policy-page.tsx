import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Alert, App, Button, Input, Modal, Popconfirm, Spin, Switch, Tag } from 'antd'
import { ArrowLeft, Plus, RefreshCw } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { createCanvasAPI, type CanvasCapability, type CanvasModelPolicy } from '@sub2api/api/canvas-api'
import { refreshCanvasConfigStore } from '@sub2api/adapters/use-config-store'
import { useCanvasHost } from '@sub2api/host-context'
import { CanvasRequestError } from '@sub2api/runtime/standalone-host'
import { buildPolicyModels, defaultImageCapability } from './model-policy-form'
import { ModelDiscoveryPicker } from './model-discovery-picker'
import { ModelCapabilityFields } from './model-capability-fields'

type Draft = { index: number | null; name: string; enabled: boolean; capability: string }

export default function ModelPolicyPage({ compact = false }: { compact?: boolean }) {
  const host = useCanvasHost()
  const { t } = useTranslation()
  const { message } = App.useApp()
  const navigate = useNavigate()
  const api = useMemo(() => createCanvasAPI(host), [host])
  const [policy, setPolicy] = useState<CanvasModelPolicy>()
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const savingRef = useRef(false)
  const [error, setError] = useState('')
  const [conflict, setConflict] = useState(false)
  const [draft, setDraft] = useState<Draft>()
  const [draftError, setDraftError] = useState('')
  const [discoverySession, setDiscoverySession] = useState(0)
  const admin = host.routeMode === 'admin'

  const load = useCallback(async () => {
    if (!admin) { setLoading(false); return }
    setLoading(true)
    setError('')
    try {
      setPolicy(await api.getModelPolicy())
      setDraft(undefined)
      setConflict(false)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('modelPolicy.loadFailed'))
    } finally { setLoading(false) }
  }, [admin, api, t])
  useEffect(() => { void load() }, [load])

  const save = async (next: CanvasModelPolicy) => {
    if (!admin || savingRef.current || conflict) return false
    savingRef.current = true
    setSaving(true)
    setError('')
    try {
      const saved = await api.updateModelPolicy(next)
      setPolicy(saved)
      setDraft(undefined)
      message.success(t('modelPolicy.saved'))
      await refreshCanvasConfigStore()
      return true
    } catch (cause) {
      const stale = cause instanceof CanvasRequestError && cause.status === 409
      setConflict(stale)
      setError(stale ? t('modelPolicy.conflict') : cause instanceof Error ? cause.message : t('modelPolicy.saveFailed'))
      return false
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  const edit = (index: number | null) => {
    if (!policy) return
    setDiscoverySession((value) => value + 1)
    const model = index === null ? undefined : policy.models[index]
    setDraft({ index, name: model?.model ?? '', enabled: model?.enabled ?? true,
      capability: JSON.stringify(model?.capability ?? defaultImageCapability, null, 2) })
    setDraftError('')
    setError('')
  }

  const saveDraft = async () => {
    if (!policy || !draft) return
    try {
      const models = buildPolicyModels(policy.models, draft.index, draft.name, draft.enabled, draft.capability)
      setDraftError('')
      await save({ ...policy, models })
    } catch (cause) {
      setDraftError(t(`modelPolicy.${cause instanceof Error ? cause.message : 'invalidCapability'}`))
    }
  }

  let capability: CanvasCapability | undefined
  try {
    const parsed = JSON.parse(draft?.capability ?? 'null')
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) capability = parsed
  } catch { /* Keep invalid advanced input available for correction. */ }
  const Container = compact ? 'section' : 'main'

  return (
    <Container aria-label={t('modelPolicy.title')} className={`h-full overflow-auto text-stone-950 dark:text-stone-100 ${compact ? 'bg-transparent' : 'bg-background'}`}>
      <div className={compact ? 'flex min-w-0 flex-col gap-3 px-3 pb-4' : 'mx-auto flex max-w-5xl flex-col gap-5 px-5 py-8 sm:px-8'}>
        <header className="flex flex-wrap items-center justify-between gap-4 border-b border-stone-200 pb-5 dark:border-stone-800">
          <div className="flex items-center gap-3">
            {!compact && <Button aria-label={t('modelPolicy.back')} icon={<ArrowLeft className="size-4" />} onClick={() => navigate('/canvas')} />}
            <div><h1 className={compact ? 'text-base font-semibold' : 'text-xl font-semibold'}>{t('modelPolicy.title')}</h1>
              {!compact && <p className="mt-1 text-sm text-stone-500">{t('modelPolicy.description')}</p>}</div>
          </div>
          {admin && <div className="flex gap-2">
            <Button aria-label={t('modelPolicy.reload')} title={t('modelPolicy.reload')} size={compact ? 'small' : 'middle'} icon={<RefreshCw className="size-4" />} disabled={saving} loading={loading} onClick={() => void load()}>{!compact && t('modelPolicy.reload')}</Button>
            <Button size={compact ? 'small' : 'middle'} type="primary" icon={<Plus className="size-4" />} disabled={!policy || loading || saving || conflict || policy.models.length >= 100} onClick={() => edit(null)}>{t('modelPolicy.add')}</Button>
          </div>}
        </header>
        {!admin ? <Alert type="warning" title={t('modelPolicy.adminOnly')} /> : loading ? <Spin /> : <>
          {error && <Alert type="error" title={error} />}
          {policy && <>
            <p className="text-sm text-stone-500">{t(compact ? 'modelPolicy.scopeCompact' : 'modelPolicy.scope')}</p>
            <div className="flex flex-wrap items-center gap-2">
              <Switch aria-label={t('modelPolicy.globalEnabled')} checked={policy.enabled} disabled={saving || conflict} onChange={(enabled) => void save({ ...policy, enabled })} />
              <span>{t('modelPolicy.globalEnabled')}</span>
              <span className="text-xs text-stone-500">{t('modelPolicy.version', { version: policy.version })}</span>
            </div>
            <div className={compact ? 'flex flex-col gap-3' : 'divide-y divide-stone-200 dark:divide-stone-800'}>
              {policy.models.map((model, index) => (
                <section key={model.model} className={compact ? 'rounded-xl border border-stone-200 p-3 dark:border-stone-700' : 'flex flex-wrap items-center justify-between gap-4 py-5'}>
                  <div className="min-w-0">
                    <h2 className="break-all font-mono font-medium">{model.model}</h2>
                    <div className="mt-2 flex flex-wrap gap-1">
                      <Tag color={model.enabled ? 'green' : 'default'}>{t(model.enabled ? 'modelPolicy.enabled' : 'modelPolicy.disabled')}</Tag>
                      {model.capability.generation && <Tag>{t('modelPolicy.generation')}</Tag>}
                      {model.capability.edit && <Tag>{t('modelPolicy.imageEdit')}</Tag>}
                      <Tag>{t('modelPolicy.outputLimit', { count: model.capability.max_outputs })}</Tag>
                    </div>
                  </div>
                  <div className={compact ? 'mt-3 flex flex-wrap items-center gap-2' : 'flex items-center gap-3'}>
                    <Switch size={compact ? 'small' : 'medium'} aria-label={t('modelPolicy.modelEnabled', { model: model.model })} checked={model.enabled} disabled={saving || conflict} onChange={(enabled) => void save({ ...policy, models: policy.models.map((item, i) => i === index ? { ...item, enabled } : item) })} />
                    <Button size={compact ? 'small' : 'middle'} disabled={saving || conflict} onClick={() => edit(index)}>{t('modelPolicy.edit')}</Button>
                    <Popconfirm title={t('modelPolicy.deleteConfirm', { model: model.model })} okText={t('modelPolicy.remove')} cancelText={t('modelPolicy.cancel')}
                      onConfirm={() => save({ ...policy, models: policy.models.filter((_, i) => i !== index).map((item, position) => ({ ...item, position })) })}>
                      <Button size={compact ? 'small' : 'middle'} danger disabled={saving || conflict || policy.models.length <= 1}>{t('modelPolicy.remove')}</Button>
                    </Popconfirm>
                  </div>
                </section>
              ))}
            </div>
            <p className="text-xs text-stone-500">{t('modelPolicy.keepOne')}</p>
          </>}
        </>}
      </div>
      <Modal open={!!draft && admin} title={t(draft?.index === null ? 'modelPolicy.add' : 'modelPolicy.edit')}
        width={720} styles={{ body: { maxHeight: '65vh', overflowY: 'auto', paddingRight: 4 } }} okText={t('modelPolicy.save')} cancelText={t('modelPolicy.cancel')} confirmLoading={saving}
        okButtonProps={{ disabled: conflict || !draft?.name }} cancelButtonProps={{ disabled: saving }} closable={!saving} mask={{ closable: false }}
        onOk={() => void saveDraft()} onCancel={() => { if (!saving) setDraft(undefined) }}>
        {draft && <div className="flex flex-col gap-4 py-3">
          {(draftError || error) && <Alert type="error" title={draftError || error} />}
          {conflict && <Button onClick={() => void load()}>{t('modelPolicy.reloadDiscard')}</Button>}
          {draft.index === null && <ModelDiscoveryPicker key={discoverySession} api={api}
            onReset={() => setDraft((current) => current ? { ...current, name: '' } : current)}
            onSelect={(model) => {
              setDraft((current) => current ? { ...current, name: model.model, capability: JSON.stringify(model.capability, null, 2) } : current)
              setDraftError('')
            }} />}
          <label className="flex flex-col gap-1.5">{t('modelPolicy.name')}
            <Input value={draft.name} readOnly placeholder={t('modelPolicy.chooseModel')} />
          </label>
          {draft.index === null && <label className="flex flex-col gap-1.5">{t('modelPolicy.template')}
            <select key={draft.name} className="rounded border border-stone-300 bg-background p-2" defaultValue="" onChange={(e) => {
              const template = policy?.models.find((item) => item.model === e.target.value)
              if (template) setDraft({ ...draft, capability: JSON.stringify(template.capability, null, 2) })
            }}><option value="" disabled>{t('modelPolicy.templateHint')}</option>
              {policy?.models.map((item) => <option key={item.model} value={item.model}>{item.model}</option>)}
            </select>
          </label>}
          <div className="flex items-center gap-3"><Switch aria-label={t('modelPolicy.enabled')} checked={draft.enabled} onChange={(enabled) => setDraft({ ...draft, enabled })} />{t('modelPolicy.enabled')}</div>
          {capability && <ModelCapabilityFields capability={capability} disabled={saving || conflict}
            onChange={(value) => setDraft((current) => current ? { ...current, capability: JSON.stringify(value, null, 2) } : current)} />}
          <details><summary className="cursor-pointer text-sm">{t('modelPolicy.advanced')}</summary>
            <p className="my-2 text-xs text-stone-500">{t('modelPolicy.advancedHelp')}</p>
            <Input.TextArea aria-label={t('modelPolicy.advanced')} rows={12} value={draft.capability} onChange={(e) => setDraft({ ...draft, capability: e.target.value })} />
          </details>
        </div>}
      </Modal>
    </Container>
  )
}
