import { useCallback, useEffect, useState } from 'react'
import { App, Button, Popconfirm, Spin, Tag } from 'antd'
import { ArrowLeft, KeyRound, Link2, RefreshCw, Unlink } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'

import { createCanvasAPI, type CanvasCredentialCandidate } from '@sub2api/api/canvas-api'
import { initializeCanvasConfigStore } from '@sub2api/adapters/use-config-store'
import { useCanvasHost } from '@sub2api/host-context'

export default function CredentialBindingPage() {
  const { message, modal } = App.useApp()
  const { t } = useTranslation()
  const host = useCanvasHost()
  const navigate = useNavigate()
  const [items, setItems] = useState<CanvasCredentialCandidate[]>([])
  const [loading, setLoading] = useState(true)
  const [busyID, setBusyID] = useState<number>()
  const [error, setError] = useState('')
  const api = createCanvasAPI(host)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      setItems(await api.listCredentialCandidates())
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('canvas.apiKeys.bindingLoadFailed'))
    } finally {
      setLoading(false)
    }
  }, [host, t])

  useEffect(() => {
    void load()
  }, [load])

  const bind = (item: CanvasCredentialCandidate) => {
    modal.confirm({
      title: t('canvas.apiKeys.bindConfirmTitle', { name: item.name }),
      content: t('canvas.apiKeys.bindConfirmDescription'),
      okText: t('canvas.apiKeys.bind'),
      cancelText: t('common.cancel'),
      async onOk() {
        setBusyID(item.id)
        try {
          await api.bindCredential(item.id)
          await Promise.all([load(), initializeCanvasConfigStore(item.id)])
          message.success(t('canvas.apiKeys.bound'))
        } finally {
          setBusyID(undefined)
        }
      }
    })
  }

  const unbind = async (item: CanvasCredentialCandidate) => {
    if (!item.credential_id) return
    setBusyID(item.id)
    try {
      await api.deleteCredential(item.credential_id)
      await Promise.all([load(), initializeCanvasConfigStore()])
      message.success(t('canvas.apiKeys.unbound'))
    } finally {
      setBusyID(undefined)
    }
  }

  return (
    <main className="h-full min-h-[480px] overflow-auto bg-background text-stone-950 dark:text-stone-100">
      <div className="mx-auto w-full max-w-4xl px-5 py-8 sm:px-8">
        <header className="flex flex-wrap items-center justify-between gap-4 border-b border-stone-200 pb-5 dark:border-stone-800">
          <div className="flex min-w-0 items-center gap-3">
            <Button type="text" aria-label={t('canvas.apiKeys.back')} icon={<ArrowLeft className="size-4" />} onClick={() => navigate('/canvas')} />
            <div className="min-w-0">
              <h1 className="text-xl font-semibold">{t('canvas.apiKeys.bindingTitle')}</h1>
              <p className="mt-1 text-sm text-stone-500 dark:text-stone-400">{t('canvas.apiKeys.bindingDescription')}</p>
            </div>
          </div>
          <Button icon={<RefreshCw className="size-4" />} loading={loading} onClick={() => void load()}>{t('canvas.apiKeys.refresh')}</Button>
        </header>

        {loading ? <div className="grid min-h-72 place-items-center"><Spin /></div> : error ? (
          <section className="flex min-h-72 flex-col items-center justify-center text-center">
            <p className="max-w-lg break-words text-sm text-red-600 dark:text-red-300">{error}</p>
            <Button className="mt-5" icon={<RefreshCw className="size-4" />} onClick={() => void load()}>{t('canvas.apiKeys.retry')}</Button>
          </section>
        ) : items.length === 0 ? (
          <section className="flex min-h-72 flex-col items-center justify-center text-center">
            <KeyRound className="size-8 text-stone-400" />
            <h2 className="mt-4 text-lg font-medium">{t('canvas.apiKeys.noOfficialKeys')}</h2>
            <Button type="primary" className="mt-5" onClick={() => host.navigate('/keys?returnTo=%2Fstudio%2Fcredentials')}>{t('canvas.apiKeys.create')}</Button>
          </section>
        ) : (
          <div className="divide-y divide-stone-200 dark:divide-stone-800">
            {items.map((item) => {
              const available = item.status === 'active'
              return (
                <section key={item.id} className="flex flex-wrap items-center justify-between gap-4 py-5">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="max-w-full truncate font-medium">{item.name}</span>
                      <Tag color={available ? 'green' : 'default'}>{available ? t('canvas.apiKeys.active') : item.status}</Tag>
                      {item.bound ? <Tag color="blue">{t('canvas.apiKeys.boundStatus')}</Tag> : null}
                    </div>
                    <p className="mt-1 text-xs text-stone-500 dark:text-stone-400">
                      {item.key_hint ? `${item.key_hint} · ` : ''}{t('canvas.apiKeys.usage', { used: item.quota_used, total: item.quota })}
                    </p>
                  </div>
                  {item.bound ? (
                    <Popconfirm title={t('canvas.apiKeys.unbindConfirm')} okText={t('canvas.apiKeys.unbind')} cancelText={t('common.cancel')} onConfirm={() => void unbind(item)}>
                      <Button danger loading={busyID === item.id} icon={<Unlink className="size-4" />}>{t('canvas.apiKeys.unbind')}</Button>
                    </Popconfirm>
                  ) : (
                    <Button type="primary" disabled={!available} loading={busyID === item.id} icon={<Link2 className="size-4" />} onClick={() => bind(item)}>{t('canvas.apiKeys.bind')}</Button>
                  )}
                </section>
              )
            })}
          </div>
        )}
      </div>
    </main>
  )
}
