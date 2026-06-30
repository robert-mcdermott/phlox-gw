import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'
import { applyTheme, initialTheme } from '@/lib/theme'

// Apply the persisted theme before first paint (without re-persisting).
applyTheme(initialTheme(), false)

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
