import { lazy, Suspense } from 'react'
import { BrowserRouter, Route, Routes } from 'react-router-dom'
import { Loading } from '@/components/odyssey'
import { AuthGate } from '@/lib/auth'
import { AppOverviewPage } from '@/pages/app-overview'
import { AssistantPage } from '@/pages/assistant'
import { AuthCallbackPage } from '@/pages/auth-callback'
import { ClustersPage } from '@/pages/clusters'
import { ConsoleLayout } from '@/pages/console-layout'
import { GitHubSettingsPage } from '@/pages/github-settings'
import { HomePage } from '@/pages/home'
import { NotFoundPage } from '@/pages/not-found'
import { PolicyRulesPage } from '@/pages/policy-rules'

// The showcase is dev/reference only: keep it out of the main bundle.
const DesignSystemPage = lazy(() => import('@/pages/design-system').then(m => ({ default: m.DesignSystemPage })))

export function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/auth/callback" element={<AuthCallbackPage />} />
        <Route path="/design" element={<Suspense fallback={<Loading />}><DesignSystemPage /></Suspense>} />
        <Route element={<AuthGate><ConsoleLayout /></AuthGate>}>
          <Route index element={<HomePage />} />
          <Route path="asistente" element={<AssistantPage />} />
          <Route path="apps/:id" element={<AppOverviewPage />} />
          <Route path="infraestructura/clusteres" element={<ClustersPage />} />
          <Route path="configuracion/github" element={<GitHubSettingsPage />} />
          <Route path="configuracion/reglas" element={<PolicyRulesPage />} />
          <Route path="*" element={<NotFoundPage />} />
        </Route>
      </Routes>
    </BrowserRouter>
  )
}
