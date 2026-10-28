import { useEffect, useState, type ReactNode } from 'react'
import { Link, NavLink, useNavigate } from 'react-router-dom'
import { useTheme } from 'next-themes'
import { Bell, Bot, ChevronsUpDown, CircleHelp, LogOut, Monitor, Moon, Plus, Search, Sun, type LucideIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button, ButtonLink } from '@/components/ui/button'
import { Command, CommandDialog, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList, CommandShortcut } from '@/components/ui/command'
import { DropdownMenu, DropdownMenuContent, DropdownMenuGroup, DropdownMenuItem, DropdownMenuLabel, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Kbd } from '@/components/ui/kbd'
import { Switch } from '@/components/ui/switch'
import { cn } from '@/lib/utils'
import { IconBadge } from './icon-badge'
import { Avatar } from './identity'
import { Logo } from './logo'
import type { Tone } from './tone'

export interface NavTab { to: string; label: string; count?: number; end?: boolean }

/** Underlined top tabs (`.tabs`). Counts use the warning "count" badge. */
export function NavTabs({ tabs, label = 'Secciones' }: { tabs: NavTab[]; label?: string }) {
  return (
    <nav aria-label={label} className="flex gap-1 overflow-x-auto px-4">
      {tabs.map(t => (
        <NavLink
          key={t.to}
          to={t.to}
          end={t.end}
          className={({ isActive }) =>
            cn(
              'flex items-center gap-1.5 border-b-2 border-transparent px-2.5 pt-2.5 pb-3 text-sm whitespace-nowrap text-muted-foreground outline-none hover:text-foreground focus-visible:rounded-sm focus-visible:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50',
              isActive && 'border-foreground text-foreground',
            )
          }
        >
          {t.label}
          {!!t.count && <Badge variant="count" aria-label={`${t.count} pendientes`}>{t.count}</Badge>}
        </NavLink>
      ))}
    </nav>
  )
}

/** A header breadcrumb segment (`.crumb`), optionally a menu trigger. */
export function Crumb({ children, menu, to }: { children: ReactNode; menu?: ReactNode; to?: string }) {
  const cls = 'flex items-center gap-2 rounded-sm px-2 py-1 font-medium outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50'
  if (menu) {
    return (
      <DropdownMenu>
        <DropdownMenuTrigger className={cn(cls, 'cursor-pointer')}>
          {children}
          <ChevronsUpDown className="size-3.5 text-muted-foreground" aria-hidden />
        </DropdownMenuTrigger>
        <DropdownMenuContent className="min-w-[280px]">{menu}</DropdownMenuContent>
      </DropdownMenu>
    )
  }
  return to ? <Link to={to} className={cls}>{children}</Link> : <span className={cls}>{children}</span>
}

const Slash = () => <span aria-hidden className="text-[22px] font-light text-border">/</span>

export interface Notification { id: string; tone: Tone; icon: LucideIcon; title: ReactNode; when: string; unread?: boolean; onSelect?: () => void }

