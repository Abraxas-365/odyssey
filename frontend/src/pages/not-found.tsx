import { Link } from 'react-router-dom'
import { Compass } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/odyssey'

export function NotFoundPage() {
  return (
    <EmptyState icon={Compass} title="No encontramos esta página" action={<Button nativeButton={false} render={<Link to="/" />}>Volver al inicio</Button>}>
      Puede que la dirección esté mal escrita o que ya no tengas acceso.
    </EmptyState>
  )
}
