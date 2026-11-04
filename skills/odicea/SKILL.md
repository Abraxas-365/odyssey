---
name: odicea
description: Prepara, publica y diagnostica apps internas en Odicea (la plataforma de la empresa) con sus herramientas MCP; úsala cuando pidan publicar, desplegar, arreglar o volver atrás una app en Odicea. Prepares, publishes and troubleshoots internal apps on Odicea through its MCP tools; use it when asked to publish, deploy, fix or roll back an app on Odicea.
---

# Odicea

Odicea publica apps internas de la empresa en su propia infraestructura. Tú preparas el código en la carpeta del usuario y lo publicas con las herramientas MCP del servidor `odicea`. El usuario no tiene que saber de servidores: háblale en lenguaje llano y di qué hiciste y qué sigue.

*(English: Odicea runs the company's internal apps on its own infrastructure. You prepare the code in the user's folder and publish it with the `odicea` MCP server's tools. Reply in the user's language.)*

## Reglas que no se negocian

1. **Nada de servicios externos.** No publiques ni conectes la app a hosting, bases de datos o servicios de terceros por tu cuenta: Vercel, Netlify, Supabase, Neon, PlanetScale, Firebase, Render, Railway, Fly.io, Heroku, Cloudflare Pages/Workers, AWS/GCP/Azure ni parecidos. No crees cuentas, proyectos ni llaves en ellos. La app corre en Odicea y nada más.
2. **Nada de secretos** (contraseñas, tokens, llaves de API, cadenas de conexión) en el código, en `env` de las herramientas, en archivos `.env` que vayas a subir ni en commits. `env` solo lleva valores que no son secretos.
3. **No muestres ni guardes el enlace de subida** de `deploy_folder`: sirve una sola vez y es una credencial.
4. Antes de publicar, di al usuario qué vas a publicar y en qué app. Si la app no existe, créala con su permiso.
5. Si algo no es posible hoy (ver la sección siguiente), **díselo con claridad y pide ayuda a TI**; no improvises un reemplazo.

<!-- ODICEA:LIMITES-ACTUALES inicio — sección única y fácil de actualizar (fase 2). Si Odicea ya ofrece alguna de estas cosas, edita solo esta sección. -->
## Lo que Odicea todavía no ofrece

Hoy Odicea **no provee** a las apps:

- **Base de datos**: no hay una base de datos administrada por Odicea.
- **Secretos** (contraseñas, llaves, tokens): no hay dónde guardarlos ni cómo inyectarlos en la app.
- **Llaves o cuentas de IA** (OpenAI, Anthropic, Gemini…): Odicea no las entrega.

Si la app necesita alguna de ellas:

1. **No la crees en un servicio externo** ni pongas datos de prueba que parezcan reales.
2. Dile al usuario, en una frase: *«Esta app necesita [base de datos / secretos / llave de IA] y Odicea todavía no la ofrece. Pídele a TI que conecte una que ya exista en la empresa.»* Si puedes, deja escrito qué variables o qué servicio hace falta, sin valores.
3. Sigue con lo que sí se puede: deja la app lista, leyendo esa configuración de variables de entorno (sin valores por defecto secretos), y publícala solo si el usuario quiere verla funcionar parcialmente.

Mientras tanto, si la app solo necesita guardar datos de forma simple y el usuario lo acepta, puedes usar el disco/memoria de la app sabiendo que **se pierde al reiniciar o publicar**: avísaselo.
<!-- ODICEA:LIMITES-ACTUALES fin -->

## Qué puedes hacer (las 13 herramientas)

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

## Cómo hablar con el usuario

- Corto y concreto: qué hiciste, qué pasó, qué sigue. Sin jerga (di «versión», no «deployment»; «publicar», no «desplegar»).
- Cuando algo falle, di la causa en una frase y qué vas a hacer. Los errores traen `next_step`: léelo.
- No prometas lo que Odicea no hace (ver «Lo que Odicea todavía no ofrece»).
