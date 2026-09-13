import { StyleProvider } from '@ant-design/cssinjs'
import { ProConfigProvider } from '@ant-design/pro-components'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { App, ConfigProvider } from 'antd'
import enUS from 'antd/es/locale/en_US'
import zhCN from 'antd/es/locale/zh_CN'
import { lazy, Suspense, useEffect, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { useTranslation } from 'react-i18next'
import { Navigate, RouterProvider, createBrowserRouter, createMemoryRouter, type RouteObject } from 'react-router-dom'
import {
  CanvasHostProvider,
  createCanvasHostStore,
  useCanvasHost,
  type CanvasHandle,
  type CanvasHostContext
} from '@sub2api/host-context'
import { clearCanvasRuntimeHost, setCanvasRuntimeHost } from '@sub2api/runtime/host-runtime'
import { initializeCanvasProjectStore, resetCanvasProjectStore } from '@sub2api/adapters/use-canvas-store'
import { initializeCanvasAssetStore, resetCanvasAssetStore } from '@sub2api/adapters/use-asset-store'
import { resetCanvasAssetRuntime } from '@sub2api/adapters/asset-runtime'
import { initializeCanvasConfigStore, resetCanvasConfigStore, useConfigStore } from '@sub2api/adapters/use-config-store'
import CanvasProjectRuntime from '@sub2api/components/canvas-project-runtime'
import { CanvasAPIKeyEmptyState, CanvasConfigErrorState } from '@sub2api/components/canvas-api-key-empty-state'
import CredentialBindingPage from '@sub2api/components/credential-binding-page'
import CanvasProjectsPage from '@/pages/canvas'
import { CanvasRefreshShell } from '@/components/canvas/canvas-refresh-shell'
import upstreamI18n from '@/i18n'
import { getAntThemeConfig } from '@/lib/app-theme'
import { useThemeStore } from '@/stores/use-theme-store'
import 'antd/dist/reset.css'
import 'streamdown/styles.css'
import '@sub2api/styles/sub2api-canvas.css'
import '@/styles/globals.css'

const FocusedImageEditor = lazy(() => import('@sub2api/editor/focused-image-editor'))

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 30_000, retry: false, refetchOnWindowFocus: false }
  }
})

function CanvasProviders({ children, mountElement }: { children: ReactNode; mountElement: HTMLElement }) {
  const { i18n } = useTranslation()
  const theme = useThemeStore((state) => state.theme)
  const dark = theme === 'dark'
  const locale = i18n.resolvedLanguage?.toLowerCase().startsWith('zh') ? zhCN : enUS

  return (
    <ConfigProvider
      locale={locale}
      theme={getAntThemeConfig(dark)}
      getPopupContainer={() => mountElement}
    >
      <ProConfigProvider dark={dark}>
        <App
          className="sub2api-canvas-app h-full min-h-0"
          style={{ display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 }}
        >
          <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
        </App>
      </ProConfigProvider>
    </ConfigProvider>
  )
}

function canvasRoutes(): RouteObject[] {
  return [
    { path: '/', element: <Navigate to="/canvas?mode=recent" replace /> },
    { path: '/canvas', element: <CanvasRouteGuard><CanvasProjectsPage /></CanvasRouteGuard> },
    { path: '/canvas/:id', element: <CanvasRouteGuard><CanvasProjectRuntime /></CanvasRouteGuard> },
    {
      path: '/editor/:projectId/:nodeId',
      element: (
        <CanvasRouteGuard>
          <Suspense fallback={<div className="flex h-full items-center justify-center">Loading...</div>}>
            <FocusedImageEditor />
          </Suspense>
        </CanvasRouteGuard>
      )
    },
    { path: '/credentials', element: <CredentialBindingPage /> },
    { path: '*', element: <Navigate to="/canvas" replace /> }
  ]
}

