# Odyssey console (frontend)

Vite + React 19 SPA. shadcn (style **base-nova**, base-ui primitives), Tailwind v4,
TanStack Query, react-router 7. UI language: Spanish. Dark by default.

```sh
npx -y npm@11 install   # npm 10.9 crashes on this tree; use npm 11
npm run dev             # http://localhost:5178 (proxies /api, /health → :8095)
npm run check           # typecheck + oxlint + vitest + build
```

`ODYSSEY_API_URL` overrides the API the dev server proxies to.

## Design system

- **Living reference: <http://localhost:5178/design>**. It needs no login and uses
  sample data from the v3 mockup. It has a "Detalles técnicos" switch.
- Tokens: `src/theme.css` holds the dark default and `.light`. `src/index.css`
  maps them to Tailwind (`bg-inset`, `text-ai`, `text-title`, …). The palette
  is IAMKit's.
- `src/components/ui/`: shadcn primitives, restyled to the mockup. Add more with
  `npx shadcn@latest add <name>`. The registry imports `cn` from `"cn"`, which is
  aliased in Vite and tsconfig.
- `src/components/odyssey/`: product components. Pages import from
  `@/components/odyssey`, not the files directly.

| Area | Components |
|---|---|
| Meaning | `Tone`, `AppStatus`/`StatusDot` (En línea · Publicando… · Necesita atención · En pausa), `IconBadge`, `Callout`, `Badge` variants |
| Layout | `AppShell`, `NavTabs`, `Crumb`, `PageHeader`, `SectionHeader`, `Hero`, `Panel*`, `SubNav` |
| Header | `CommandPalette` (⌘K), `NotificationsMenu`, `UserMenu` (technical toggle + theme) |
| Apps | `AppCard`, `AppThumbnail`, `CreateAppTile`, `VersionRow`, `GitLine`, `ProgressSteps` |
| Attention | `AttentionList`/`AttentionItem`, `RequestTracker` |
| Resources | `ResourceRow`, `ResourceIcons`, `ResourceOption`, `UsageMeter` (≥85% → warning), `Stat`, `KeyValue` |
| Governance | `ApprovalRow`, `ApprovalDetail`, `RuleRow` (sentence + switch + lock), `PersonRow`, `Avatar` |
| Input | `ChipGroup`, `OptionCards`, `SwitchField`, `AskBox`, `Term` (jargon tooltip) |
| Technical | `TechnicalDetails`, `LogViewer`, `CodeLine`, `AssistantPrompt`, `AssistantBy` |
| Feedback | `EmptyState`, `Loading`, `ListSkeleton`, `ErrorNotice` |

### UX rules (from the UX v3 spec)

1. Use plain words: "App", "Versión", "Volver a esta". Never write
   deployment/rollback/pod. Wrap jargon in `<Term help=…>`.
2. **Technical mode only adds** detail (commit, branch, engine, logs). Pass
   `technical` from `usePreferences()`, or wrap content in `<Technical>`.
3. An error states the consequence first ("Tu app sigue funcionando"). It
   offers **one** primary action.
4. Colours carry meaning through `tone`. Never use raw colours for state.

## Code map

```
src/lib/session.ts      OAuth code+PKCE against IAMKit (config from GET /api/config)
src/lib/auth.tsx        AuthGate, useAuth().can(permission)
src/lib/api.ts          fetch wrapper → ApiError (docs/api.md §3), errorMessage() in Spanish
src/lib/projects.ts     TanStack Query hooks for /api/v1/projects, slugify (mirrors the API)
src/lib/preferences.tsx per-user "Mostrar detalles técnicos"
src/pages/              routes: /, /asistente, /apps/:id, /auth/callback, /design
```
