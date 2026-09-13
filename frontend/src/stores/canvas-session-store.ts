import { create } from 'zustand'
import { persist } from 'zustand/middleware'

interface CanvasSessionState {
  apiKeyID?: number
  model?: string
  setSelection(apiKeyID?: number, model?: string): void
  clear(): void
}

export const useCanvasSessionStore = create<CanvasSessionState>()(
  persist(
    (set) => ({
      setSelection: (apiKeyID, model) => set({ apiKeyID, model }),
      clear: () => set({ apiKeyID: undefined, model: undefined })
    }),
    {
      name: 'sub2api:canvas-session',
      partialize: ({ apiKeyID, model }) => ({ apiKeyID, model })
    }
  )
)
