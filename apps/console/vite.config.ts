import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Builds to the default dist/ (gitignored), symmetric with apps/landing.
// The Dockerfile copies dist/. into backend/cmd/server/web/console/ (the
// //go:embed target) at image-build time. Locally, `go run ./cmd/server`
// only ever serves the checked-in placeholder under web/console/ — for
// real console development run this app's own Vite dev server (`npm run
// dev`, :5173) instead of trying to embed a local build.
export default defineConfig({
  base: '/app/',
  plugins: [react()],
})
