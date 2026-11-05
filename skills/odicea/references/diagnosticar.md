# Diagnosticar una publicación que falló

## 1. Mira el estado

`get_deployment` (app, y `deployment_id` si lo tienes). Lee `status`, `step`, `failure` y `next_step`. `get_app` muestra también la versión que sigue en línea.

Antes de buscar en el código, mira **`get_app`**: si `needs_attention` es `true`, `uses.attention` dice en palabras simples qué le falta o qué la bloquea. Para ver de un vistazo qué apps (y solicitudes) necesitan algo, usa **`get_attention`**: cada elemento trae una frase y el enlace de la consola; `all_in_order` en `true` es "todo en orden", y si `unavailable` trae fuentes, esa parte no se pudo revisar (no digas "todo en orden"). En `list_apps`, `needs_attention` es una pista rápida (última publicación fallida o vulnerabilidades críticas) limitada como `get_attention`: quien no es de TI solo la ve en las apps que creó, y en las demás va ausente; ausente significa que no se pudo revisar o que no te corresponde, nunca «todo bien». Un elemento `app_vulnerable` o `deployment_failed` es de TI o de quien publica: muestra el mensaje y la ruta de la consola, no intentes "arreglar" la imagen a ciegas. Cada elemento trae `who`: **`owner`** (la gente de la app lo puede resolver), **`it`** (solo TI) o **`wait`** (en curso: no hay nada que hacer, espera). `needs_attention` solo cuenta `owner` e `it`, o una publicación que falló. Si una sección de `uses` viene `null` y su nombre está en `uses.unavailable`, no se pudo leer ahora: no significa que esté bien ni que esté mal; vuelve a llamar `get_app` en un momento. Cada `kind` tiene su salida:

| `kind` | `who` | Qué pasa | Qué hacer |
|---|---|---|---|
| `secret_missing` | owner | Un secreto no tiene valor (o hay que cargarlo de nuevo porque cambió la llave del clúster); `uses.secrets.missing` dice cuáles | Nunca pidas el valor por el chat: `request_secret` y que una persona lo cargue en la consola |
| `secrets_unavailable` | it | El clúster todavía no puede recibir datos privados, o la bóveda de la empresa no se pudo leer (`uses.secrets.unavailable` cuenta cuántos) | No pidas cargar secretos de nuevo: no servirá. Es de TI; avísales |
| `resource_not_ready` | wait | Una base de datos, caché o archivos aún se prepara o se elimina | Espera y vuelve a mirar `get_app`; no republiques en bucle |
| `resource_failed` | it | Un recurso falló (`uses.resources[].detail` trae la razón) | No lo recrees a ciegas: pide ayuda a TI con el nombre del recurso |
| `ai_error` | it | No se pudo retirar una llave de IA anterior, o la app usa la IA y ya no está disponible (el gateway no está conectado) | Es de TI/plataforma; avísalo, no cambies el código |
| `ai_pending` | wait | La IA se está activando, renovando o desactivando | Espera y vuelve a mirar `get_app` |
| `config_pending` | wait | Los últimos cambios de configuración (acceso, variables…) aún no llegan a la app en línea | Espera unos segundos; si sigue, mira `get_deployment` de la publicación de configuración |
| `config_failed` | owner | La publicación de configuración falló y nadie la reintenta | Mira `get_deployment` de esa publicación; guardar el cambio otra vez (por ejemplo `set_access`) la reintenta |
| `egress_not_applied` | it o wait | El clúster limita la salida a internet pero no la aplicó: `wait` si su agente aún no respondió; `it` si no puede o falló | Si es `it`, es de TI: si la app necesita llamar a un servicio externo, pídeles que lo permitan |

Una publicación pasa por: **build** (construir la carpeta) → **apply** (enviarla al servidor) → **wait_ready** (esperar a que responda) → **promote** (dejarla en línea). El `step` dice dónde se quedó. Si falla después de llegar al servidor y había otra versión sirviendo, Odicea restaura la anterior sola: dilo al usuario.

## 2. Según el código de error

| Código | Qué significa | Qué hacer |
|---|---|---|
| `UNSUPPORTED_APP` | No reconoció el tipo de app o cómo arrancarla | Revisa [preparar-app.md](preparar-app.md): `package.json` con `start`, `requirements.txt`, `go.mod` o un `Dockerfile`. Corrige y vuelve a publicar |
| `BUILD_FAILED` | Falló un paso del build | `get_build_logs`, encuentra la primera línea de error real, corrígela, vuelve a publicar |
| `BUILD_TIMEOUT` | El build tardó demasiado | Reduce dependencias o pasos pesados; `get_build_logs` muestra dónde se va el tiempo |
| `SOURCE_INVALID` | No se pudo leer la carpeta subida | Vuelve a pedir `deploy_folder` y sube de nuevo desde la raíz de la app |
| `SOURCE_EXPIRED` | La carpeta subida ya no está disponible | Publica de nuevo |
| `HEALTH_CHECK_FAILED` / `NOT_READY` | Arrancó pero no responde a tiempo | Verifica que escuche en `$PORT` y `0.0.0.0` y que `health_path` devuelva 200; si necesita una base de datos, créala con `create_database` (la app la lee de `DATABASE_URL`) |
| `RESOURCE_NOT_READY` | Una base de datos, caché o espacio de archivos de la app no quedó listo a tiempo | No es el código: revisa su estado con `list_resources`; si dice que falló, pide ayuda a TI con el nombre del recurso. Si sigue preparándose, espera y publica de nuevo |
| `APP_CRASHED` | La app se cierra al arrancar | Casi siempre falta configuración o una dependencia; reproduce el arranque y revisa errores |
| `IMAGE_PULL_FAILED`, `PUSH_FAILED`, `NO_BUILDER`, `NO_CLUSTER`, `APPLY_FAILED`, `PROMOTE_FAILED`, `INTERNAL` | Problema de la plataforma, no del código | No cambies el código a ciegas: reintenta una vez y, si persiste, pide ayuda a TI con el `deployment_id` |
| `PROJECT_ARCHIVED` | La app está archivada | Pide al usuario que la restaure en la consola |

## 3. Si falla por una dependencia que Odicea no ofrece

Base de datos o llaves de IA: Odicea las ofrece (`create_database`, `enable_ai`); **no las inventes ni uses un servicio externo.** Si fallan, el mensaje dice si es cosa de TI. Otros secretos o llaves de terceros: sigue «Lo que Odicea todavía no ofrece» en [SKILL.md](../SKILL.md) y pide a TI que conecte uno existente.

## 4. Volver a lo que funcionaba

Si el usuario necesita la app ya: `list_versions` y `rollback`. Luego arregla con calma.

## 5. Cuando termines

Cuenta en una o dos frases la causa, qué cambiaste y el resultado (la URL si quedó en línea).
