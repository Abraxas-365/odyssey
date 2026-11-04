# Preparar la app para Odicea

Antes de `deploy_folder`, revisa esto en la carpeta del usuario. Odicea construye la carpeta por ti: usa el `Dockerfile` si lo hay y, si no, detecta el tipo de app (Node, Python, Go…) y la arranca con su comando estándar.

## Checklist

1. **Un solo servicio web.** La app escucha HTTP en el puerto de la variable `$PORT` (8080 si no está definida) y en todas las interfaces (`0.0.0.0`, no solo `localhost`).
2. **Responde en `/`** con un 200 (o define otra ruta con `health_path` en la herramienta). Odicea espera esa respuesta antes de mostrar la versión a nadie.
3. **Se sabe arrancar.** Tiene lo que el constructor necesita para reconocerla:
   - Node: `package.json` con script `start` (y `build` si hace falta compilar).
   - Python: `requirements.txt` o `pyproject.toml` y un comando de arranque claro (por ejemplo gunicorn/uvicorn en `$PORT`).
   - Go: `go.mod` y un `main` que arranque el servidor.
   - Cualquier otra cosa: un `Dockerfile` que construya y arranque la app.
4. **El código está completo.** La subida respeta `.gitignore` y excluye `.git`, `node_modules` y `.env*`: las dependencias se instalan en el build, no se suben. Verifica que el lockfile (`package-lock.json`, `poetry.lock`, `go.sum`…) esté en la carpeta.
5. **Sin secretos.** Busca contraseñas, llaves y cadenas de conexión en el código y en archivos de configuración. Si las hay, sácalas a variables de entorno que se lean al arrancar y no pongas su valor en ninguna parte. Ver «Lo que Odicea todavía no ofrece» en [SKILL.md](../SKILL.md).
6. **Sin servicios externos.** Si la app trae SDKs o configuración de Supabase, Firebase, Vercel, Neon, etc., no los conectes ni crees cuentas: avisa al usuario y a TI según [SKILL.md](../SKILL.md).
7. **Estado.** El disco de la app no es permanente: no guardes datos importantes en archivos locales.

## Qué pasar a `deploy_folder`

- `port`: solo si la app escucha en un puerto fijo distinto de 8080 (lo normal es leer `$PORT`).
- `health_path`: solo si `/` no responde 200 (por ejemplo `/health`).
- `replicas`: de 1 a 10; por defecto 1.
- `env`: variables que **no** son secretos (idioma, zona horaria, modo).

## Antes de subir

Dile al usuario qué app vas a publicar y qué cambió. Después sigue el flujo de [SKILL.md](../SKILL.md).
