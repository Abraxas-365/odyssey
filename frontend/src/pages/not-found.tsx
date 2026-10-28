import { Link } from 'react-router-dom'
import { Compass } from 'lucide-react'
import { ButtonLink } from '@/components/ui/button'
import { EmptyState } from '@/components/odyssey'

export function NotFoundPage() {
  return (
    <EmptyState level={1} icon={Compass} title="No encontramos esta página" action={<ButtonLink render={<Link to="/" />}>Volver al inicio</ButtonLink>}>
      Puede que la dirección esté mal escrita o que ya no tengas acceso.
    </EmptyState>
  )
}