function CanvasRuntime({ mountElement, router }: { mountElement: HTMLElement; router: ReturnType<typeof createMemoryRouter> }) {
  const host = useCanvasHost()
  const rootNode = mountElement.getRootNode()
  const styleContainer = rootNode instanceof ShadowRoot ? rootNode : mountElement

  useEffect(() => {
    const locale = host.locale.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en-US'
    void upstreamI18n.changeLanguage(locale)
    useThemeStore.getState().setTheme(host.theme)
    mountElement.classList.toggle('dark', host.theme === 'dark')
  }, [host.locale, host.theme, mountElement])

  return (
    <StyleProvider container={styleContainer}>
      <CanvasProviders mountElement={mountElement}>
        <RouterProvider router={router} />
      </CanvasProviders>
    </StyleProvider>
  )
}

function CanvasRouteGuard({ children }: { children: ReactNode }) {
  const config = useConfigStore((state) => state.canvasConfig)
  const loading = useConfigStore((state) => state.canvasConfigLoading)
  const error = useConfigStore((state) => state.canvasConfigError)

  if (!config && loading) return <CanvasRefreshShell />
  if (!config && error) return <CanvasConfigErrorState message={error} onRetry={() => void initializeCanvasConfigStore()} />
  if (config?.api_keys.length === 0) return <CanvasAPIKeyEmptyState />
  if (config && !config.api_keys.some((key) => key.available !== false)) return <CanvasAPIKeyEmptyState unavailable />
  return children
}

function initializeRuntime(context: CanvasHostContext): void {
  setCanvasRuntimeHost(context)
  resetCanvasProjectStore()
  resetCanvasAssetRuntime()
  resetCanvasAssetStore()
  resetCanvasConfigStore()
  void initializeCanvasProjectStore()
  void initializeCanvasAssetStore()
  void initializeCanvasConfigStore()
}

export function mountCanvas(element: HTMLElement, context: CanvasHostContext): CanvasHandle {
  initializeRuntime(context)
  const store = createCanvasHostStore(context)
  const router = createMemoryRouter(canvasRoutes(), { initialEntries: ['/canvas?mode=recent'] })
  const root = createRoot(element)
  element.classList.add('sub2api-canvas-mount')
  root.render(
    <CanvasHostProvider store={store}>
      <CanvasRuntime mountElement={element} router={router} />
    </CanvasHostProvider>
  )

  let mounted = true
  return {
    updateContext(next) {
      if (mounted) {
        setCanvasRuntimeHost(next)
        store.update(next)
      }
    },
    unmount() {
      if (!mounted) return
      mounted = false
      root.unmount()
      resetCanvasProjectStore()
      resetCanvasAssetRuntime()
      resetCanvasAssetStore()
      resetCanvasConfigStore()
      clearCanvasRuntimeHost(store.getSnapshot())
      element.replaceChildren()
      element.classList.remove('sub2api-canvas-mount')
    }
  }
}

export function mountStandaloneCanvas(element: HTMLElement, context: CanvasHostContext, basename = '/studio'): CanvasHandle {
  initializeRuntime(context)
  const store = createCanvasHostStore(context)
  const router = createBrowserRouter(canvasRoutes(), { basename })
  const root = createRoot(element)
  element.classList.add('sub2api-canvas-mount')
  root.render(
    <CanvasHostProvider store={store}>
      <CanvasRuntime mountElement={element} router={router} />
    </CanvasHostProvider>
  )
  let mounted = true
  return {
    updateContext(next) {
      if (mounted) {
        setCanvasRuntimeHost(next)
        store.update(next)
      }
    },
    unmount() {
      if (!mounted) return
      mounted = false
      root.unmount()
      resetCanvasProjectStore()
      resetCanvasAssetRuntime()
      resetCanvasAssetStore()
      resetCanvasConfigStore()
      clearCanvasRuntimeHost(store.getSnapshot())
      element.replaceChildren()
      element.classList.remove('sub2api-canvas-mount')
    }
  }
}

export type { CanvasHandle, CanvasHostContext }
