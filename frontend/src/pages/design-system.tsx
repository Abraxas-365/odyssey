import { useState, type ReactNode } from 'react'
import { AlertTriangle, AppWindow, Bot, Clock, Database, FileText, FlaskConical, Hammer, Plus, Rocket, ShieldCheck, Upload } from 'lucide-react'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Kbd } from '@/components/ui/kbd'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  AppCard,
  AppStatus,
  appStatuses,
  AppThumbnail,
  ApprovalDetail,
  ApprovalRow,
  AskBox,
  AssistantBy,
  AssistantPrompt,
  AttentionItem,
  AttentionList,
  Avatar,
  Callout,
  ChipGroup,
  CodeLine,
  CreateAppTile,
  EmptyState,
  ErrorNotice,
  GitLine,
  Hero,
  IconBadge,
  KeyValue,
  ListSkeleton,
  Loading,
  LogViewer,
  OptionCards,
  PageHeader,
  Panel,
  PanelBody,
  PanelFooter,
  PanelHeader,
  PanelList,
  PanelRow,
  PersonRow,
  ProgressSteps,
  RequestTracker,
  requestSteps,
  ResourceIcons,
  ResourceOption,
  ResourceRow,
  RuleRow,
  SectionHeader,
  Stat,
  StepNumber,
  SubNav,
  SwitchField,
  TechnicalDetails,
  Term,
  UsageMeter,
  VersionRow,
  Wordmark,
  type ApprovalRequestData,
  type AppStatusKind,
  type ResourceUse,
  type Tone,
} from '@/components/odyssey'
import { ApiError } from '@/lib/api'

/**
 * /design — living reference of the Odyssey design system. Sample data comes
 * from the v3 mockup (Ana / Ventas / Acme). Unauthenticated, no API calls.
 */
export function DesignSystemPage() {
  const [technical, setTechnical] = useState(false)
  const [section, setSection] = useState('fundamentos')
  const sections = [
    ['fundamentos', 'Fundamentos'],
    ['acciones', 'Acciones y formularios'],
    ['estado', 'Estado y feedback'],
    ['apps', 'Apps y versiones'],
    ['recursos', 'Recursos y uso'],
    ['aprobaciones', 'Aprobaciones y reglas'],
    ['tecnico', 'Detalles técnicos'],
  ] as const

  return (
    <div className="min-h-dvh">
      <header className="sticky top-0 z-10 flex h-14 items-center gap-3 border-b bg-background px-6">
        <Wordmark />
        <Badge>Design system</Badge>
        <div className="flex-1" />
        <SwitchField title="Detalles técnicos" checked={technical} onCheckedChange={setTechnical} />
      </header>
      <div className="mx-auto grid max-w-[1160px] gap-8 px-6 pt-8 pb-16 md:grid-cols-[200px_1fr]">
        <aside className="md:sticky md:top-20 md:self-start">
          <SubNav label="Secciones" value={section} onChange={v => { setSection(v); document.getElementById(v)?.scrollIntoView({ behavior: 'smooth' }) }} items={sections.map(([value, label]) => ({ value, label }))} />
        </aside>
        <main className="flex min-w-0 flex-col gap-12">
          <Foundations />
          <Actions />
          <StateAndFeedback />
          <AppsAndVersions technical={technical} />
          <Resources technical={technical} />
          <Approvals />
          <TechnicalSection technical={technical} />
        </main>
      </div>
    </div>
  )
}

function Section({ id, title, description, children }: { id: string; title: string; description?: string; children: ReactNode }) {
  return (
    <section id={id} aria-labelledby={`${id}-title`} className="flex scroll-mt-20 flex-col gap-5">
      <div>
        <h2 id={`${id}-title`} className="font-heading text-title font-bold">{title}</h2>
        {description && <p className="mt-1 max-w-[680px] text-muted-foreground">{description}</p>}
      </div>
      {children}
    </section>
  )
}

function Sample({ title, children, className }: { title: string; children: ReactNode; className?: string }) {
  return (
    <div className="flex flex-col gap-2">
      <div className="text-xs text-muted-foreground">{title}</div>
      <div className={className}>{children}</div>
    </div>
  )
}

