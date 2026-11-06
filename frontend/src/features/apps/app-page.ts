import { useOutletContext } from 'react-router-dom'
import type { UseQueryResult } from '@tanstack/react-query'
import type { useDeployments } from '@/lib/releases'
import type { CurrentRelease, Deployment, Environment, Project } from '@/lib/types'

/**
 * What every tab of an app (Resumen · Historial · Compartir · Configuración)
 * reads, loaded once by the app's layout (pages/app-layout.tsx) and handed
 * down through the router's outlet.
 */
export interface AppPage {
  project: Project
  archived: boolean
  /** Can edit the app (name, access, variables…): not archived and projects:write. */
  canWrite: boolean
  /** Can see deployments (versions, current, settings). */
  canRead: boolean
  /** Can publish a version: not archived and deployments:create. */
  canPublish: boolean
  technical: boolean
  /** Phase 1 publishes to production only; staging and previews come with GitHub. */
  production: Environment | undefined
  current: UseQueryResult<CurrentRelease>
  /** The current release when one is online. */
  live: CurrentRelease | undefined
  publishing: boolean
  deployments: ReturnType<typeof useDeployments>
  history: Deployment[]
  /** The last attempt someone made, when it failed (none while publishing). */
  problem: Deployment | undefined
  /** A GitHub repository is connected (then the app publishes itself from it). */
  connected: boolean
  /** Opens "Subir una carpeta". */
  uploadFolder: () => void
}

export const useAppPage = () => useOutletContext<AppPage>()
