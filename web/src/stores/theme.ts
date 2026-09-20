// Theme (M2.5). Three states, not two: light, dark, and "whatever the system
// says" — which is the default, because a panel opened at night from a phone
// should not be the one bright rectangle in the room, and a person who never
// touches this setting should still get the right answer.
//
// Element Plus switches on the `dark` class on <html>; its dark variables are
// imported once in main.ts. Ours ride the same class, so there is one switch
// and not two that can disagree.
import { ref, watch } from 'vue'

export type ThemeChoice = 'system' | 'light' | 'dark'

const KEY = 'veilbridge.theme'

function stored(): ThemeChoice {
  const v = localStorage.getItem(KEY)
  return v === 'light' || v === 'dark' || v === 'system' ? v : 'system'
}

export const theme = ref<ThemeChoice>(stored())

const media = window.matchMedia?.('(prefers-color-scheme: dark)')

function resolved(choice: ThemeChoice): boolean {
  if (choice === 'dark') return true
  if (choice === 'light') return false
  return media?.matches ?? false
}

function apply() {
  document.documentElement.classList.toggle('dark', resolved(theme.value))
  // colorScheme makes the browser's own widgets (scrollbars, form controls)
  // follow, so the panel does not end up with a dark page and white scrollbars.
  document.documentElement.style.colorScheme = resolved(theme.value) ? 'dark' : 'light'
}

watch(theme, (v) => {
  localStorage.setItem(KEY, v)
  apply()
})

// Following the system means following it while the panel is open, not only at
// load: laptops switch at sunset.
media?.addEventListener('change', () => {
  if (theme.value === 'system') apply()
})

apply()

export function setTheme(v: ThemeChoice) {
  theme.value = v
}