const swatches: [string, string][] = [
  ['background', 'bg-background'], ['card', 'bg-card'], ['muted', 'bg-muted'], ['inset', 'bg-inset'], ['border', 'bg-border'],
  ['foreground', 'bg-foreground'], ['muted-foreground', 'bg-muted-foreground'], ['primary', 'bg-primary'],
  ['success', 'bg-success'], ['warning', 'bg-warning'], ['destructive', 'bg-destructive'], ['info', 'bg-info'], ['ai', 'bg-ai'],
]

function Foundations() {
  return (
    <Section id="fundamentos" title="Fundamentos" description="Paleta IAMKit, oscuro por defecto (también hay tema claro). Inter para el texto y JetBrains Mono para títulos, números y código.">
      <div className="grid grid-cols-3 gap-3 sm:grid-cols-5 lg:grid-cols-7">
        {swatches.map(([name, cls]) => (
          <div key={name} className="flex flex-col gap-1.5">
            <div className={`h-14 rounded-lg border ${cls}`} />
            <code className="text-2xs text-muted-foreground">{name}</code>
          </div>
        ))}
      </div>
      <Panel>
        <PanelBody className="gap-4">
          <div className="font-heading text-display font-bold">Display 34 · ¿Qué quieres crear?</div>
          <div className="font-heading text-title font-bold">Título 28 · Visitas a clientes</div>
          <div className="font-mono text-stat font-semibold">26 · $48.20</div>
          <div className="font-heading text-section font-semibold">Sección 17 · Necesita tu atención</div>
          <div className="text-body">Cuerpo 14 · Tu app sigue funcionando. Nadie se vio afectado.</div>
          <div className="text-[13px] text-muted-foreground">Secundario 13 · Hace 2 horas · por Ana</div>
          <div className="text-xs text-muted-foreground">Pequeño 12 · 340 MB de 10 GB</div>
          <div className="font-mono text-[13px]">Mono 13 · main · a1b2c3d</div>
        </PanelBody>
      </Panel>
      <Sample title="Tonos (siempre con significado, nunca decorativos)" className="flex flex-wrap gap-3">
        {(['ok', 'warn', 'bad', 'info', 'ai', 'neutral'] as Tone[]).map(t => <IconBadge key={t} icon={t === 'ai' ? Bot : t === 'bad' ? AlertTriangle : ShieldCheck} tone={t} label={t} />)}
      </Sample>
    </Section>
  )
}

function Actions() {
  const [chip, setChip] = useState('all')
  const [access, setAccess] = useState('team')
  const [ask, setAsk] = useState('')
  return (
    <Section id="acciones" title="Acciones y formularios" description="Una acción principal por pantalla (botón invertido). Lo destructivo siempre pide confirmación.">
      <Sample title="Botones" className="flex flex-wrap items-center gap-2">
        <Button><Rocket />Publicar</Button>
        <Button variant="outline">Ver versiones</Button>
        <Button variant="secondary">Secundario</Button>
        <Button variant="ghost">Cancelar</Button>
        <Button variant="destructive">Archivar</Button>
        <Button variant="link">Ver detalles</Button>
        <Button size="sm" variant="outline">Pequeño</Button>
        <Button size="lg">Grande</Button>
        <Button disabled>Deshabilitado</Button>
      </Sample>
      <Sample title="Chips de filtro" ><ChipGroup label="Filtro" value={chip} onChange={setChip} options={[{ value: 'all', label: 'Todas' }, { value: 'mine', label: 'Mías' }, { value: 'shared', label: 'Compartidas conmigo' }]} /></Sample>
      <div className="grid gap-6 md:grid-cols-2">
        <Sample title="Opciones (Acceso general)">
          <OptionCards label="Acceso general" value={access} onChange={setAccess} options={[
            { value: 'team', title: 'Solo tu equipo', description: 'Ventas · 12 personas' },
            { value: 'org', title: 'Todo Acme', description: 'Necesita la aprobación de TI' },
            { value: 'link', title: 'Cualquiera con el enlace', description: 'Necesita aprobación de TI y Seguridad', disabled: true },
          ]} />
        </Sample>
        <Sample title="Campos" className="flex flex-col gap-3">
          <Input placeholder="Nombre de la app" />
          <Input aria-invalid placeholder="Con error" />
          <Select defaultValue="viewer" items={{ viewer: 'Lector', approver: 'Aprobador', admin: 'Admin' }}>
            <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="viewer">Lector</SelectItem>
              <SelectItem value="approver">Aprobador</SelectItem>
              <SelectItem value="admin">Admin</SelectItem>
            </SelectContent>
          </Select>
          <SwitchField title="Mostrar detalles técnicos" description="Commit, rama, registros y variables" checked={false} onCheckedChange={() => {}} />
          <div className="text-[13px]">Atajo: <Kbd>⌘K</Kbd> · <Term help="La dirección web donde la gente abre tu app.">dominio</Term> · <Term icon help="Una copia de los datos de la empresa que se actualiza cada noche.">Datos de la empresa</Term></div>
        </Sample>
      </div>
      <Sample title="Cuadro “¿Qué quieres crear?”">
        <AskBox value={ask} onChange={setAsk} footer={<>
          <ChipGroup label="Ejemplos" value="" onChange={setAsk} options={[{ value: 'Un formulario de solicitudes de vacaciones', label: 'Vacaciones' }, { value: 'Un tablero de ventas por región', label: 'Tablero de ventas' }]} />
          <span className="flex-1" />
          <Button disabled={!ask}>Crear con mi asistente</Button>
        </>} />
      </Sample>
      <Sample title="Pasos" className="flex items-center gap-3"><StepNumber n={1} done /><StepNumber n={2} /><StepNumber n={3} /></Sample>
    </Section>
  )
}

