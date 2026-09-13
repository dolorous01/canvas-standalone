import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import react from '@vitejs/plugin-react'
import { defineConfig, type Plugin } from 'vite'

function releaseNotices(): Plugin {
  return {
    name: 'canvas-release-notices',
    generateBundle() {
      for (const fileName of ['LICENSE', 'LICENSE.upstream', 'NOTICE', 'UPSTREAM.md']) {
        this.emitFile({ type: 'asset', fileName, source: readFileSync(resolve(__dirname, fileName)) })
      }
    }
  }
}

function browserRuntimeGuard(): Plugin {
  return {
    name: 'canvas-browser-runtime-guard',
    generateBundle(_options, bundle) {
      for (const output of Object.values(bundle)) {
        if (output.type === 'chunk' && /\bprocess\.env\b/.test(output.code)) {
          this.error(`canvas browser bundle contains an unresolved process.env reference: ${output.fileName}`)
        }
      }
    }
  }
}

export default defineConfig(({ mode }) => {
  const sourceURL = process.env.VITE_CANVAS_SOURCE_URL || ''
  const buildID = (process.env.VITE_CANVAS_BUILD_ID || 'dev').trim()
  if (mode === 'production' && !sourceURL.startsWith('https://')) {
    throw new Error('VITE_CANVAS_SOURCE_URL must be an HTTPS corresponding-source URL')
  }
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(buildID)) {
    throw new Error('VITE_CANVAS_BUILD_ID must be a safe path segment')
  }
  return {
    base: `/canvas-static/${buildID}/`,
    plugins: [react(), browserRuntimeGuard(), releaseNotices()],
    define: {
      __CANVAS_SOURCE_URL__: JSON.stringify(sourceURL),
      __APP_VERSION__: JSON.stringify('0.16.0-sub2api.1'),
      __APP_RELEASES__: JSON.stringify([]),
      'process.env': JSON.stringify({ NODE_ENV: mode === 'production' ? 'production' : 'development' })
    },
    resolve: {
      alias: [
        {
          find: /^@\/pages\/canvas\/project$/,
          replacement: resolve(__dirname, 'src/overrides/pages/canvas/project.tsx')
        },
        {
          find: /^@\/pages\/assets$/,
          replacement: resolve(__dirname, 'src/overrides/pages/assets/index.tsx')
        },
        {
          find: /^@\/components\/canvas\/asset-picker-modal$/,
          replacement: resolve(__dirname, 'src/overrides/components/canvas/asset-picker-modal.tsx')
        },
        {
          find: /^@\/components\/canvas\/canvas-side-panel$/,
          replacement: resolve(__dirname, 'src/overrides/components/canvas/canvas-side-panel.tsx')
        },
        {
          find: /^@\/components\/canvas\/canvas-top-bar$/,
          replacement: resolve(__dirname, 'src/overrides/components/canvas/canvas-top-bar.tsx')
        },
        {
          find: /^@\/components\/canvas\/canvas-prompt-chip-input$/,
          replacement: resolve(__dirname, 'src/overrides/components/canvas/canvas-prompt-chip-input.tsx')
        },
        {
          find: /^\.\/canvas-prompt-chip-input$/,
          replacement: resolve(__dirname, 'src/overrides/components/canvas/canvas-prompt-chip-input.tsx')
        },
        {
          find: /^@\/stores\/use-canvas-side-panel-store$/,
          replacement: resolve(__dirname, 'src/overrides/stores/use-canvas-side-panel-store.ts')
        },
        {
          find: /^@\/i18n\/locales\/en-US$/,
          replacement: resolve(__dirname, 'src/overrides/i18n/locales/en-US.ts')
        },
        {
          find: /^@\/i18n\/locales\/zh-CN$/,
          replacement: resolve(__dirname, 'src/overrides/i18n/locales/zh-CN.ts')
        },
        {
          find: '@/stores/canvas/use-canvas-store',
          replacement: resolve(__dirname, 'src/adapters/use-canvas-store.ts')
        },
        {
          find: '@/stores/use-config-store',
          replacement: resolve(__dirname, 'src/adapters/use-config-store.ts')
        },
        {
          find: '@/stores/use-asset-store',
          replacement: resolve(__dirname, 'src/adapters/use-asset-store.ts')
        },
        {
          find: '@/services/image-storage',
          replacement: resolve(__dirname, 'src/adapters/image-storage.ts')
        },
        {
          find: '@/services/api/image',
          replacement: resolve(__dirname, 'src/adapters/image-api.ts')
        },
        {
          find: '@/services/api/video',
          replacement: resolve(__dirname, 'src/adapters/video-api.ts')
        },
        {
          find: '@/services/api/audio',
          replacement: resolve(__dirname, 'src/adapters/audio-api.ts')
        },
        {
          find: '@/lib/canvas/plugin-loader',
          replacement: resolve(__dirname, 'src/adapters/plugin-loader.ts')
        },
        {
          find: '@/lib/canvas/plugin-registry',
          replacement: resolve(__dirname, 'src/adapters/plugin-registry.ts')
        },
        {
          find: '@/lib/canvas/canvas-generation-helpers',
          replacement: resolve(__dirname, 'src/adapters/canvas-generation-helpers.ts')
        },
        {
          find: '@/services/file-storage',
          replacement: resolve(__dirname, 'src/adapters/file-storage.ts')
        },
        { find: '@sub2api', replacement: resolve(__dirname, 'src') },
        { find: '@', replacement: resolve(__dirname, 'src/upstream') }
      ]
    },
    build: {
      outDir: '../dist',
      emptyOutDir: true,
      manifest: 'manifest.json',
      rollupOptions: {
        output: {
          entryFileNames: 'assets/canvas-[hash].js',
          chunkFileNames: 'assets/chunk-[hash].js',
          assetFileNames: 'assets/[name]-[hash][extname]'
        }
      }
    }
  }
})
