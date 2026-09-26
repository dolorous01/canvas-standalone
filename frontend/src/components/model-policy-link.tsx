import { Button } from 'antd'
import { SlidersHorizontal } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useCanvasHost } from '@sub2api/host-context'

export function ModelPolicyLink() {
  const host = useCanvasHost()
  const { t } = useTranslation()
  if (host.routeMode !== 'admin') return null
  const base = host.apiBaseURL.startsWith('/canvas-api-next/') ? '/studio-next' : '/studio'
  return (
    <Button aria-label={t('modelPolicy.title')} title={t('modelPolicy.title')} icon={<SlidersHorizontal className="size-4" />} onClick={() => host.navigate(`${base}/admin/models`)}>
      <span className="hidden sm:inline">{t('modelPolicy.title')}</span>
    </Button>
  )
}
