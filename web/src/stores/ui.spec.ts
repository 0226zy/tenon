import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useUiStore } from './ui'

beforeEach(() => {
  localStorage.clear()
  setActivePinia(createPinia())
})

describe('ui workspace collapse', () => {
  it('toggles, persists and re-expands from the code/preview actions', () => {
    const ui = useUiStore()
    expect(ui.workspaceCollapsed).toBe(false)

    ui.toggleWorkspace()
    expect(ui.workspaceCollapsed).toBe(true)
    expect(localStorage.getItem('proto.workspace.collapsed')).toBe('1')

    ui.showWorkspace('preview')
    expect(ui.workspaceCollapsed).toBe(false)
    expect(ui.workspaceTab).toBe('preview')
    expect(localStorage.getItem('proto.workspace.collapsed')).toBe('0')
  })
})
