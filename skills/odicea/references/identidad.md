# Saber quién es el usuario de la app

Solo aplica si la app tiene **«Quién puede abrirla» = `team` u `organization`** (`get_app` → `access.mode`). En una app `public` no hay inicio de sesión: Odicea no manda nada de esto y no hay usuario que conocer.

## Qué recibe la app

Odicea inicia la sesión antes de que el pedido llegue y le agrega cabeceras:

| Cabecera | Qué es | Para qué sirve |
|---|---|---|
| `X-Odicea-User`, `X-Odicea-Email`, `X-Odicea-Name`, `X-Odicea-Org` | quién es el usuario, en texto plano | **solo mostrar** («Hola, Ana») |
| `X-Odicea-Assertion` | prueba firmada de quién es (un JWT que vale 2 minutos) | **verificarla** antes de decidir algo |

Y estas variables de entorno (solo en apps `team`/`organization`; no las pongas tú con `set_env`):

- `ODICEA_AUTH_JWKS_URL`: de dónde bajar las llaves públicas con que verificar.
- `ODICEA_AUTH_AUDIENCE`: la `aud` que debe traer la aserción (el host de la app tal como lo escribe el navegador, con el puerto si lo tiene: `host[:puerto]`).
- `ODICEA_AUTH_ISSUER`: el emisor esperado (`odicea-gate`).
- `ODICEA_AUTH_HEADER`: la cabecera con la aserción (`X-Odicea-Assertion`).

## La regla

**Si la app decide permisos según quién es el usuario (qué ve, qué edita, si es admin), verifica la aserción. Nunca confíes en `X-Odicea-Email` sola:** otra app del clúster puede llamar directo a la tuya y escribir cualquier correo ahí. Si la app solo muestra el nombre, las cabeceras de texto bastan.

Una aserción válida cumple todo esto: firma EdDSA con una llave de `ODICEA_AUTH_JWKS_URL` (por `kid`), `iss` = `ODICEA_AUTH_ISSUER`, `aud` = `ODICEA_AUTH_AUDIENCE`, no vencida (con 30 s de margen), claim `typ` = `odicea-assertion` y `sub` no vacío. Claims útiles: `sub`, `email`, `name`, `org` (la organización del usuario), app\_org (la dueña de la app), `env`, `mode`. Cachea las llaves (10 min) y no las bajes en cada pedido.

Las llaves viajan por la red interna del clúster (HTTP dentro del clúster). Cuando TI rota la llave, la vieja se sigue aceptando hasta que TI la retira y hasta 10 min en la caché de la app: no hay que hacer nada en el código.

## Desarrollo local (sin Odicea)

Fuera de Odicea las variables `ODICEA_AUTH_*` **no existen**. Decide ese caso **de forma explícita**: si faltan, la app corre en «modo desarrollo» sin verificar usuarios (y con un usuario de prueba si la app lo necesita, creado por ti) y lo dice en el log al arrancar. **Nunca** aceptes pedidos sin verificar por defecto en producción: si faltan las variables en Odicea, es un error de configuración, no un permiso. Para saber si es desarrollo usa una variable propia (por ejemplo `APP_DEV=1`, que solo existe en tu máquina), no la ausencia de `ODICEA_AUTH_*`.

## Go

La librería es `pkg/odiceaauth` (solo biblioteca estándar: no importa nada más). Lo recomendado es **copiar la carpeta** `pkg/odiceaauth` del repositorio de Odicea dentro de la app (por ejemplo en `internal/odiceaauth`): `go get github.com/Abraxas-365/odyssey/pkg/odiceaauth` traería al `go.mod` de la app todo el módulo de Odicea y, por ahora, ese módulo no está publicado.