function StateAndFeedback() {
  return (
    <Section id="estado" title="Estado y feedback" description="Los errores se explican como consecuencias, con un solo botón para resolverlos.">
      <Sample title="Estados de una app" className="flex flex-wrap gap-4">
        {(Object.keys(appStatuses) as AppStatusKind[]).map(s => <AppStatus key={s} status={s} />)}
      </Sample>
      <Sample title="Insignias" className="flex flex-wrap gap-2">
        <Badge variant="success">Publicada ahora</Badge><Badge variant="warning">Falta algo</Badge><Badge variant="destructive">No se publicó</Badge><Badge variant="info">En revisión</Badge><Badge variant="ai">tu asistente</Badge><Badge>Versión de prueba</Badge><Badge variant="count">3</Badge>
      </Sample>
      <div className="flex flex-col gap-3">
        <Callout tone="ok" title="Tu app está en línea.">La abrieron 34 personas hoy.</Callout>
        <Callout tone="bad" title="Tu app sigue funcionando." action={<Button size="sm">Arreglar con mi asistente</Button>}>Los últimos cambios no se publicaron porque una prueba falló.</Callout>
        <Callout tone="warn" title="La base de datos está casi llena.">Usa 9.1 GB de 10 GB.</Callout>
        <Callout tone="info">TI revisa las solicitudes en menos de un día hábil.</Callout>
        <ErrorNotice error={new ApiError('project archived', 422, 'BUSINESS', 'PROJECT_ARCHIVED')} onRetry={() => {}} />
      </div>
      <AttentionList count={3}>
        <AttentionItem tone="bad" icon={AlertTriangle} title="“Visitas a clientes” no publicó tus últimos cambios" action={<Button size="sm">Arreglar</Button>}>
          <b>La app sigue funcionando</b> con la versión anterior. Nadie se vio afectado.
        </AttentionItem>
        <AttentionItem tone="info" icon={Clock} title="Pediste acceso a los datos de ventas" action={<Button size="sm" variant="ghost">Ver solicitud</Button>} extra={<RequestTracker steps={requestSteps('Carlos (TI)')} current={1} />}>
          Te avisamos cuando esté listo.
        </AttentionItem>
        <AttentionItem tone="warn" icon={Database} title="La base de datos de “Inventario” está casi llena" action={<Button size="sm" variant="outline">Ampliar</Button>}>
          Usa 91% de su espacio.
        </AttentionItem>
      </AttentionList>
      <div className="grid gap-4 md:grid-cols-2">
        <EmptyState icon={AppWindow} title="Todavía no hay apps" action={<Button><Plus />Crear app</Button>}>Crea una app y publícala con la cuenta de tu empresa.</EmptyState>
        <Panel><PanelHeader title="Cargando" /><ListSkeleton /><Loading className="px-5" /></Panel>
      </div>
      <Sample title="Toasts" className="flex gap-2">
        <Button variant="outline" onClick={() => toast.success('Cambios guardados')}>Éxito</Button>
        <Button variant="outline" onClick={() => toast.error('No se pudo guardar')}>Error</Button>
      </Sample>
    </Section>
  )
}