/** The bell: the same for every persona (UX v3 §10). */
export function NotificationsMenu({ items, onSeeAll }: { items: Notification[]; onSeeAll?: () => void }) {
  const unread = items.filter(i => i.unread).length
  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="ghost" size="icon" className="relative" aria-label={unread ? `Notificaciones, ${unread} sin leer` : 'Notificaciones'} />}>
        <Bell />
        {unread > 0 && <span aria-hidden className="absolute top-1.5 right-1.5 size-2 rounded-full border-2 border-background bg-warning" />}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-[360px]">
        <DropdownMenuGroup>
          <DropdownMenuLabel>Notificaciones</DropdownMenuLabel>
          {items.length === 0 && <div className="px-2 py-6 text-center text-[13px] text-muted-foreground">Nada nuevo por ahora.</div>}
          {items.map(n => (
            <DropdownMenuItem key={n.id} onClick={n.onSelect} className="items-start gap-2.5 p-2">
              <IconBadge icon={n.icon} tone={n.tone} size="sm" />
              <span className="min-w-0 flex-1">
                <span className={cn('block text-[13px]', n.unread && 'font-semibold')}>{n.title}</span>
                <span className="block text-xs text-muted-foreground">{n.when}</span>
              </span>
            </DropdownMenuItem>
          ))}
        </DropdownMenuGroup>
        {onSeeAll && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={onSeeAll}>Ver toda la actividad</DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** Avatar menu: identity, "Mostrar detalles técnicos", theme, sign out. */
export function UserMenu({ name, email, technical, onTechnicalChange, onSwitchOrganization, onLogout, children }: { name: string; email?: string; technical: boolean; onTechnicalChange: (v: boolean) => void; onSwitchOrganization?: () => void; onLogout: () => void; children?: ReactNode }) {
  const { theme, setTheme } = useTheme()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger className="cursor-pointer rounded-full p-0.5 outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
        {/* Named by text, not aria-label: the visible initials must not contradict the name (WCAG 2.5.3). */}
        <span className="sr-only">Cuenta de {name}</span>
        <Avatar name={name} size="lg" decorative />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-[280px]">
        <DropdownMenuGroup>
          <DropdownMenuLabel className="flex flex-col">
            <span className="font-semibold text-foreground">{name}</span>
            {email && <span className="text-xs font-normal">{email}</span>}
          </DropdownMenuLabel>
        </DropdownMenuGroup>
        <DropdownMenuSeparator />
        <DropdownMenuItem closeOnClick={false} onClick={() => onTechnicalChange(!technical)} className="items-start gap-3 p-2">
          <span className="min-w-0 flex-1">
            <span className="block">Mostrar detalles técnicos</span>
            <span className="block text-xs text-muted-foreground">Commit, rama, registros y variables</span>
          </span>
          <Switch checked={technical} tabIndex={-1} aria-hidden className="pointer-events-none mt-0.5" />
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuGroup>
          <DropdownMenuLabel>Tema</DropdownMenuLabel>
          <DropdownMenuRadioGroup value={theme ?? 'dark'} onValueChange={v => setTheme(String(v))}>
            <DropdownMenuRadioItem value="dark"><Moon />Oscuro</DropdownMenuRadioItem>
            <DropdownMenuRadioItem value="light"><Sun />Claro</DropdownMenuRadioItem>
            <DropdownMenuRadioItem value="system"><Monitor />Como el sistema</DropdownMenuRadioItem>
          </DropdownMenuRadioGroup>
        </DropdownMenuGroup>
        {children}
        <DropdownMenuSeparator />
        {onSwitchOrganization && <DropdownMenuItem onClick={onSwitchOrganization}>Cambiar de organización</DropdownMenuItem>}
        <DropdownMenuItem onClick={onLogout}><LogOut />Cerrar sesión</DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

export interface CommandEntry { id: string; label: string; group: string; icon?: LucideIcon; shortcut?: string; onSelect: () => void }

/** ⌘K "Buscar o pedir algo…" palette. */
export function CommandPalette({ entries }: { entries: CommandEntry[] }) {
  const [open, setOpen] = useState(false)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() === 'k' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); setOpen(o => !o) }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])
  const groups = [...new Set(entries.map(e => e.group))]
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="hidden h-[34px] w-[300px] cursor-pointer items-center gap-2 rounded-md border px-3 text-[13px] text-muted-foreground outline-none hover:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring/50 md:flex"
      >
        <Search className="size-4" aria-hidden />
        <span className="flex-1 text-left">Buscar o pedir algo…</span>
        <Kbd>⌘K</Kbd>
      </button>
      <Button variant="ghost" size="icon" className="md:hidden" aria-label="Buscar" onClick={() => setOpen(true)}>
        <Search />
      </Button>
      <CommandDialog open={open} onOpenChange={setOpen} title="Buscar" description="Busca apps o acciones">
        <Command>
          <CommandInput placeholder="Buscar apps, datos o acciones…" />
          <CommandList>
            <CommandEmpty>No encontramos nada con ese nombre.</CommandEmpty>
            {groups.map(g => (
              <CommandGroup key={g} heading={g}>
                {entries.filter(e => e.group === g).map(e => (
                  <CommandItem key={e.id} value={`${e.label} ${e.id}`} onSelect={() => { setOpen(false); e.onSelect() }}>
                    {e.icon && <e.icon />}
                    {e.label}
                    {e.shortcut && <CommandShortcut>{e.shortcut}</CommandShortcut>}
                  </CommandItem>
                ))}
              </CommandGroup>
            ))}
          </CommandList>
        </Command>
      </CommandDialog>
    </>
  )
}

/**
 * The console chrome (mockup header): logo / team / app crumbs, search, bell,
 * help, avatar; tabs underneath; a centred 1160px main column.
 */
export function AppShell({ team, project, tabs, search, notifications, user, children, wide }: { team: ReactNode; project?: ReactNode; tabs?: NavTab[]; search?: ReactNode; notifications?: ReactNode; user: ReactNode; children: ReactNode; wide?: boolean }) {
  return (
    <div className="min-h-dvh">
      <a href="#main" className="sr-only z-50 rounded-md bg-strong px-3 py-2 text-strong-foreground focus:not-sr-only focus:fixed focus:top-2 focus:left-2">
        Saltar al contenido
      </a>
      <header className="sticky top-0 z-10 border-b bg-background">
        <div className="flex h-14 items-center gap-2.5 px-6">
          <Link to="/" aria-label="Inicio" className="rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
            <Logo />
          </Link>
          <Slash />
          {team}
          {project && (
            <>
              <Slash />
              {project}
            </>
          )}
          <div className="flex-1" />
          {search}
          {notifications}
          <ButtonLink variant="ghost" size="icon" aria-label="Ayuda" href="https://github.com/Abraxas-365/odyssey" target="_blank" rel="noreferrer">
            <CircleHelp />
          </ButtonLink>
          {user}
        </div>
        {tabs && tabs.length > 0 && <NavTabs tabs={tabs} />}
      </header>
      <main id="main" className={cn('mx-auto flex w-full flex-col gap-6 px-6 pt-8 pb-12', wide ? 'max-w-[1400px]' : 'max-w-[1160px]')}>
        {children}
      </main>
    </div>
  )
}

/** Quick actions reused by the palette and the home header. */
export function useCoreCommands(): CommandEntry[] {
  const navigate = useNavigate()
  return [
    { id: 'new-app', label: 'Crear app', group: 'Acciones', icon: Plus, onSelect: () => navigate('/?crear=1') },
    { id: 'agent', label: 'Conectar mi asistente', group: 'Acciones', icon: Bot, onSelect: () => navigate('/asistente') },
  ]
}
