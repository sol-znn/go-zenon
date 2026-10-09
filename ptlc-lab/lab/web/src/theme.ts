// Dark or light, for both pages of the lab. Dark is where the design system
// lives and is what a first visit gets; the choice is kept in this browser.
// The attribute is first set by a few lines in each page's <head>, before
// anything is painted, so a page chosen light never shows a dark frame first.
import { ref } from 'vue'

export type Theme = 'dark' | 'light'
const KEY = 'ptlc.theme'

const current = (): Theme => (document.documentElement.getAttribute('data-theme') === 'light' ? 'light' : 'dark')
export const theme = ref<Theme>(current())

export function toggleTheme(): void {
  theme.value = theme.value === 'dark' ? 'light' : 'dark'
  document.documentElement.setAttribute('data-theme', theme.value)
  try {
    localStorage.setItem(KEY, theme.value)
  } catch {
    // Storage can be blocked; the theme still holds for this tab.
  }
}