function AppsAndVersions({ technical }: { technical: boolean }) {
  const resources: ResourceUse[] = [{ kind: 'postgres', detail: '' }, { kind: 'files', detail: '' }, { kind: 'ai', detail: '', usage: 91 }]
  return (
    <Section id="apps" title="Apps y versiones" description="“App” y “Versión”, nunca proyecto o deployment. Lo técnico aparece solo con el interruptor.">
      <PageHeader title="Visitas a clientes" status={<AppStatus status="live" />} description="Los vendedores registran visitas con fotos y ubicación" actions={<><Button variant="outline">Compartir</Button><Button>Abrir app</Button></>} />
      <div className="grid gap-4 md:grid-cols-3">
        <AppCard technical={technical} resources={<ResourceIcons resources={resources} />} app={{ name: 'Visitas a clientes', description: 'Registro de visitas de ventas', status: 'live', updated: 'hace 2 h', audience: 'Ventas', people: 12, git: { sha: 'a1b2c3d4', message: 'Agrega fotos a la visita' } }} />
        <AppCard technical={technical} app={{ name: 'Inventario', status: 'attention', updated: 'ayer', audience: 'Solo tú', people: 1, thumbnail: 'form', git: { sha: 'f00ba12', message: 'Filtro por almacén' } }} />
        <CreateAppTile />
      </div>
      <Sample title="Miniaturas" className="flex items-end gap-3"><AppThumbnail mini /><AppThumbnail mini kind="form" /><AppThumbnail mini off /></Sample>
      <Panel>
        <PanelHeader title="Versiones" meta="4" />
        <PanelList>
          <VersionRow technical={technical} version={{ id: '4', kind: 'failed', message: 'Agrega exportar a Excel', when: 'hace 10 min', by: 'tu asistente', sha: '9e8d7c6b' }} onOpen={() => {}} />
          <VersionRow technical={technical} version={{ id: '3', kind: 'preview', message: 'Nuevo diseño del formulario', when: 'hace 1 h', by: 'Ana', sha: '5d4c3b2a', branch: 'diseno' }} onPublish={() => {}} />
          <VersionRow technical={technical} version={{ id: '2', kind: 'live', message: 'Agrega fotos a la visita', when: 'hace 2 h', by: 'Ana', sha: 'a1b2c3d4' }} />
          <VersionRow technical={technical} version={{ id: '1', kind: 'previous', message: 'Primera versión', when: 'el lunes', by: 'Ana', sha: '1a2b3c4d' }} onRestore={() => {}} />
        </PanelList>
      </Panel>
      <Panel>
        <PanelHeader icon={Rocket} title="Publicando “Visitas a clientes”" />
        <ProgressSteps technical={technical} steps={[
          { icon: Hammer, title: 'Preparando tu app', description: 'Juntando el código y lo que necesita', technical: 'build: Railpack + BuildKit', state: 'done' },
          { icon: ShieldCheck, title: 'Revisando seguridad', description: 'Buscando contraseñas expuestas y fallas conocidas', technical: 'gitleaks · trivy · SBOM', state: 'running' },
          { icon: Upload, title: 'Publicando', description: 'Sin cortar a quien la esté usando', technical: 'rollout canario', state: 'pending' },
        ]} />
        <PanelFooter><span>Suele tardar 2 minutos.</span><AssistantBy person="Ana" /></PanelFooter>
      </Panel>
    </Section>
  )
}

