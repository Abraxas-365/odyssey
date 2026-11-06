import { lazy, Suspense } from 'react'
import { BrowserRouter, Route, Routes } from 'react-router-dom'
import { Loading } from '@/components/odyssey'
import { AuthGate } from '@/lib/auth'
import { AppHistoryPage } from '@/pages/app-history'
import { AppLayout } from '@/pages/app-layout'
import { AppOverviewPage } from '@/pages/app-overview'
import { AppSettingsPage } from '@/pages/app-settings'
import { AppSharePage } from '@/pages/app-share'
import { AssistantPage } from '@/pages/assistant'
import { AuthCallbackPage } from '@/pages/auth-callback'
import { BandejaPage } from '@/pages/bandeja'
import { ClustersPage } from '@/pages/clusters'
import { ConsoleLayout } from '@/pages/console-layout'
import { GitHubSettingsPage } from '@/pages/github-settings'
import { HomePage } from '@/pages/home'
import { InventoryPage } from '@/pages/inventory'
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
          <Route path="bandeja" element={<BandejaPage />} />
          <Route path="inventario" element={<InventoryPage />} />
          <Route path="apps/:id" element={<AppLayout />}>
            <Route index element={<AppOverviewPage />} />
            <Route path="historial" element={<AppHistoryPage />} />
            <Route path="compartir" element={<AppSharePage />} />
            <Route path="configuracion" element={<AppSettingsPage />} />
          </Route>
          <Route path="infraestructura/clusteres" element={<ClustersPage />} />
          <Route path="configuracion/github" element={<GitHubSettingsPage />} />
          <Route path="configuracion/reglas" element={<PolicyRulesPage />} />
          <Route path="*" element={<NotFoundPage />} />
        </Route>
      </Routes>
    </BrowserRouter>
  )
}
