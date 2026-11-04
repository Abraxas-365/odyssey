---
name: odicea
description: Prepara, publica y diagnostica apps internas en Odicea (la plataforma de la empresa) con sus herramientas MCP; úsala cuando pidan publicar, desplegar, arreglar o volver atrás una app en Odicea. Prepares, publishes and troubleshoots internal apps on Odicea through its MCP tools; use it when asked to publish, deploy, fix or roll back an app on Odicea.
---

# Odicea

Odicea publica apps internas de la empresa en su propia infraestructura. Tú preparas el código en la carpeta del usuario y lo publicas con las herramientas MCP del servidor `odicea`. El usuario no tiene que saber de servidores: háblale en lenguaje llano y di qué hiciste y qué sigue.

*(English: Odicea runs the company's internal apps on its own infrastructure. You prepare the code in the user's folder and publish it with the `odicea` MCP server's tools. Reply in the user's language.)*

## Reglas que no se negocian

1. **Nada de servicios externos.** No publiques ni conectes la app a hosting, bases de datos o servicios de terceros por tu cuenta: Vercel, Netlify, Supabase, Neon, PlanetScale, Firebase, Render, Railway, Fly.io, Heroku, Cloudflare Pages/Workers, AWS/GCP/Azure ni parecidos. No crees cuentas, proyectos ni llaves en ellos. La app corre en Odicea y nada más.
2. **Nada de secretos** (contraseñas, tokens, llaves de API, cadenas de conexión) en el código, en `env` de las herramientas, en `set_env`, en archivos `.env` que vayas a subir ni en commits. **Nunca pidas ni aceptes el valor de un secreto por el chat**: usa `request_secret` y que una persona lo cargue en la consola.
3. **No muestres ni guardes el enlace de subida** de `deploy_folder`: sirve una sola vez y es una credencial.
4. Antes de publicar, di al usuario qué vas a publicar y en qué app. Si la app no existe, créala con su permiso.
5. Si algo no es posible hoy (ver la sección siguiente), **díselo con claridad y pide ayuda a TI**; no improvises un reemplazo.

<!-- ODICEA:LIMITES-ACTUALES inicio — sección única y fácil de actualizar (fase 2). Si Odicea ya ofrece alguna de estas cosas, edita solo esta sección. -->
## Lo que Odicea todavía no ofrece

Hoy Odicea **no provee** a las apps:

- **Base de datos**: no hay una base de datos administrada por Odicea.
- **Llaves o cuentas de IA** (OpenAI, Anthropic, Gemini…): Odicea no las entrega.

Si la app necesita alguna de ellas:

1. **No la crees en un servicio externo** ni pongas datos de prueba que parezcan reales.
2. Dile al usuario, en una frase: *«Esta app necesita [base de datos / llave de IA] y Odicea todavía no la ofrece. Pídele a TI que conecte una que ya exista en la empresa.»* Si ya existe, su cadena de conexión o llave es un secreto: pídela con `request_secret` (nunca por el chat).
3. Sigue con lo que sí se puede: deja la app lista, leyendo esa configuración de variables de entorno (sin valores por defecto secretos), y publícala solo si el usuario quiere verla funcionar parcialmente.

Mientras tanto, si la app solo necesita guardar datos de forma simple y el usuario lo acepta, puedes usar el disco/memoria de la app sabiendo que **se pierde al reiniciar o publicar**: avísaselo.
<!-- ODICEA:LIMITES-ACTUALES fin -->

## Qué puedes hacer (las 16 herramientas)

| Quiero… | Herramienta |
|---|---|
| Saber con qué cuenta y permisos estoy | `whoami` |
| Ver las apps y su estado | `list_apps`, `get_app` |
| Crear una app vacía | `create_app` |
| Publicar la carpeta del usuario | `deploy_folder` (+ ejecutar el comando que devuelve) |
| Publicar un repositorio de GitHub | `list_repositories`, `connect_repository`, `publish_now` |
| Publicar una imagen ya construida (por digest) | `deploy_image` |
| Seguir una publicación | `get_deployment` |
| Ver por qué falló el build | `get_build_logs` |
| Ver el historial o volver atrás | `list_versions`, `rollback` |
| Cambiar quién puede abrir la app | `set_access` (y `get_app` muestra el modo) |
| Poner o quitar variables (no secretas) | `set_env` |
| Pedir un secreto (una persona lo carga en la consola) | `request_secret` |