function Resources({ technical }: { technical: boolean }) {
  return (
    <Section id="recursos" title="Recursos y uso" description="Lo que usa cada app, en palabras simples. La barra se pone ámbar al 85%.">
      <div className="grid gap-4 sm:grid-cols-3">
        <Panel><PanelBody><Stat value="$48.20" label="Este mes · de $200" progress={24} /></PanelBody></Panel>
        <Panel><PanelBody><Stat value="34" label="Personas hoy" /></PanelBody></Panel>
        <Panel><PanelBody><Stat value="91%" label="Base de datos" progress={91} /></PanelBody></Panel>
      </div>
      <Panel>
        <PanelHeader title="Lo que usa esta app" actions={<Button size="sm" variant="outline"><Plus />Agregar</Button>} />
        <PanelList>
          <ResourceRow technical={technical} resource={{ kind: 'app', detail: '2 copias en línea, se ajusta sola' }} />
          <ResourceRow technical={technical} onOpen={() => {}} resource={{ kind: 'postgres', detail: '9.1 GB de 10 GB', usage: 91, cost: 12 }} />
          <ResourceRow technical={technical} onOpen={() => {}} resource={{ kind: 'files', detail: '340 MB · 1,204 fotos', usage: 4, cost: 0.4 }} />
          <ResourceRow technical={technical} resource={{ kind: 'erp', detail: 'Clientes y pedidos, solo lectura', flag: 'waiting' }} />
          <ResourceRow technical={technical} resource={{ kind: 'key', detail: 'Falta la clave de Google Maps', flag: 'warn' }} />
        </PanelList>
      </Panel>
      <div className="grid gap-3 sm:grid-cols-2">
        <ResourceOption technical={technical} kind="postgres" selected description="Guarda información de forma ordenada" />
        <ResourceOption technical={technical} kind="vector" description="Busca por significado, no solo por palabras" />
      </div>
      <Sample title="Medidor" className="flex items-center gap-4"><UsageMeter value={30} /><UsageMeter value={86} /><UsageMeter value={100} /></Sample>
      <Panel>
        <PanelHeader icon={FileText} title="Datos · visitas" meta="1,204 filas" />
        <Table>
          <TableHeader><TableRow><TableHead>Cliente</TableHead><TableHead>Vendedor</TableHead><TableHead>Fecha</TableHead><TableHead className="text-right">Monto</TableHead></TableRow></TableHeader>
          <TableBody>
            {[['Ferretería Lima', 'Ana Ruiz', '26 oct', 'S/ 1,200'], ['Bodega San Juan', 'Luis Paz', '25 oct', 'S/ 860'], ['Minimarket Sol', 'Ana Ruiz', '24 oct', 'S/ 2,310']].map(r => (
              <TableRow key={r[0]}>{r.map((c, i) => <TableCell key={i} className={i === 3 ? 'text-right font-mono' : undefined}>{c}</TableCell>)}</TableRow>
            ))}
          </TableBody>
        </Table>
      </Panel>
    </Section>
  )
}

const request: ApprovalRequestData = {
  id: 'r1',
  requester: { name: 'Ana Ruiz', team: 'Ventas' },
  title: 'Ana quiere usar los datos de clientes',
  context: 'Visitas a clientes · hace 20 min',
  viaAssistant: true,
  reason: 'Para que los vendedores elijan el cliente de una lista en vez de escribirlo.',
  allows: ['Ver nombre, RUC y dirección de clientes', 'Leer pedidos de los últimos 12 meses'],
  denies: ['Modificar o borrar datos', 'Ver datos de contacto personales'],
  audience: 'Ventas · 12 personas',
  checks: [{ label: 'Sin contraseñas en el código', ok: true }, { label: 'Inicia sesión con la cuenta de la empresa', ok: true }, { label: 'Tiene descripción', ok: false }],
  state: 'pending',
}

function Approvals() {
  const [rules, setRules] = useState({ expire: true, review: true, public: true })
  return (
    <Section id="aprobaciones" title="Aprobaciones y reglas" description="Cada solicitud dice quién pide, para qué, qué podrá y qué no. Las reglas se leen como frases.">
      <div className="grid gap-4 lg:grid-cols-[340px_1fr]">
        <Panel className="self-start">
          <PanelHeader title="Bandeja" meta="2" />
          <PanelList>
            <ApprovalRow request={request} selected />
            <ApprovalRow request={{ ...request, id: 'r2', requester: { name: 'Luis Paz' }, title: 'Luis quiere compartir “Inventario” con toda Acme', context: 'hace 1 día' }} />
            <ApprovalRow request={{ ...request, id: 'r3', title: 'Acceso a pedidos', context: 'Aprobada por ti · lunes', state: 'approved' }} />
          </PanelList>
        </Panel>
        <Panel>
          <PanelHeader title={request.title} description={<span className="flex items-center gap-2">{request.context} <AssistantBy person="Ana" /></span>} />
          <ApprovalDetail request={request} duration="6 meses, luego se vuelve a pedir" />
          <PanelFooter className="justify-end"><Button variant="outline">No aprobar</Button><Button>Aprobar</Button></PanelFooter>
        </Panel>
      </div>
      <Panel>
        <PanelHeader icon={ShieldCheck} title="Datos y accesos" description="Quién puede usar qué, y por cuánto tiempo" />
        <RuleRow checked={rules.expire} onCheckedChange={v => setRules(r => ({ ...r, expire: v }))}>Las aprobaciones de datos vencen a los <b>6 meses</b></RuleRow>
        <RuleRow checked={rules.review} onCheckedChange={v => setRules(r => ({ ...r, review: v }))}>Compartir con <b>toda la empresa</b> necesita la aprobación de TI</RuleRow>
        <RuleRow checked={rules.public} locked>Las apps <b>públicas</b> necesitan además la aprobación de Seguridad</RuleRow>
      </Panel>
      <Panel>
        <PanelHeader title="Compartir “Visitas a clientes”" />
        <PanelBody className="gap-0 divide-y">
          <PersonRow name="Ana Ruiz" detail="ana@acme.com · Dueña" color="var(--primary)" />
          <PersonRow name="Ventas" detail="12 personas" group trailing={<Badge>Lector</Badge>} />
          <PersonRow name="Carlos Díaz" detail="TI" trailing={<Badge variant="ai">Aprobador</Badge>} />
        </PanelBody>
      </Panel>
      <Sample title="Avatares" className="flex items-center gap-2"><Avatar name="Ana Ruiz" /><Avatar name="Luis Paz" size="lg" /><Avatar name="Carlos Díaz" muted size="lg" /><Avatar name="Ventas" group size="lg" /></Sample>
      <Sample title="Seguimiento" className="flex flex-col gap-2">
        <RequestTracker steps={requestSteps('Carlos (TI)')} current={0} />
        <RequestTracker steps={requestSteps('Carlos (TI)')} current={3} />
        <RequestTracker steps={requestSteps('Seguridad')} current={1} rejected />
      </Sample>
    </Section>
  )
}

