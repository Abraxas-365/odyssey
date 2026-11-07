# Publicar desde GitHub

Con un repositorio conectado, **cada push a la rama publica la app** (sin que el asistente haga nada). Úsalo cuando el código ya vive en GitHub.

## Conectar

1. `list_repositories` (con `search` si hay muchos): lista solo los repositorios de las cuentas donde está instalada la GitHub App de Odicea.
   - Si no hay ninguna cuenta conectada, `next_step` trae un enlace de la consola para instalarla: **eso requiere un navegador y a alguien con permiso en GitHub**; pásale el enlace al usuario (o a TI) y espera.
   - Si el repositorio no aparece, la App no tiene acceso a él: pídele a TI que lo agregue a la instalación.
2. `create_app` si la app no existe.
3. `connect_repository` con `app` y `repository` (`owner/nombre`). Opciones:
   - `branch`: por defecto la rama principal.
   - `root`: carpeta de la app dentro del repo (vacía si es la raíz). Si el repo tiene **varias apps** (un monorepo), no elijas una con `root`: define sus partes como en [varias-partes.md](varias-partes.md) (`detect_services` con `repository`, luego `set_services`) y conecta sin `root`.
   - `port`, `health_path`, `replicas`, `env`: como en [preparar-app.md](preparar-app.md) (`env` sin secretos).
   Publica el último commit ya y devuelve la publicación.
4. `get_deployment` con `wait_seconds` hasta que `done` sea verdadero.

## Publicar sin esperar un push

`publish_now` publica el último commit de la rama conectada. En una app de varias partes solo se construyen las que cambiaron; con `rebuild: true` se construyen todas de nuevo.

## Reglas

- Antes de conectar, prepara la app como en [preparar-app.md](preparar-app.md) y que el cambio esté **subido** a GitHub (el asistente no hace push sin que el usuario lo pida).
- No guardes secretos en el repositorio. Ver «Lo que Odicea todavía no ofrece» en [SKILL.md](../SKILL.md).
- Si el build falla: `get_build_logs` y [diagnosticar.md](diagnosticar.md).
- Si GitHub no está habilitado en esta instalación (`whoami` lo dice), publica la carpeta con `deploy_folder`.