Solo ves las herramientas que tu rol permite (por ejemplo, un Lector solo lee). Si una falla por permisos, llama a `whoami` y explica qué permiso falta.

## Flujo para publicar una carpeta

1. `whoami` y `list_apps`: ¿existe ya la app? Si no, `create_app` con un nombre claro.
2. **Prepara la app** (puerto, health, arranque, sin secretos): sigue [references/preparar-app.md](references/preparar-app.md).
3. `deploy_folder` (app, y `port`, `health_path`, `replicas` o `env` solo si hace falta). Devuelve un `command`: **ejecútalo tú en una shell, dentro de la carpeta de la app**.
4. `get_deployment` con `wait_seconds` (30) y repítelo mientras `done` sea falso.
5. Si queda en línea, comparte la URL. Si falla, sigue [references/diagnosticar.md](references/diagnosticar.md).

Si el comando de subida falla con un enlace vencido o usado, pide otro `deploy_folder`.

## Publicar desde GitHub

Para que cada push publique la app, sigue [references/github.md](references/github.md).

## Volver atrás

`list_versions` para elegir; `rollback` (sin `deployment_id` vuelve a la última versión que estuvo en línea) y luego `get_deployment` para confirmarlo.

## Quién puede abrir la app

Por defecto una app nueva solo la abre **el equipo** de la organización, con su inicio de sesión de Odicea. `get_app` muestra `access.mode`: `team` (solo el equipo), `organization` (toda la empresa) o `public` (cualquiera con el enlace, sin iniciar sesión). Para cambiarlo, `set_access` con el modo que pida el usuario; si la app ya está en línea se vuelve a publicar la misma versión (sigue con `get_deployment`). `public` exige un permiso que suele tener solo TI: si falla por eso, no lo intentes por otro lado; dile al usuario que pida a TI que la deje pública. Si queda en `failed` con «Este clúster no puede proteger apps con inicio de sesión», es de TI, no de la app. No metas inicio de sesión propio en la app para esto: Odicea ya lo hace antes de que llegue.

## Variables y secretos

- **Variables** (valores que no son secretos: zona horaria, nivel de log, URLs públicas): `set_env` con `set` (nombre → valor) y/o `unset`. Quedan para todas las publicaciones de la app y ganan sobre el `env` de `deploy_folder`. Si la app está en línea se vuelve a publicar la misma versión (sigue con `get_deployment`).
- **Secretos** (contraseñas, llaves, tokens, cadenas de conexión): **tú nunca ves ni escribes el valor**. Llama a `request_secret` con el `name` de la variable y una `note` corta de para qué es. Devuelve `console_url`: compártelo con el usuario para que una persona con acceso cargue ahí el valor (se cifra en su navegador; ni Odicea lo puede leer). Mientras falte, la app sigue como está. Cuando lo carguen, Odicea publica de nuevo con el secreto como variable de entorno del mismo nombre.
- Haz que la app lea esos nombres de variables de entorno y falle con un mensaje claro si falta uno; no pongas valores por defecto que parezcan reales.
- Un secreto de la bóveda de la empresa lo conecta TI desde la consola, no tú.

## Cómo hablar con el usuario

- Corto y concreto: qué hiciste, qué pasó, qué sigue. Sin jerga (di «versión», no «deployment»; «publicar», no «desplegar»).
- Cuando algo falle, di la causa en una frase y qué vas a hacer. Los errores traen `next_step`: léelo.
- No prometas lo que Odicea no hace (ver «Lo que Odicea todavía no ofrece»).
