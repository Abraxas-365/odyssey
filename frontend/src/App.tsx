import { lazy, Suspense } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { Loading } from '@/components/odyssey'
import { AuthGate } from '@/lib/auth'
import { AdminIndex, AdminLayout } from '@/pages/admin-layout'
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
import { RegistroPage } from '@/pages/registro'

// The showcase is dev/reference only: keep it out of the main bundle.
const DesignSystemPage = lazy(() => import('@/pages/design-system').then(m => ({ default: m.DesignSystemPage })))

/** An old address (bookmarks, links already sent) keeps working, query and all. */
function Moved({ to }: { to: string }) {
  const { search, hash } = useLocation()
  return <Navigate to={to + search + hash} replace />
}

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
          <Route path="registro" element={<RegistroPage />} />
          <Route path="apps/:id" element={<AppLayout />}>
            <Route index element={<AppOverviewPage />} />
            <Route path="historial" element={<AppHistoryPage />} />
            <Route path="compartir" element={<AppSharePage />} />
            <Route path="configuracion" element={<AppSettingsPage />} />
          </Route>
          <Route path="administracion" element={<AdminLayout />}>
            <Route index element={<AdminIndex />} />
            <Route path="clusteres" element={<ClustersPage />} />
            <Route path="github" element={<GitHubSettingsPage />} />
            <Route path="reglas" element={<PolicyRulesPage />} />
          </Route>
          <Route path="infraestructura/clusteres" element={<Moved to="/administracion/clusteres" />} />
          <Route path="configuracion/github" element={<Moved to="/administracion/github" />} />
          <Route path="configuracion/reglas" element={<Moved to="/administracion/reglas" />} />
          <Route path="*" element={<NotFoundPage />} />
        </Route>
      </Routes>
    </BrowserRouter>
  )
}
