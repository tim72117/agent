import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import { CliAuthPage } from './CliAuthPage'
import { ToastProvider } from './Toast'
import './style.css'

// No router dependency for one extra path — a plain pathname check is
// enough for a single special-purpose page. The console SPA is mounted
// under the /app path prefix (see vite.config.ts's base), so this must
// match that prefix.
const page = window.location.pathname === '/app/cli-auth' ? <CliAuthPage /> : <App />

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ToastProvider>{page}</ToastProvider>
  </StrictMode>,
)