```go
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/Abraxas-365/odyssey/pkg/odiceaauth"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		id, _ := odiceaauth.FromContext(r.Context())
		fmt.Fprintf(w, "Hola, %s (%s)", id.Name, id.Email)
	})

	var app http.Handler = mux
	if os.Getenv("APP_DEV") == "" { // en Odicea: verifica siempre
		v, err := odiceaauth.FromEnv() // falla si faltan ODICEA_AUTH_*
		if err != nil {
			log.Fatal(err)
		}
		app = v.Require(mux) // 401 si la aserción no es válida
	} else {
		log.Println("MODO DESARROLLO: sin verificar usuarios")
	}
	log.Fatal(http.ListenAndServe(":"+os.Getenv("PORT"), app))
}
```

`odiceaauth.FromContext(r.Context())` devuelve la `Identity` (`Subject`, `Email`, `Name`, `Org`, `AppOrg`, `Env`, `Mode`). Detrás de `Require`, las cabeceras `X-Odicea-User|Email|Name|Org` ya traen los valores verificados. En modo desarrollo `FromContext` devuelve `ok=false`: decide tú qué mostrar.

## Node (`jose`)

`npm install jose`. El `typ` del encabezado es `JWT` y el claim `typ` es `odicea-assertion`: este se revisa a mano.

```js
import { createRemoteJWKSet, jwtVerify } from "jose";

const jwks = createRemoteJWKSet(new URL(process.env.ODICEA_AUTH_JWKS_URL));

export async function identity(req) {
  const token = req.headers[(process.env.ODICEA_AUTH_HEADER || "x-odicea-assertion").toLowerCase()];
  if (!token) throw new Error("sin aserción");
  const { payload } = await jwtVerify(token, jwks, {
    issuer: process.env.ODICEA_AUTH_ISSUER,
    audience: process.env.ODICEA_AUTH_AUDIENCE,
    algorithms: ["EdDSA"],
    typ: "JWT",
    clockTolerance: 30,
    requiredClaims: ["sub", "exp", "iat"],
  });
  if (payload.typ !== "odicea-assertion") throw new Error("aserción de otro tipo");
  return { id: payload.sub, email: payload.email, name: payload.name, org: payload.org };
}
```

Si `identity` lanza un error, responde 401. Si `ODICEA_AUTH_JWKS_URL` no existe, `new URL(undefined)` falla: maneja ese caso como «desarrollo» explícito (ver arriba).

## Python (`PyJWT`)

`pip install "PyJWT[crypto]"`.

```python
import os
import jwt  # PyJWT
from jwt import PyJWKClient

# timeout corto: si la puerta no responde, la app falla rápido (401), no se cuelga.
_jwks = PyJWKClient(os.environ["ODICEA_AUTH_JWKS_URL"], cache_keys=True, lifespan=600, timeout=5)

def identity(token: str) -> dict:
    key = _jwks.get_signing_key_from_jwt(token).key
    payload = jwt.decode(
        token,
        key,
        algorithms=["EdDSA"],
        audience=os.environ["ODICEA_AUTH_AUDIENCE"],
        issuer=os.environ["ODICEA_AUTH_ISSUER"],
        leeway=30,
        options={"require": ["exp", "iat", "sub"]},
    )
    if payload.get("typ") != "odicea-assertion":
        raise jwt.InvalidTokenError("aserción de otro tipo")
    return payload  # sub, email, name, org, app_org, env, mode
```

Lee la cabecera con `request.headers["X-Odicea-Assertion"]`; si falta o `identity` lanza `jwt.PyJWTError`, responde 401.

`PyJWKClient` vuelve a bajar las llaves cada vez que llega un `kid` que no conoce, sin límite. Si la app recibe mucho tráfico de fuera del clúster, rechaza antes un `kid` desconocido que ya se pidió hace menos de 10 s (la librería de Go y `jose` ya lo hacen solos).

## Antes de terminar

- ¿La app decide permisos por usuario? Entonces verifica la aserción (arriba), no uses `X-Odicea-Email` sola.
- No metas inicio de sesión propio: Odicea ya lo hace; esto solo te dice **quién** entró.
- Dile al usuario, en una frase, qué permisos quedaron según quién entra.
