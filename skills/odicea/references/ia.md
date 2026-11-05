# Usar la IA desde el código de la app

Con `enable_ai` la app recibe, como variables de entorno, todo lo que necesita. **No hay nada que configurar en el código**: las librerías oficiales leen estas variables por su cuenta.

| Variable | Para qué |
|---|---|
| `OPENAI_API_KEY` | La llave propia de la app (compatible con OpenAI). |
| `OPENAI_BASE_URL` | La dirección de la IA para la librería de OpenAI (ya termina en `/v1`). |
| `ANTHROPIC_API_KEY` | La misma llave, para la librería de Anthropic. |
| `ANTHROPIC_BASE_URL` | La dirección de la IA para la librería de Anthropic (sin `/v1`). |

La llave no se escribe en el código, ni en `.env`, ni en `set_env`, ni se pide con `request_secret`: Odicea la entrega y la renueva. Esos cuatro nombres están reservados mientras la IA esté activa.

## Reglas

1. **Crea el cliente sin argumentos de llave ni de dirección.** Si los pones a mano, rompes la conexión cuando Odicea renueve la llave.
2. **No cuentes con un modelo fijo.** Los disponibles dependen de la organización: pregúntalos con la propia API (`/v1/models`) o lee el nombre del modelo de una variable normal (por ejemplo `AI_MODEL`, puesta con `set_env`) y falla con un mensaje claro si falta.
3. **No imprimas ni guardes la llave** (ni en logs, ni en errores, ni en respuestas al navegador). La IA se llama **desde el servidor** de la app, nunca desde el navegador: el navegador no debe recibir la llave.
4. Si la llamada falla con 401 o 403, la llave se renovó o la IA se desactivó: no reintentes sin fin; reinicia la lectura de las variables o avisa al usuario.

## Python

```python
# pip install openai anthropic
from openai import OpenAI
from anthropic import Anthropic
import os

model = os.environ["AI_MODEL"]  # un nombre que aparezca en client.models.list()

openai_client = OpenAI()        # lee OPENAI_API_KEY y OPENAI_BASE_URL
r = openai_client.chat.completions.create(
    model=model,
    messages=[{"role": "user", "content": "Resume este texto: ..."}],
)
print(r.choices[0].message.content)

anthropic_client = Anthropic()  # lee ANTHROPIC_API_KEY y ANTHROPIC_BASE_URL
m = anthropic_client.messages.create(
    model=model,
    max_tokens=512,
    messages=[{"role": "user", "content": "Resume este texto: ..."}],
)
print(m.content[0].text)
```

## Node (TypeScript o JavaScript)

```ts
// npm install openai @anthropic-ai/sdk
import OpenAI from "openai";
import Anthropic from "@anthropic-ai/sdk";

const model = process.env.AI_MODEL!; // un nombre que aparezca en openai.models.list()

const openai = new OpenAI(); // lee OPENAI_API_KEY y OPENAI_BASE_URL
const r = await openai.chat.completions.create({
  model,
  messages: [{ role: "user", content: "Resume este texto: ..." }],
});
console.log(r.choices[0].message.content);

const anthropic = new Anthropic(); // lee ANTHROPIC_API_KEY y ANTHROPIC_BASE_URL
const m = await anthropic.messages.create({
  model,
  max_tokens: 512,
  messages: [{ role: "user", content: "Resume este texto: ..." }],
});
console.log(m.content);
```

## Go (sin librería, con `net/http`)

La dirección y la llave vienen de las mismas variables:

```go
req, _ := http.NewRequest("POST", os.Getenv("OPENAI_BASE_URL")+"/chat/completions", body)
req.Header.Set("Authorization", "Bearer "+os.Getenv("OPENAI_API_KEY"))
req.Header.Set("Content-Type", "application/json")
```

## Probarlo en local

Fuera de Odicea esas variables no existen. Para probar en la máquina del usuario, no pidas ni escribas una llave en el chat: deja que el código falle con un mensaje claro («falta OPENAI_API_KEY») y di al usuario que la IA solo funciona ya publicada, o que TI le dé una llave de desarrollo para su entorno.
