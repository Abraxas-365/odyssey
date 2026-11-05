# Diagnosticar una publicación que falló

## 1. Mira el estado

`get_deployment` (app, y `deployment_id` si lo tienes). Lee `status`, `step`, `failure` y `next_step`. `get_app` muestra también la versión que sigue en línea.

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
