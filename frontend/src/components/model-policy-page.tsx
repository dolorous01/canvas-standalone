import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Alert, App, Button, Input, InputNumber, Modal, Popconfirm, Spin, Switch, Tag } from 'antd'
import { ArrowLeft, Plus, RefreshCw } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { createCanvasAPI, type CanvasCapability, type CanvasModelPolicy } from '@sub2api/api/canvas-api'
import { refreshCanvasConfigStore } from '@sub2api/adapters/use-config-store'
import { useCanvasHost } from '@sub2api/host-context'
import { CanvasRequestError } from '@sub2api/runtime/standalone-host'
import { buildPolicyModels, defaultImageCapability } from './model-policy-form'

type Draft = { index: number | null; name: string; enabled: boolean; capability: string }

export default function ModelPolicyPage() {
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
    const model = index === null ? undefined : policy.models[index]
    setDraft({ index, name: model?.model ?? '', enabled: model?.enabled ?? true,
      capability: JSON.stringify(model?.capability ?? policy.models[0]?.capability ?? defaultImageCapability, null, 2) })
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
  const setCapability = (changes: Partial<CanvasCapability>) => {
    if (draft && capability) setDraft({ ...draft, capability: JSON.stringify({ ...capability, ...changes }, null, 2) })
  }

  return (
    <main className="h-full overflow-auto bg-background text-stone-950 dark:text-stone-100">
      <div className="mx-auto flex max-w-5xl flex-col gap-5 px-5 py-8 sm:px-8">
        <header className="flex flex-wrap items-center justify-between gap-4 border-b border-stone-200 pb-5 dark:border-stone-800">
          <div className="flex items-center gap-3">
            <Button aria-label={t('modelPolicy.back')} icon={<ArrowLeft className="size-4" />} onClick={() => navigate('/canvas')} />
            <div><h1 className="text-xl font-semibold">{t('modelPolicy.title')}</h1>
              <p className="mt-1 text-sm text-stone-500">{t('modelPolicy.description')}</p></div>
          </div>
          {admin && <div className="flex gap-2">
            <Button icon={<RefreshCw className="size-4" />} disabled={saving} loading={loading} onClick={() => void load()}>{t('modelPolicy.reload')}</Button>
            <Button type="primary" icon={<Plus className="size-4" />} disabled={!policy || loading || saving || conflict || policy.models.length >= 100} onClick={() => edit(null)}>{t('modelPolicy.add')}</Button>
          </div>}
        </header>
        {!admin ? <Alert type="warning" title={t('modelPolicy.adminOnly')} /> : loading ? <Spin /> : <>
          {error && <Alert type="error" title={error} />}
          {policy && <>
            <p className="text-sm text-stone-500">{t('modelPolicy.scope')}</p>
            <div className="flex items-center gap-3">
              <Switch aria-label={t('modelPolicy.globalEnabled')} checked={policy.enabled} disabled={saving || conflict} onChange={(enabled) => void save({ ...policy, enabled })} />
              <span>{t('modelPolicy.globalEnabled')}</span>
              <span className="text-xs text-stone-500">{t('modelPolicy.version', { version: policy.version })}</span>
            </div>
            <div className="divide-y divide-stone-200 dark:divide-stone-800">
              {policy.models.map((model, index) => (
                <section key={model.model} className="flex flex-wrap items-center justify-between gap-4 py-5">
                  <div className="min-w-0">
                    <h2 className="break-all font-mono font-medium">{model.model}</h2>
                    <div className="mt-2 flex flex-wrap gap-1">
                      <Tag color={model.enabled ? 'green' : 'default'}>{t(model.enabled ? 'modelPolicy.enabled' : 'modelPolicy.disabled')}</Tag>
                      {model.capability.generation && <Tag>{t('modelPolicy.generation')}</Tag>}
                      {model.capability.edit && <Tag>{t('modelPolicy.imageEdit')}</Tag>}
                    </div>
                  </div>
                  <div className="flex items-center gap-3">
                    <Switch aria-label={t('modelPolicy.modelEnabled', { model: model.model })} checked={model.enabled} disabled={saving || conflict} onChange={(enabled) => void save({ ...policy, models: policy.models.map((item, i) => i === index ? { ...item, enabled } : item) })} />
                    <Button disabled={saving || conflict} onClick={() => edit(index)}>{t('modelPolicy.edit')}</Button>
                    <Popconfirm title={t('modelPolicy.deleteConfirm', { model: model.model })} okText={t('modelPolicy.remove')} cancelText={t('modelPolicy.cancel')}
                      onConfirm={() => save({ ...policy, models: policy.models.filter((_, i) => i !== index).map((item, position) => ({ ...item, position })) })}>
                      <Button danger disabled={saving || conflict || policy.models.length <= 1}>{t('modelPolicy.remove')}</Button>
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
        width={680} okText={t('modelPolicy.save')} cancelText={t('modelPolicy.cancel')} confirmLoading={saving}
        okButtonProps={{ disabled: conflict }} cancelButtonProps={{ disabled: saving }} closable={!saving} mask={{ closable: false }}
        onOk={() => void saveDraft()} onCancel={() => { if (!saving) setDraft(undefined) }}>
        {draft && <div className="flex flex-col gap-4 py-3">
          {(draftError || error) && <Alert type="error" title={draftError || error} />}
          {conflict && <Button onClick={() => void load()}>{t('modelPolicy.reloadDiscard')}</Button>}
          <label className="flex flex-col gap-1.5">{t('modelPolicy.name')}
            <Input value={draft.name} maxLength={128} placeholder="gpt-image-2.5-flare" onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
          </label>
          <p className="text-xs text-stone-500">{t('modelPolicy.nameHelp')}</p>
          {draft.index === null && <label className="flex flex-col gap-1.5">{t('modelPolicy.template')}
            <select className="rounded border border-stone-300 bg-background p-2" defaultValue="" onChange={(e) => {
              const template = policy?.models.find((item) => item.model === e.target.value)
              if (template) setDraft({ ...draft, capability: JSON.stringify(template.capability, null, 2) })
            }}><option value="" disabled>{t('modelPolicy.templateHint')}</option>
              {policy?.models.map((item) => <option key={item.model} value={item.model}>{item.model}</option>)}
            </select>
          </label>}
          <div className="flex items-center gap-3"><Switch aria-label={t('modelPolicy.enabled')} checked={draft.enabled} onChange={(enabled) => setDraft({ ...draft, enabled })} />{t('modelPolicy.enabled')}</div>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <label className="flex items-center gap-3"><Switch aria-label={t('modelPolicy.generation')} checked={capability?.generation} disabled={!capability} onChange={(generation) => setCapability({ generation })} />{t('modelPolicy.generation')}</label>
            <label className="flex items-center gap-3"><Switch aria-label={t('modelPolicy.imageEdit')} checked={capability?.edit} disabled={!capability} onChange={(edit) => setCapability({ edit })} />{t('modelPolicy.imageEdit')}</label>
            <label className="flex flex-col gap-1.5">{t('modelPolicy.maxOutputs')}<InputNumber min={1} max={10} precision={0} value={capability?.max_outputs} disabled={!capability} onChange={(value) => setCapability({ max_outputs: value ?? 1 })} /></label>
            <label className="flex flex-col gap-1.5">{t('modelPolicy.maxInputs')}<InputNumber min={0} max={32} precision={0} value={capability?.max_input_images} disabled={!capability} onChange={(value) => setCapability({ max_input_images: value ?? 0 })} /></label>
          </div>
          <details><summary className="cursor-pointer text-sm">{t('modelPolicy.advanced')}</summary>
            <p className="my-2 text-xs text-stone-500">{t('modelPolicy.advancedHelp')}</p>
            <Input.TextArea aria-label={t('modelPolicy.advanced')} rows={12} value={draft.capability} onChange={(e) => setDraft({ ...draft, capability: e.target.value })} />
          </details>
        </div>}
      </Modal>
    </main>
  )
}