function TechnicalSection({ technical }: { technical: boolean }) {
  const [tab, setTab] = useState('general')
  return (
    <Section id="tecnico" title="Detalles técnicos" description="Siempre a un clic para todos; abiertos por defecto en modo técnico.">
      <Panel>
        <PanelHeader icon={FlaskConical} title="Arreglar con mi asistente" />
        <PanelBody>
          <AssistantPrompt text="La versión “Agrega exportar a Excel” de Visitas a clientes no se publicó: la prueba export.test.ts falló (Cannot find module 'xlsx'). Arréglalo y vuelve a publicar." />
        </PanelBody>
        <TechnicalDetails defaultOpen={technical}>
          <LogViewer lines={[
            [{ text: '14:02:11 ', tone: 'time' }, { text: 'build ', tone: 'key' }, 'railpack detected node 22'],
            [{ text: '14:02:48 ', tone: 'time' }, { text: '✓ ', tone: 'ok' }, 'image built in 37s'],
            [{ text: '14:03:02 ', tone: 'time' }, { text: 'warn ', tone: 'warn' }, '2 moderate vulnerabilities'],
            [{ text: '14:03:15 ', tone: 'time' }, { text: '✗ ', tone: 'error' }, "export.test.ts: Cannot find module 'xlsx'"],
          ]} />
        </TechnicalDetails>
      </Panel>
      <Sample title="Comandos" className="flex flex-col gap-2">
        <CodeLine code="claude mcp add --transport http odyssey https://odyssey.acme.com/mcp" />
        {technical && <GitLine sha="a1b2c3d4e5" message="Agrega fotos a la visita" />}
      </Sample>
      <Panel>
        <PanelBody><KeyValue items={[['Dirección', <span className="font-mono">visitas.apps.acme.com</span>], ['Creada', '12 oct 2026'], ['Dueña', 'Ana Ruiz']]} /></PanelBody>
      </Panel>
      <Tabs value={tab} onValueChange={v => setTab(String(v))}>
        <TabsList><TabsTrigger value="general">General</TabsTrigger><TabsTrigger value="vars">Variables</TabsTrigger></TabsList>
        <TabsContent value="general" className="pt-3 text-[13px] text-muted-foreground">Pestañas para subsecciones dentro de una tarjeta.</TabsContent>
        <TabsContent value="vars" className="pt-3"><CodeLine code="DATABASE_URL=postgres://…" /></TabsContent>
      </Tabs>
      <Hero title="¿Qué quieres crear?">Encabezado de página completa (onboarding y crear).</Hero>
      <SectionHeader title="Encabezado de sección" actions={<Button size="sm" variant="ghost">Ver todo</Button>} />
      <Panel interactive><PanelList><PanelRow interactive><IconBadge icon={AppWindow} size="sm" /><span className="flex-1">Fila clicable</span><span className="text-xs text-muted-foreground">hace 2 h</span></PanelRow></PanelList></Panel>
    </Section>
  )
}
