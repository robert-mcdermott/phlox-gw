// Theme switching + localStorage persistence.
// Ported from frontend/src/static/app.js. The active theme is applied as a
// `data-theme` attribute on <html>; the per-theme CSS variable blocks live in
// the stylesheet (Chunk 6 maps these to shadcn variables).

export interface Theme {
  id: string
  name: string
  /** [background, accent, foreground] preview swatch colors. */
  swatch: [string, string, string]
  dark: boolean
}

export const THEME_STORAGE_KEY = 'phlox-gw-theme'
export const DEFAULT_THEME = 'phlox-dark'

export const THEMES: Theme[] = [
  { id: 'phlox-dark', name: 'Phlox Dark', swatch: ['#160821', '#DF00FF', '#f0e6f7'], dark: true },
  { id: 'phlox-light', name: 'Phlox Light', swatch: ['#faf5fe', '#C200DE', '#2a1438'], dark: false },
  { id: 'fred-hutch', name: 'Fred Hutch', swatch: ['#1B365D', '#00ABC8', '#FFB500'], dark: false },
  { id: 'light', name: 'Light', swatch: ['#ffffff', '#0ea5b7', '#111827'], dark: false },
  { id: 'dark', name: 'Dark', swatch: ['#0f172a', '#22d3ee', '#e2e8f0'], dark: true },
  { id: 'hutch-night', name: 'Hutch Night', swatch: ['#10192b', '#AA4AC4', '#00ABC8'], dark: true },
  { id: 'sandstone', name: 'Sandstone', swatch: ['#faf5ee', '#b8860b', '#1B365D'], dark: false },
  { id: 'terminal', name: 'Terminal', swatch: ['#000000', '#00ff41', '#00ff41'], dark: true },
]

export function normalizedTheme(id: string | null | undefined): string {
  return THEMES.some((theme) => theme.id === id) ? (id as string) : DEFAULT_THEME
}

export function initialTheme(): string {
  try {
    return normalizedTheme(localStorage.getItem(THEME_STORAGE_KEY) || DEFAULT_THEME)
  } catch {
    return DEFAULT_THEME
  }
}

export function isDarkTheme(id: string): boolean {
  return THEMES.find((theme) => theme.id === id)?.dark ?? false
}

/**
 * Apply a theme to the document root and (optionally) persist it.
 * Returns the normalized theme id that was applied.
 */
export function applyTheme(id: string, persist = true): string {
  const theme = normalizedTheme(id)
  const root = document.documentElement
  root.setAttribute('data-theme', theme)
  // Keep shadcn's `.dark` class in sync so dark-mode variants resolve.
  root.classList.toggle('dark', isDarkTheme(theme))
  if (persist) {
    try {
      localStorage.setItem(THEME_STORAGE_KEY, theme)
    } catch {
      // ignore storage failures (private mode, etc.)
    }
  }
  return theme
}
