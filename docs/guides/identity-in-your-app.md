# Know who is using your app

When someone opens your app, Odicea has already signed them in and tells your app who they are. Your app can greet them by name or decide what they may see, without building a login.

This page is the most technical of the guides. If you build with an assistant, you can ask it to do this for you (see the end of the page).

## When it applies

Only for apps whose **Compartir** setting is **Solo tu equipo** or **Toda la empresa**. An app set to **Cualquiera con el enlace** has no sign-in, so it gets no identity. See [Who can open an app](who-can-open-it.md).

## What your app receives

With every request, Odicea adds these headers:

| Header | What it is | Use it to |
| :--- | :--- | :--- |
| `X-Odicea-User` | The person's id | Show |
| `X-Odicea-Email` | Their email | Show |
| `X-Odicea-Name` | Their name | Show ("Hola, Lucía") |
| `X-Odicea-Org` | Their organization | Show |
| `X-Odicea-Assertion` | A signed proof of who they are, valid for two minutes | **Verify** before deciding anything |

Odicea clears these headers on the way in, so a visitor cannot set them by hand. But another app inside the same cluster could call yours directly and write any email in the plain headers.

> [!IMPORTANT]
> If your app decides permissions by person (what they see, edit or whether they are an admin), verify `X-Odicea-Assertion`. Never trust `X-Odicea-Email` alone. If your app only shows the name, the plain headers are enough.

Odicea also gives the app four environment variables, only for apps that need sign-in. Do not set them yourself:

| Variable | Value |
| :--- | :--- |
| `ODICEA_AUTH_JWKS_URL` | Where to download the public keys that check the proof |
| `ODICEA_AUTH_ISSUER` | The expected issuer: `odicea-gate` |
| `ODICEA_AUTH_AUDIENCE` | The address of your app, as the browser writes it |
| `ODICEA_AUTH_HEADER` | The header that carries the proof: `X-Odicea-Assertion` |

## What is inside the proof

`X-Odicea-Assertion` is a standard signed token (a JWT, signed with EdDSA/Ed25519). Its claims:

| Claim | Meaning |
| :--- | :--- |
| `sub` | The person's id. Always present. |
| `email`, `name` | Their email and name. They can be empty. |
| `org` | The person's organization. |
| `app_org` | The organization that owns the app. |
| `env` | The app's environment. |
| `mode` | `team` or `organization`. |
| `iss` | `odicea-gate` |
| `aud` | The address of your app, so a proof for one app is useless in another. |
| `iat`, `exp` | When it was made and when it expires, two minutes later. |
| `typ` | `odicea-assertion` |

A proof is valid when all of these hold: it is signed with one of the published keys (matched by its `kid`), `iss` and `aud` match the variables above, it has not expired (30 seconds of clock difference are allowed), `typ` is `odicea-assertion` and `sub` is not empty. Cache the keys for a few minutes instead of downloading them on every request. When IT changes the key, the old one is still accepted for a while, so you change nothing.

## In Go

Use the library `pkg/odiceaauth`. It needs only the Go standard library. The recommended way is to copy the `pkg/odiceaauth` folder of the Odicea repository into your app (for example into `internal/odiceaauth`), which keeps your `go.mod` small.

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
	if os.Getenv("APP_DEV") == "" { // in Odicea: always verify
		v, err := odiceaauth.FromEnv() // fails if ODICEA_AUTH_* are missing
		if err != nil {
			log.Fatal(err)
		}
		app = v.Require(mux) // 401 if the proof is not valid
	} else {
		log.Println("DEVELOPMENT MODE: users are not verified")
	}
	log.Fatal(http.ListenAndServe(":"+os.Getenv("PORT"), app))
}
```

- `FromEnv()` builds the checker from the four variables and caches the keys.
- `Require(next)` answers 401 to any request whose proof is not valid, and otherwise passes it on with the person's identity. Behind it, the plain `X-Odicea-*` headers hold the verified values.
- `odiceaauth.FromContext(r.Context())` returns the identity: `Subject`, `Email`, `Name`, `Org`, `AppOrg`, `Env`, `Mode`.
- To check a proof yourself, call `v.Verify(ctx, token)`.

## From another language

Verify the proof with any JWT library, downloading the keys from `ODICEA_AUTH_JWKS_URL`. The libraries must accept EdDSA.

**JavaScript** (`npm install jose`):

```js
import { createRemoteJWKSet, jwtVerify } from "jose";

const jwks = createRemoteJWKSet(new URL(process.env.ODICEA_AUTH_JWKS_URL));

export async function identity(req) {
  const token = req.headers[(process.env.ODICEA_AUTH_HEADER || "x-odicea-assertion").toLowerCase()];
  if (!token) throw new Error("no assertion");
  const { payload } = await jwtVerify(token, jwks, {
    issuer: process.env.ODICEA_AUTH_ISSUER,
    audience: process.env.ODICEA_AUTH_AUDIENCE,
    algorithms: ["EdDSA"],
    typ: "JWT",
    clockTolerance: 30,
    requiredClaims: ["sub", "exp", "iat"],
  });
  if (payload.typ !== "odicea-assertion") throw new Error("wrong kind of assertion");
  return { id: payload.sub, email: payload.email, name: payload.name, org: payload.org };
}
```

**Python** (`pip install "PyJWT[crypto]"`):

```python
import os
import jwt  # PyJWT
from jwt import PyJWKClient

# short timeout: if the gate does not answer, the app fails fast (401), not hangs
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
        raise jwt.InvalidTokenError("wrong kind of assertion")
    return payload  # sub, email, name, org, app_org, env, mode
```

In both cases, read the header `X-Odicea-Assertion`, and answer 401 if it is missing or `identity` fails. The `typ` claim (not the token's header) is checked by hand in both examples.

## On your own computer

Outside Odicea the `ODICEA_AUTH_*` variables do not exist, and nobody signs in. Decide that case on purpose: if the variables are missing, run in an explicit "development mode" that says so in the log when it starts. Never accept unverified requests by default in production.

## With your assistant

```text
Quiero que la app Portal de proveedores salude a cada persona por su nombre
y que solo Ana Torres vea la pantalla de administración. Usa la identidad que
manda Odicea.
```

The assistant knows this page's rules: it verifies the proof before it decides permissions, does not add its own sign-in, and tells you which permissions result for each person.

## Who may do what

Nothing here needs a permission: the identity is part of every request that reaches an app with sign-in. Who may open the app is set in **Compartir**, which needs `odyssey:projects:write`.

For how the sign-in and the keys work, read [Security model](../explanation/security-model.md) and [the access contract](../access.md).

## Next steps

- [Who can open an app](who-can-open-it.md)
- [Publish an app](publish.md)
