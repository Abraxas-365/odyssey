# Apps de varias partes (monorepo)

Algunas apps tienen **varias partes en un mismo código**: por ejemplo un frontend en `apps/web`, una API en `apps/api` y un proceso de fondo en `apps/jobs`. En Odicea siguen siendo **una sola app** (un nombre, una dirección, un historial de versiones, una sola forma de volver atrás), con varias partes que se publican juntas.

Una app nueva tiene una sola parte (`web`, en `/`, con todo el código): si el código es una sola app, no hagas nada de esto.

## Cuándo

- La carpeta (o el repositorio) tiene varias apps en subcarpetas, cada una con su `package.json`, `go.mod`, `pyproject.toml`, `Dockerfile`… y a menudo un archivo de workspace (`pnpm-workspace.yaml`, `turbo.json`, `nx.json`, `go.work`).
- No la uses para separar un frontend que el backend ya sirve (eso es una sola parte).

## Definir las partes

1. **Propón** las partes con `detect_services`:
   - Carpeta del usuario: pasa `paths`, las rutas de sus archivos relativas a la carpeta que vas a publicar (por ejemplo la salida de `git ls-files`, sin `node_modules` ni `.git`). Basta con las que terminan en los nombres que dice su descripción.
   - Repositorio de GitHub: pasa `repository` (y `branch` si no es la principal); Odicea lee su árbol.
   - Solo lee nombres de archivos y **no guarda nada**.
2. **Muestra la propuesta al usuario** en lenguaje llano: cada parte con su nombre, su carpeta y qué parte de la dirección atiende (`/` el sitio, `/api` todo lo que empieza con `/api`; un worker no atiende ninguna). Muestra también las `notes`. Ajusta lo que pida.
3. **Guárdalas** con `set_services` (`app` e `items`, la lista completa en orden). Cada parte:
   - `name`: minúsculas y números, hasta 20 (`web`, `api`, `jobs`).
   - `kind`: `web` (responde en la dirección de la app) o `worker` (sin puerto: consumidores de colas, tareas programadas).
   - `root`: su carpeta, relativa a la carpeta que se publica.
   - `route`: `/`, `/api`… o vacía para una parte web interna que solo ven las demás.
   - `strip_prefix`: verdadero si la parte espera `/users` y no `/api/users`.
   - Máximo 8 partes. La misma lista no cambia nada.
4. `list_services` muestra cómo quedaron.

Si `set_services` responde `pending_approval`, las reglas de TI piden aprobarlo: sigue [reglas-ti.md](reglas-ti.md). Si falla con «El clúster de esta app todavía no puede correr varios servicios», es de TI (su agente es antiguo): díselo al usuario y no lo intentes de otra forma.

## Publicar

- **Carpeta**: `deploy_folder` como siempre, pero **ejecuta el comando en la carpeta que contiene todas las partes** (la raíz del monorepo), nunca dentro de una de ellas; la respuesta trae `services` con sus carpetas. Cada parte se construye desde su carpeta.
- **GitHub**: `connect_repository` **sin `root`** (la raíz del repo); cada parte toma su carpeta. Cada push publica la app entera.
- Solo se **construyen las partes que cambiaron**; las demás reutilizan lo ya construido. `get_deployment` dice qué partes cambian (`changed`) y en `builds` cuáles se construyeron y cuáles se reutilizaron (`reused`).
- Para construir todo de nuevo (por ejemplo tras una actualización de la imagen base o un arreglo fuera del código), pasa `rebuild: true` a `deploy_folder` o a `publish_now`.
- Cada parte se prepara como en [preparar-app.md](preparar-app.md): escucha en `$PORT` (los workers no), arranca sola y no trae secretos. Las partes se hablan por la dirección de la app (`/api/...`) desde el navegador; entre ellas, por el nombre de la parte.

## Estado y fallas

- `get_app` trae `services`: cada parte con su estado (`online`, `starting`, `failed`, `unknown` o `not_published`). Una parte en `failed` hace que la app necesite atención.
- Si falla el build de una parte, `get_deployment` la nombra: lee sus registros con `get_build_logs` y `service` (sin `service` muestra la primera que falló). Sigue [diagnosticar.md](diagnosticar.md).
- Volver atrás (`rollback`) devuelve **todas** las partes a esa versión, y también la lista de partes que tenía.

## Dar algo solo a algunas partes

`set_env`, `request_secret`, `create_database` y `enable_ai` aceptan `services`: la lista de partes que lo reciben (por ejemplo solo `api` recibe `DATABASE_URL` y solo `jobs` la llave de IA). Sin `services` lo recibe toda la app. Un volumen de archivos (`files`) en una app de varias partes va a una sola parte. Si falla con `SERVICE_SETTINGS_UNAVAILABLE`, el clúster todavía no puede darlo por parte: es de TI; mientras tanto, dáselo a toda la app si el usuario está de acuerdo.
