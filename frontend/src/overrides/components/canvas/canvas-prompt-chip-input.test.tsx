import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { CanvasPromptChipInput } from './canvas-prompt-chip-input'

;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

vi.mock('@/i18n', () => ({ default: { t: (key: string) => key } }))
vi.mock('@/stores/use-theme-store', () => ({
  useThemeStore: (selector: (state: { theme: 'light' }) => unknown) => selector({ theme: 'light' })
}))

const mountedRoots: Array<{ host: HTMLElement; root: Root }> = []

afterEach(async () => {
  for (const mounted of mountedRoots.splice(0)) {
    await act(async () => mounted.root.unmount())
    mounted.host.remove()
  }
  window.getSelection()?.removeAllRanges()
})

describe('CanvasPromptChipInput', () => {
  it('preserves the caret and input order when a controlled value echoes inside a Shadow Root', async () => {
    const changes: string[] = []
    const editor = await renderControlledInput(changes)

    editor.focus()
    const shadowRoot = editor.getRootNode() as ShadowRoot
    expect(document.activeElement).toBe(shadowRoot.host)
    expect(shadowRoot.activeElement).toBe(editor)

    const caret = document.createRange()
    const firstTextNode = await insertTextAtEnd(editor, 'a', caret)

    expect(firstTextNode).toBeInstanceOf(Text)
    expect(editor.firstChild).toBe(firstTextNode)

    await insertTextAtEnd(editor, 'b', caret)
    await insertTextAtEnd(editor, 'c', caret)

    expect(changes).toEqual(['a', 'ab', 'abc'])
    expect(editor.textContent).toBe('abc')
    expect(caret.startContainer).toBe(firstTextNode)
    expect(caret.startOffset).toBe(3)
  })

  it('waits for IME composition to finish and preserves the composed text node', async () => {
    const changes: string[] = []
    const editor = await renderControlledInput(changes)

    editor.focus()
    await act(async () => {
      editor.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }))
      editor.textContent = '\u4f60'
      editor.dispatchEvent(new InputEvent('input', { bubbles: true, data: '\u4f60', inputType: 'insertCompositionText' }))
    })

    const composedTextNode = editor.firstChild
    expect(changes).toEqual([])

    await act(async () => {
      editor.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true, data: '\u4f60' }))
    })

    expect(changes).toEqual(['\u4f60'])
    expect(editor.firstChild).toBe(composedTextNode)
    expect(editor.textContent).toBe('\u4f60')
  })
})

async function renderControlledInput(changes: string[]) {
  const host = document.createElement('div')
  const shadowRoot = host.attachShadow({ mode: 'open' })
  const container = document.createElement('div')
  shadowRoot.append(container)
  document.body.append(host)

  const root = createRoot(container)
  mountedRoots.push({ host, root })

  function Harness() {
    const [value, setValue] = useState('')
    return (
      <CanvasPromptChipInput
        value={value}
        references={[]}
        onChange={(next) => {
          changes.push(next)
          setValue(next)
        }}
      />
    )
  }

  await act(async () => root.render(<Harness />))
  const editor = shadowRoot.querySelector<HTMLElement>('[contenteditable="true"]')
  if (!editor) throw new Error('Prompt editor did not render')
  return editor
}

async function insertTextAtEnd(editor: HTMLElement, text: string, caret: Range) {
  const textNode = editor.lastChild instanceof Text ? editor.lastChild : document.createTextNode('')
  if (!textNode.isConnected) editor.append(textNode)
  textNode.appendData(text)
  caret.setStart(textNode, textNode.length)
  caret.collapse(true)

  await act(async () => {
    editor.dispatchEvent(new InputEvent('input', { bubbles: true, data: text, inputType: 'insertText' }))
  })
  return textNode
}
